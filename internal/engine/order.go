package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/notify"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/solver"
)

const (
	// orderTimeout 包含人工验证等待时间。
	orderTimeout = 8 * time.Minute
	// retryCooldown 自动锁单失败后同一关注的冷却时间，防止每轮都打站点。
	retryCooldown = 45 * time.Second
	maxAttempts   = 3
	attemptDelay  = 1500 * time.Millisecond
)

// ErrOrderInFlight 同一关注已有锁单在执行。
var ErrOrderInFlight = errors.New("order already in flight for this watch")

// ErrNoOrderer Provider 不支持下单。
var ErrNoOrderer = errors.New("provider does not support ordering")

// triggerWatches 对可下单商品的自动锁单关注异步执行锁单。
func (e *Engine) triggerWatches(site domain.Site, products []domain.Product, watches []domain.Watch) {
	byID := make(map[string]domain.Product, len(products))
	for _, product := range products {
		byID[product.ID] = product
	}
	for _, watch := range watches {
		product, ok := byID[watch.ProductID]
		if !ok || !e.shouldAutoOrder(watch, product) {
			continue
		}
		e.wg.Add(1)
		go func(watch domain.Watch, product domain.Product) {
			defer e.wg.Done()
			if _, err := e.executeOrder(e.rootCtx, site, watch, product, false, true); err != nil && !errors.Is(err, ErrOrderInFlight) {
				e.log.Warn("auto order failed", "watch", watch.ID, "err", err)
			}
		}(watch, product)
	}
}

// shouldAutoOrder 自动锁单的全部前置条件。
func (e *Engine) shouldAutoOrder(watch domain.Watch, product domain.Product) bool {
	if !watch.Enabled || !watch.AutoOrder || watch.AccountID == "" || !product.Stock.Status.Orderable() {
		return false
	}
	if watch.MaxOrders > 0 && watch.OrdersPlaced >= watch.MaxOrders {
		return false
	}
	if !watch.LastTriggered.IsZero() && time.Since(watch.LastTriggered) < retryCooldown {
		return false
	}
	return withinPriceLimit(watch, product)
}

// withinPriceLimit 价格上限按所选周期价格 × 数量判断（不含优惠）。
func withinPriceLimit(watch domain.Watch, product domain.Product) bool {
	if watch.MaxPriceCents <= 0 {
		return true
	}
	price, ok := product.PriceFor(watch.Cycle)
	if !ok {
		return false
	}
	return price.Amount.Cents*int64(maxInt(watch.Qty, 1)) <= watch.MaxPriceCents
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (e *Engine) watchMutex(watchID string) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	lock, ok := e.orderLock[watchID]
	if !ok {
		lock = &sync.Mutex{}
		e.orderLock[watchID] = lock
	}
	return lock
}

// BuildSpec 合并关注规则与账号默认值。
func BuildSpec(watch domain.Watch, account domain.Account, dryRun bool) domain.OrderSpec {
	spec := domain.OrderSpec{
		ProductID: watch.ProductID,
		Cycle:     watch.Cycle,
		Options:   watch.Options,
		OS:        firstNonEmpty(watch.OS, account.Defaults.OS),
		Hostname:  firstNonEmpty(watch.Hostname, account.Defaults.Hostname),
		SSHKey:    account.Defaults.SSHKey,
		Coupon:    watch.Coupon,
		Gateway:   firstNonEmpty(watch.Gateway, account.Defaults.Gateway),
		Qty:       maxInt(watch.Qty, 1),
		DryRun:    dryRun,
	}
	return spec
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// executeOrder 锁单主流程：同一关注串行（TryLock，正在执行则直接返回）、记录订单、重试瞬时错误、通知结果。
func (e *Engine) executeOrder(ctx context.Context, site domain.Site, watch domain.Watch, product domain.Product, dryRun, automatic bool) (domain.Order, error) {
	lock := e.watchMutex(watch.ID)
	if !lock.TryLock() {
		return domain.Order{}, ErrOrderInFlight
	}
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(ctx, orderTimeout)
	defer cancel()
	if automatic {
		// 先记下触发时间，失败也进入冷却。
		if err := e.touchWatch(ctx, watch.ID, false); err != nil {
			return domain.Order{}, err
		}
	}
	account, err := e.store.GetAccount(ctx, watch.AccountID)
	if err != nil {
		return domain.Order{}, fmt.Errorf("load account: %w", err)
	}
	instance, err := e.providerFor(site)
	if err != nil {
		return domain.Order{}, err
	}
	orderer, ok := instance.(provider.Orderer)
	if !ok {
		return domain.Order{}, ErrNoOrderer
	}
	spec := BuildSpec(watch, account, dryRun)
	order := domain.Order{ID: newID(), WatchID: watch.ID, SiteID: site.ID, AccountID: account.ID, ProductID: product.ID, ProductName: firstNonEmpty(product.Name, watch.ProductName), Spec: spec, Status: domain.OrderPending, CreatedAt: time.Now().UTC()}
	if err := e.store.SaveOrder(ctx, order); err != nil {
		return order, err
	}
	e.bus.Publish("order", order)
	env, err := e.buildEnv(site, &account)
	if err != nil {
		return order, err
	}
	started := time.Now()
	var result domain.OrderResult
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, err = orderer.PlaceOrder(ctx, env, spec)
		if err == nil || !retryable(err) || attempt == maxAttempts {
			break
		}
		e.log.Info("order attempt failed, retrying", "watch", watch.ID, "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
		case <-time.After(attemptDelay):
		}
		if ctx.Err() != nil {
			break
		}
	}
	e.persistSession(ctx, &account, env.Session)
	order.DurationMs = time.Since(started).Milliseconds()
	order.Result = result
	switch {
	case err != nil:
		order.Status = domain.OrderFailed
		order.Error = err.Error()
		if errors.Is(err, provider.ErrSessionExpired) {
			e.markAccountStatus(ctx, account.ID, domain.AccountExpired, err.Error())
		}
	case dryRun:
		order.Status = domain.OrderDryRun
	default:
		order.Status = domain.OrderPlaced
		if touchErr := e.touchWatch(ctx, watch.ID, true); touchErr != nil {
			e.log.Warn("update watch", "err", touchErr)
		}
	}
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer saveCancel()
	if saveErr := e.store.SaveOrder(saveCtx, order); saveErr != nil {
		e.log.Warn("save order", "err", saveErr)
	}
	e.bus.Publish("order", order)
	e.recordEvent(saveCtx, orderEvent(site, order), orderMessage(site, order))
	return order, err
}

// retryable 缺货、会话失效、验证取消 / 超时、配置错误等不重试。
func retryable(err error) bool {
	switch {
	case errors.Is(err, provider.ErrOutOfStock), errors.Is(err, provider.ErrSessionExpired), errors.Is(err, provider.ErrNotSupported), errors.Is(err, provider.ErrCouponInvalid):
		return false
	case errors.Is(err, solver.ErrChallengeCancelled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return false
	}
	return true
}

func orderEvent(site domain.Site, order domain.Order) domain.Event {
	title := fmt.Sprintf("%s 锁单成功: %s", site.Name, order.ProductName)
	detail := order.Result.PayURL
	switch order.Status {
	case domain.OrderFailed:
		title = fmt.Sprintf("%s 锁单失败: %s", site.Name, order.ProductName)
		detail = order.Error
	case domain.OrderDryRun:
		title = fmt.Sprintf("%s 试下单通过: %s", site.Name, order.ProductName)
		detail = order.Result.Message
	}
	return domain.Event{Type: domain.EventOrder, SiteID: site.ID, ProductID: order.ProductID, Title: title, Detail: detail}
}

func orderMessage(site domain.Site, order domain.Order) *notify.Message {
	event := orderEvent(site, order)
	if order.Status == domain.OrderDryRun {
		return nil
	}
	body := fmt.Sprintf("周期 %s · 数量 %d · 优惠码 %s\n应付 %s", order.Spec.Cycle, order.Spec.Qty, firstNonEmpty(order.Spec.Coupon, "无"), FormatMoney(order.Result.Total))
	if order.Status == domain.OrderFailed {
		body = order.Error
	} else {
		body += "\n订单 " + firstNonEmpty(order.Result.OrderID, "-") + " · 账单 " + firstNonEmpty(order.Result.InvoiceID, "-") + "，请尽快支付"
	}
	return &notify.Message{Title: event.Title, Body: body, URL: order.Result.PayURL}
}

// touchWatch 更新触发时间；placed 时累加成功次数。
func (e *Engine) touchWatch(ctx context.Context, watchID string, placed bool) error {
	e.docMu.Lock()
	defer e.docMu.Unlock()
	watch, err := e.store.GetWatch(ctx, watchID)
	if err != nil {
		return err
	}
	watch.LastTriggered = time.Now().UTC()
	if placed {
		watch.OrdersPlaced++
	}
	return e.store.SaveWatch(ctx, watch)
}

// providerFor 优先复用运行中的实例（保留 Inertia 版本等缓存）。
func (e *Engine) providerFor(site domain.Site) (provider.Provider, error) {
	e.mu.RLock()
	runner, ok := e.sites[site.ID]
	e.mu.RUnlock()
	if ok {
		return runner.provider, nil
	}
	return provider.New(site)
}

// OrderWatch 手动锁单 / 试下单。
func (e *Engine) OrderWatch(ctx context.Context, watchID string, dryRun bool) (domain.Order, error) {
	site, watch, product, err := e.loadWatch(ctx, watchID)
	if err != nil {
		return domain.Order{}, err
	}
	if watch.AccountID == "" {
		return domain.Order{}, errors.New("watch has no account")
	}
	return e.executeOrder(ctx, site, watch, product, dryRun, false)
}

// QuoteWatch 算价（校验优惠码）。
func (e *Engine) QuoteWatch(ctx context.Context, watchID string) (domain.Quote, error) {
	site, watch, _, err := e.loadWatch(ctx, watchID)
	if err != nil {
		return domain.Quote{}, err
	}
	if watch.AccountID == "" {
		return domain.Quote{}, errors.New("watch has no account")
	}
	account, err := e.store.GetAccount(ctx, watch.AccountID)
	if err != nil {
		return domain.Quote{}, err
	}
	instance, err := e.providerFor(site)
	if err != nil {
		return domain.Quote{}, err
	}
	orderer, ok := instance.(provider.Orderer)
	if !ok {
		return domain.Quote{}, ErrNoOrderer
	}
	env, err := e.buildEnv(site, &account)
	if err != nil {
		return domain.Quote{}, err
	}
	quote, err := orderer.Quote(ctx, env, BuildSpec(watch, account, true))
	e.persistSession(ctx, &account, env.Session)
	return quote, err
}

func (e *Engine) loadWatch(ctx context.Context, watchID string) (domain.Site, domain.Watch, domain.Product, error) {
	watch, err := e.store.GetWatch(ctx, watchID)
	if err != nil {
		return domain.Site{}, domain.Watch{}, domain.Product{}, err
	}
	site, err := e.store.GetSite(ctx, watch.SiteID)
	if err != nil {
		return domain.Site{}, domain.Watch{}, domain.Product{}, err
	}
	product, err := e.store.GetProduct(ctx, site.ID, watch.ProductID)
	if err != nil {
		product = domain.Product{ID: watch.ProductID, Name: watch.ProductName}
	}
	return site, watch, product, nil
}

// CheckAccount 校验会话并更新账号状态。
func (e *Engine) CheckAccount(ctx context.Context, accountID string) (domain.Account, error) {
	account, err := e.store.GetAccount(ctx, accountID)
	if err != nil {
		return domain.Account{}, err
	}
	site, err := e.store.GetSite(ctx, account.SiteID)
	if err != nil {
		return domain.Account{}, err
	}
	instance, err := e.providerFor(site)
	if err != nil {
		return domain.Account{}, err
	}
	checker, ok := instance.(provider.SessionChecker)
	if !ok {
		return account, provider.ErrNotSupported
	}
	env, err := e.buildEnv(site, &account)
	if err != nil {
		return domain.Account{}, err
	}
	info, checkErr := checker.CheckSession(ctx, env)
	e.persistSession(ctx, &account, env.Session)
	return e.updateAccount(ctx, accountID, func(stored *domain.Account) {
		stored.CheckedAt = time.Now().UTC()
		if checkErr != nil {
			stored.Status = domain.AccountUnknown
			if errors.Is(checkErr, provider.ErrSessionExpired) {
				stored.Status = domain.AccountExpired
			}
			stored.LastError = checkErr.Error()
			return
		}
		stored.Status = domain.AccountActive
		stored.LastError = ""
		stored.Identity = info.Identity
		stored.Balance = info.Balance
	})
}

func newID() string {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// NewID 对外的短 ID 生成。
func NewID() string { return newID() }
