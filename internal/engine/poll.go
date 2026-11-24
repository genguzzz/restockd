package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/notify"
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
	trigger  chan chan error
	cancel   context.CancelFunc
	done     chan struct{}
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
	runner := &siteRunner{site: site, provider: instance, trigger: make(chan chan error), cancel: cancel, done: make(chan struct{})}
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

// PollNow 立即轮询一次并等待结果。
func (e *Engine) PollNow(ctx context.Context, siteID string) error {
	e.mu.RLock()
	runner, ok := e.sites[siteID]
	e.mu.RUnlock()
	if !ok {
		return fmt.Errorf("site %s is not running (disabled?)", siteID)
	}
	reply := make(chan error, 1)
	select {
	case runner.trigger <- reply:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) runSite(ctx context.Context, runner *siteRunner) {
	defer e.wg.Done()
	defer close(runner.done)
	meta := runner.provider.Meta()
	interval := provider.EffectiveInterval(meta, runner.site)
	// 启动时错峰，避免多个站点同时发请求。
	timer := time.NewTimer(time.Duration(rand.Int64N(int64(3 * time.Second))))
	defer timer.Stop()
	failures := 0
	for {
		var reply chan error
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case reply = <-runner.trigger:
		}
		err := e.pollSite(ctx, runner)
		if reply != nil {
			reply <- err
		}
		if ctx.Err() != nil {
			return
		}
		wait := jitter(interval)
		if err != nil {
			failures++
			wait = backoff(interval, failures, err)
		} else {
			failures = 0
		}
		e.markNextPoll(ctx, runner.site.ID, time.Now().Add(wait), failures)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
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

// pollSite 抓取目录 → 比对 → 落库 → 广播 → 触发关注规则。
func (e *Engine) pollSite(ctx context.Context, runner *siteRunner) error {
	ctx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()
	site, err := e.store.GetSite(ctx, runner.site.ID)
	if err != nil {
		return err
	}
	var account *domain.Account
	if site.MonitorAccount != "" {
		loaded, err := e.store.GetAccount(ctx, site.MonitorAccount)
		if err == nil {
			account = &loaded
		}
	}
	env, err := e.buildEnv(site, account)
	if err != nil {
		return err
	}
	started := time.Now()
	catalog, pollErr := runner.provider.Catalog(ctx, env)
	e.persistSession(ctx, account, env.Session)
	if pollErr != nil {
		e.markPollResult(ctx, site.ID, pollErr)
		e.reportPollError(ctx, site, pollErr)
		return pollErr
	}
	previous, err := e.store.ListProducts(ctx, site.ID)
	if err != nil {
		return err
	}
	for i := range catalog.Products {
		catalog.Products[i].SiteID = site.ID
		catalog.Products[i].SortPrices()
	}
	if err := e.store.ReplaceProducts(ctx, site.ID, catalog.Products); err != nil {
		return err
	}
	e.markPollResult(ctx, site.ID, nil)
	e.log.Debug("polled", "site", site.ID, "products", len(catalog.Products), "elapsed", time.Since(started))
	watches, err := e.store.ListWatches(ctx, site.ID)
	if err != nil {
		return err
	}
	watched := watchIndex(watches)
	for _, event := range Diff(site.ID, previous, catalog.Products) {
		e.recordEvent(ctx, event, e.messageFor(site, event, watched))
	}
	e.triggerWatches(site, catalog.Products, watches)
	return nil
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

// messageFor 只有被关注商品的补货 / 价格变动才推送；新品上架对所有商品推送。
func (e *Engine) messageFor(site domain.Site, event domain.Event, watched map[string]bool) *notify.Message {
	switch event.Type {
	case domain.EventRestock, domain.EventPriceChange:
		if !watched[event.ProductID] {
			return nil
		}
	case domain.EventNewProduct:
	default:
		return nil
	}
	link := ""
	if product, err := e.store.GetProduct(context.Background(), site.ID, event.ProductID); err == nil {
		link = product.URL
	}
	return &notify.Message{Title: "[" + site.Name + "] " + event.Title, Body: event.Detail, URL: link}
}

func (e *Engine) reportPollError(ctx context.Context, site domain.Site, err error) {
	detail := err.Error()
	if fetch.IsChallenge(err) {
		detail += "；请在站点设置中启用 FlareSolverr，或给监控账号粘贴同一 IP/UA 获取的 cf_clearance Cookie"
	}
	if errors.Is(err, provider.ErrSessionExpired) && site.MonitorAccount != "" {
		e.markAccountStatus(ctx, site.MonitorAccount, domain.AccountExpired, err.Error())
	}
	e.recordEvent(ctx, domain.Event{Type: domain.EventPollError, SiteID: site.ID, Title: site.Name + " 轮询失败", Detail: detail}, nil)
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

func (e *Engine) markAccountStatus(ctx context.Context, accountID string, status domain.AccountStatus, message string) {
	account, err := e.updateAccount(ctx, accountID, func(account *domain.Account) {
		account.Status = status
		account.LastError = message
		account.CheckedAt = time.Now().UTC()
	})
	if err == nil && status == domain.AccountExpired {
		e.recordEvent(ctx, domain.Event{Type: domain.EventPollError, SiteID: account.SiteID, Title: "账号会话失效: " + account.Label, Detail: "请重新粘贴 Cookie"},
			&notify.Message{Title: "账号会话失效: " + account.Label, Body: "站点 " + account.SiteID + " 需要重新粘贴 Cookie"})
	}
}
