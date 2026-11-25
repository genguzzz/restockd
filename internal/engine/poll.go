package engine

import (
	"context"
	"errors"
	"maps"
	"math/rand/v2"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/store"
)

const (
	maxBackoff  = 10 * time.Minute
	pollTimeout = 90 * time.Second
)

// siteRunner 单站点调度协程。
type siteRunner struct {
	site     domain.Site
	provider provider.Provider
	trigger  chan chan pollResult
	wake     chan wakeKind
	cancel   context.CancelFunc
	done     chan struct{}
	// hiddenWarned 已记过「商品隐藏、跳过自动锁单」日志的关注（只在调度协程内访问）。
	hiddenWarned map[string]bool
}

// pollResult 一次轮询的结果：商品数与错误。
type pollResult struct {
	products int
	err      error
}

// ReloadSite 新建 / 更新站点后重启其调度协程；禁用站点只停止不启动。
func (e *Engine) ReloadSite(site domain.Site) error {
	e.StopSite(site.ID)
	if !site.Enabled {
		return nil
	}
	instance, err := provider.New(site)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(e.rootCtx)
	runner := &siteRunner{site: site, provider: instance, trigger: make(chan chan pollResult), wake: make(chan wakeKind, 1), cancel: cancel, done: make(chan struct{}), hiddenWarned: map[string]bool{}}
	e.mu.Lock()
	e.sites[site.ID] = runner
	delete(e.sessions, "anon:"+site.ID)
	e.mu.Unlock()
	e.wg.Add(1)
	go e.runSite(ctx, runner)
	return nil
}

// StopSite 停止站点调度并等待协程退出。
func (e *Engine) StopSite(siteID string) {
	e.mu.Lock()
	runner, ok := e.sites[siteID]
	delete(e.sites, siteID)
	e.mu.Unlock()
	if ok {
		runner.cancel()
		<-runner.done
	}
}

// PollNow 立即轮询一次并等待结果，返回本次抓到的商品数。
func (e *Engine) PollNow(ctx context.Context, siteID string) (int, error) {
	e.mu.RLock()
	runner, ok := e.sites[siteID]
	e.mu.RUnlock()
	if !ok {
		return 0, invalidf("site %s is not running (disabled?)", siteID)
	}
	reply := make(chan pollResult, 1)
	select {
	case runner.trigger <- reply:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	select {
	case result := <-reply:
		return result.products, result.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (e *Engine) runSite(ctx context.Context, runner *siteRunner) {
	defer e.wg.Done()
	defer close(runner.done)
	// 启动时错峰，避免多个站点同时发请求。
	timer := time.NewTimer(time.Duration(rand.Int64N(int64(3 * time.Second))))
	defer timer.Stop()
	resetTimer := func(wait time.Duration, failures int) {
		e.markNextPoll(ctx, runner.site.ID, time.Now().Add(wait), failures)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
	}
	failures := 0
	// nextAllowed 上次轮询 + 等待时间（失败时含退避 / Retry-After）：失败期间唤醒不得提前轮询。
	var lastPoll, nextAllowed time.Time
	for {
		var reply chan pollResult
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case reply = <-runner.trigger:
		case kind := <-runner.wake:
			if failures > 0 && time.Now().Before(nextAllowed) {
				continue
			}
			interval, watched := e.siteInterval(ctx, runner)
			sinceLast := time.Since(lastPoll)
			switch {
			case kind == wakeViewer && !watched && sinceLast >= viewerRefreshMin:
				// 有人来看：无关注站点立即刷一次。
			case lastPoll.IsZero() || sinceLast >= interval:
				// 新间隔下已到期。
			default:
				resetTimer(interval-sinceLast, failures)
				continue
			}
		}
		products, err := e.pollSite(ctx, runner)
		lastPoll = time.Now()
		if reply != nil {
			reply <- pollResult{products: products, err: err}
		}
		if ctx.Err() != nil {
			return
		}
		interval, _ := e.siteInterval(ctx, runner)
		wait := jitter(interval)
		if err != nil {
			failures++
			wait = backoff(interval, failures, err)
		} else {
			failures = 0
		}
		nextAllowed = lastPoll.Add(wait)
		resetTimer(wait, failures)
	}
}

// jitter ±10%，避免固定节奏被站点识别。
func jitter(interval time.Duration) time.Duration {
	spread := int64(interval) / 5
	if spread <= 0 {
		return interval
	}
	return interval - time.Duration(spread/2) + time.Duration(rand.Int64N(spread))
}

// backoff 指数退避，限流时至少等待 Retry-After。
func backoff(interval time.Duration, failures int, err error) time.Duration {
	wait := interval
	for i := 1; i < failures && wait < maxBackoff; i++ {
		wait *= 2
	}
	if wait > maxBackoff {
		wait = maxBackoff
	}
	var rateLimit *fetch.RateLimitError
	if errors.As(err, &rateLimit) && rateLimit.RetryAfter > wait {
		wait = rateLimit.RetryAfter
	}
	return wait
}

// pollSite 抓取目录 → 比对 → 落库 → 广播 → 触发关注规则，返回商品数。
func (e *Engine) pollSite(ctx context.Context, runner *siteRunner) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()
	site, err := e.store.GetSite(ctx, runner.site.ID)
	if err != nil {
		return 0, err
	}
	started := time.Now()
	var catalog domain.Catalog
	c, err := e.prepare(ctx, site, site.MonitorAccount)
	if err == nil {
		// 只有声明「库存需登录」的站点才在无 Cookie 时预先登录；公开库存先直接拉。
		err = e.run(ctx, c, runner.provider.Meta().Caps.StockNeedsLogin, func() error {
			var catalogErr error
			catalog, catalogErr = c.inst.Catalog(ctx, c.env)
			return catalogErr
		})
	}
	if err != nil {
		e.markPollResult(ctx, site.ID, err)
		e.reportPollError(ctx, site, err)
		return 0, err
	}
	previous, err := e.store.ListProducts(ctx, site.ID)
	if err != nil {
		return 0, err
	}
	byID := make(map[string]domain.Product, len(catalog.Products))
	for i := range catalog.Products {
		catalog.Products[i].SiteID = site.ID
		catalog.Products[i].SortPrices()
		byID[catalog.Products[i].ID] = catalog.Products[i]
	}
	if err := e.store.ReplaceProducts(ctx, site.ID, catalog.Products); err != nil {
		return 0, err
	}
	e.markPollResult(ctx, site.ID, nil)
	e.log.Debug("polled", "site", site.ID, "products", len(catalog.Products), "elapsed", time.Since(started))
	watches, err := e.store.ListWatches(ctx, site.ID)
	if err != nil {
		return len(catalog.Products), err
	}
	watched := watchIndex(watches)
	if idSchemeChanged(previous, catalog.Products) {
		// 商品 ID 与上次完全不重合：适配器换了 ID 体系（如 usvps24 数字 ID → slug），按首次抓取处理，
		// 不把全部商品当成新上架 / 补货刷屏；引用旧 ID 的关注规则需要用户重新选择商品。
		e.log.Warn("product ids changed completely, treating poll as baseline", "site", site.ID, "previous", len(previous), "current", len(catalog.Products), "watches", len(watches))
		previous = nil
	}
	// Diff 用完整目录（取消隐藏时不会一次性冒出积压事件），隐藏商品的事件直接丢弃。
	changes := Diff(site.ID, previous, catalog.Products)
	history := e.trackStock(ctx, site.ID, catalog.Products, changes)
	for _, change := range changes {
		if site.Hidden(change.ProductID) {
			continue
		}
		change.Detail = withStockHistory(change, history, time.Now())
		event, message := stockNotice(site, change, watched, byID)
		e.recordEvent(ctx, event, message)
	}
	e.triggerWatches(site, e.orderableProducts(runner, site, byID, watches), watches)
	return len(catalog.Products), nil
}

// orderableProducts 去掉隐藏商品（不自动锁单）；关注了隐藏商品的自动锁单规则每条只记一次日志。
func (e *Engine) orderableProducts(runner *siteRunner, site domain.Site, products map[string]domain.Product, watches []domain.Watch) map[string]domain.Product {
	if len(site.HiddenProducts) == 0 {
		return products
	}
	visible := maps.Clone(products)
	for _, id := range site.HiddenProducts {
		delete(visible, id)
	}
	for _, watch := range watches {
		if watch.Enabled && watch.AutoOrder && site.Hidden(watch.ProductID) && !runner.hiddenWarned[watch.ID] {
			runner.hiddenWarned[watch.ID] = true
			e.log.Info("auto order skipped: product is hidden", "site", site.ID, "watch", watch.ID, "product", watch.ProductID)
		}
	}
	return visible
}

// idSchemeChanged 两次都有商品但 ID 没有任何交集。
func idSchemeChanged(previous, current []domain.Product) bool {
	if len(previous) == 0 || len(current) == 0 {
		return false
	}
	seen := make(map[string]bool, len(previous))
	for _, product := range previous {
		seen[product.ID] = true
	}
	for _, product := range current {
		if seen[product.ID] {
			return false
		}
	}
	return true
}

// watchIndex 商品 ID → 是否有启用的通知型关注。
func watchIndex(watches []domain.Watch) map[string]bool {
	index := map[string]bool{}
	for _, watch := range watches {
		if watch.Enabled && watch.Notify {
			index[watch.ProductID] = true
		}
	}
	return index
}

// reportPollError 记轮询失败事件；监控账号会话失效时更新账号状态（变为失效时才通知，
// 自动登录失败已在 login 里按同样规则通知）。
func (e *Engine) reportPollError(ctx context.Context, site domain.Site, err error) {
	if errors.Is(err, provider.ErrSessionExpired) && site.MonitorAccount != "" {
		e.markAccountStatus(ctx, site.MonitorAccount, domain.AccountExpired, err.Error())
	}
	event, message := pollErrorNotice(site, err)
	e.recordEvent(ctx, event, message)
}

func (e *Engine) markPollResult(ctx context.Context, siteID string, pollErr error) {
	e.docMu.Lock()
	defer e.docMu.Unlock()
	site, err := e.store.GetSite(ctx, siteID)
	if err != nil {
		return
	}
	site.LastPollAt = time.Now().UTC()
	site.LastError = ""
	if pollErr != nil {
		site.LastError = pollErr.Error()
	}
	if err := e.store.SaveSite(ctx, site); err != nil {
		e.log.Warn("save site status", "err", err)
	}
}

func (e *Engine) markNextPoll(ctx context.Context, siteID string, next time.Time, failures int) {
	e.docMu.Lock()
	defer e.docMu.Unlock()
	site, err := e.store.GetSite(ctx, siteID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			e.log.Warn("load site", "err", err)
		}
		return
	}
	site.NextPollAt = next.UTC()
	site.ConsecutiveFail = failures
	if err := e.store.SaveSite(ctx, site); err != nil {
		e.log.Warn("save site", "err", err)
		return
	}
	e.bus.Publish("site", site)
}
