package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/solver"
	"github.com/genguzzz/restockd/internal/store"
)

const (
	// orderTimeout 包含人工验证等待时间。
	orderTimeout = 8 * time.Minute
	// retryCooldown 自动锁单失败后同一关注的冷却时间，防止每轮都打站点。
	retryCooldown = 45 * time.Second
	maxAttempts   = 3
	attemptDelay  = 1500 * time.Millisecond
)

// triggerWatches 对可下单商品的自动锁单关注异步执行锁单。
func (e *Engine) triggerWatches(site domain.Site, products map[string]domain.Product, watches []domain.Watch) {
	for _, watch := range watches {
		product, ok := products[watch.ProductID]
		if !ok || !e.shouldAutoOrder(watch, product) {
			continue
		}
		e.wg.Add(1)
		go func(watch domain.Watch, product domain.Product) {
			defer e.wg.Done()
			if _, err := e.orderWatch(e.rootCtx, site, watch, product, false, true, false); err != nil && !errors.Is(err, ErrOrderInFlight) {
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
	return price.Amount.Cents*int64(max(watch.Qty, 1)) <= watch.MaxPriceCents
}

// watchSpec 关注规则的下单参数（未合并账号默认值）。
func watchSpec(watch domain.Watch, dryRun bool) domain.OrderSpec {
	return domain.OrderSpec{ProductID: watch.ProductID, Cycle: watch.Cycle, Options: watch.Options, OS: watch.OS, Hostname: watch.Hostname, Coupon: watch.Coupon, Gateway: watch.Gateway, Qty: max(watch.Qty, 1), DryRun: dryRun}
}

// withAccountDefaults 未填写的系统 / 主机名 / SSH 公钥 / 支付方式回落到账号默认值。
func withAccountDefaults(spec domain.OrderSpec, account domain.Account) domain.OrderSpec {
	spec.OS = cmp.Or(spec.OS, account.Defaults.OS)
	spec.Hostname = cmp.Or(spec.Hostname, account.Defaults.Hostname)
	spec.SSHKey = cmp.Or(spec.SSHKey, account.Defaults.SSHKey)
	spec.Gateway = cmp.Or(spec.Gateway, account.Defaults.Gateway)
	return spec
}

// orderWatch 按关注规则锁单；同一关注不并发。手动下单（非 force）做 24 小时防重，自动锁单由 MaxOrders / 冷却约束。
func (e *Engine) orderWatch(ctx context.Context, site domain.Site, watch domain.Watch, product domain.Product, dryRun, automatic, force bool) (domain.Order, error) {
	order := domain.Order{WatchID: watch.ID, AccountID: watch.AccountID, ProductID: product.ID, ProductName: cmp.Or(product.Name, watch.ProductName), Spec: watchSpec(watch, dryRun)}
	return e.executeOrder(ctx, site, watchLockKey(watch.ID), order, automatic, !automatic && !force)
}

func watchLockKey(watchID string) string { return "watch:" + watchID }

// executeOrder 锁单主流程：同一 lockKey 串行（TryLock，正在执行则直接返回 ErrOrderInFlight）；
// 先建订单记录，之后任何失败都经 finishOrder 落为 failed、广播并记事件（自动锁单不会静默失败）。
// order 由调用方填好关注 / 账号 / 商品与未合并账号默认值的 Spec；dedupe 时真实下单先查防重窗口（不建记录直接拒绝）。
func (e *Engine) executeOrder(ctx context.Context, site domain.Site, lockKey string, order domain.Order, automatic, dedupe bool) (domain.Order, error) {
	lock := e.orderLocks.Get(lockKey)
	if !lock.TryLock() {
		return domain.Order{}, ErrOrderInFlight
	}
	defer lock.Unlock()
	if dedupe && !order.Spec.DryRun {
		existing, err := e.store.LatestPlacedOrder(ctx, site.ID, order.ProductID, order.AccountID, time.Now().Add(-e.Settings().Orders.MarkTTL()))
		if err == nil {
			return domain.Order{}, &DuplicateOrderError{Existing: existing}
		}
		if !errors.Is(err, store.ErrNotFound) {
			return domain.Order{}, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, orderTimeout)
	defer cancel()
	order.ID, order.SiteID, order.Status, order.CreatedAt = newID(), site.ID, domain.OrderPending, time.Now().UTC()
	started := time.Now()
	result, err := e.placeOrder(ctx, site, &order, automatic)
	e.finishOrder(site, &order, result, err, started)
	return order, err
}

// placeOrder 落 pending 记录、准备账号会话并下单，瞬时错误重试；order.Spec 补上账号默认值。
func (e *Engine) placeOrder(ctx context.Context, site domain.Site, order *domain.Order, automatic bool) (domain.OrderResult, error) {
	if err := e.store.SaveOrder(ctx, *order); err != nil {
		return domain.OrderResult{}, err
	}
	e.bus.Publish("order", *order)
	if automatic {
		// 先记下触发时间，失败也进入冷却。
		if err := e.touchWatch(ctx, order.WatchID, false); err != nil {
			return domain.OrderResult{}, err
		}
	}
	c, err := e.prepare(ctx, site, order.AccountID)
	if err != nil {
		return domain.OrderResult{}, fmt.Errorf("prepare: %w", err)
	}
	order.Spec = withAccountDefaults(order.Spec, *c.account)
	orderer, ok := c.inst.(provider.Orderer)
	if !ok {
		return domain.OrderResult{}, ErrNoOrderer
	}
	var result domain.OrderResult
	for attempt := 1; ; attempt++ {
		err = e.run(ctx, c, true, func() error {
			var placeErr error
			result, placeErr = orderer.PlaceOrder(ctx, c.env, order.Spec)
			return placeErr
		})
		if err == nil || !retryable(err) || attempt == maxAttempts {
			return result, err
		}
		e.log.Info("order attempt failed, retrying", "order", order.ID, "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return result, err
		case <-time.After(attemptDelay):
		}
	}
}

// finishOrder 唯一的收尾路径：定状态、保存、广播、记事件与通知。
func (e *Engine) finishOrder(site domain.Site, order *domain.Order, result domain.OrderResult, err error, started time.Time) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(e.rootCtx), 10*time.Second)
	defer cancel()
	order.DurationMs = time.Since(started).Milliseconds()
	order.Result = result
	switch {
	case err != nil:
		order.Status = domain.OrderFailed
		order.Error = err.Error()
		if errors.Is(err, provider.ErrSessionExpired) {
			e.markAccountStatus(ctx, order.AccountID, domain.AccountExpired, err.Error())
		}
	case order.Spec.DryRun:
		order.Status = domain.OrderDryRun
	default:
		order.Status = domain.OrderPlaced
		if order.WatchID != "" {
			if touchErr := e.touchWatch(ctx, order.WatchID, true); touchErr != nil {
				e.log.Warn("update watch", "err", touchErr)
			}
		}
	}
	if saveErr := e.store.SaveOrder(ctx, *order); saveErr != nil {
		e.log.Warn("save order", "err", saveErr)
	}
	e.bus.Publish("order", *order)
	event, message := orderNotice(site, *order)
	e.recordEvent(ctx, event, message)
}

// retryable 缺货、会话失效、验证取消 / 超时、配置错误、站点限流等不重试（限流时重试只会延长封禁）。
func retryable(err error) bool {
	var rateLimit *fetch.RateLimitError
	if errors.As(err, &rateLimit) {
		return false
	}
	for _, permanent := range []error{provider.ErrOutOfStock, provider.ErrOrderRejected, provider.ErrSessionExpired, provider.ErrNotSupported, provider.ErrCouponInvalid, provider.ErrLoginFailed,
		ErrNoAuthenticator, ErrNoCredentials, errAutoLogin, solver.ErrChallengeCancelled, context.DeadlineExceeded, context.Canceled} {
		if errors.Is(err, permanent) {
			return false
		}
	}
	return true
}

// OrderWatch 手动锁单 / 试下单；force 跳过 24 小时防重。
func (e *Engine) OrderWatch(ctx context.Context, watchID string, dryRun, force bool) (domain.Order, error) {
	site, watch, product, err := e.loadWatch(ctx, watchID)
	if err != nil {
		return domain.Order{}, err
	}
	if watch.AccountID == "" {
		return domain.Order{}, ErrNoAccount
	}
	return e.orderWatch(ctx, site, watch, product, dryRun, false, force)
}

// ProductOrderInput 不经关注规则直接下单：账号 + 下单参数（ProductID 以路径为准），未填项回落账号默认值。
type ProductOrderInput struct {
	AccountID string `json:"account_id"`
	// Force 跳过 24 小时防重。
	Force bool `json:"force"`
	domain.OrderSpec
}

// OrderProduct 直接对某商品锁单 / 试下单；同一站点 + 商品 + 账号不并发，真实下单做 24 小时防重（Force 除外）。
func (e *Engine) OrderProduct(ctx context.Context, siteID, productID string, input ProductOrderInput) (domain.Order, error) {
	site, err := e.store.GetSite(ctx, siteID)
	if err != nil {
		return domain.Order{}, err
	}
	product, err := e.store.GetProduct(ctx, siteID, productID)
	if err != nil {
		return domain.Order{}, err
	}
	if input.AccountID == "" {
		return domain.Order{}, invalidf("account_id is required")
	}
	account, err := e.store.GetAccount(ctx, input.AccountID)
	if err != nil {
		return domain.Order{}, tagged{err: fmt.Errorf("account %s: %w", input.AccountID, err), tag: ErrInvalid}
	}
	if account.SiteID != site.ID {
		return domain.Order{}, invalidf("account belongs to another site")
	}
	spec := input.OrderSpec
	spec.ProductID, spec.Cycle, spec.Qty = productID, cmp.Or(spec.Cycle, domain.CycleMonthly), max(spec.Qty, 1)
	if _, ok := product.PriceFor(spec.Cycle); !ok && len(product.Prices) > 0 {
		return domain.Order{}, invalidf("cycle %s is not offered for this product", spec.Cycle)
	}
	order := domain.Order{AccountID: account.ID, ProductID: productID, ProductName: product.Name, Spec: spec}
	return e.executeOrder(ctx, site, "product:"+site.ID+"/"+productID+"/"+account.ID, order, false, !input.Force)
}

// QuoteWatch 算价（校验优惠码）。
func (e *Engine) QuoteWatch(ctx context.Context, watchID string) (domain.Quote, error) {
	ctx, cancel := context.WithTimeout(ctx, e.interactiveTimeout())
	defer cancel()
	site, watch, _, err := e.loadWatch(ctx, watchID)
	if err != nil {
		return domain.Quote{}, err
	}
	if watch.AccountID == "" {
		return domain.Quote{}, ErrNoAccount
	}
	c, err := e.prepare(ctx, site, watch.AccountID)
	if err != nil {
		return domain.Quote{}, err
	}
	orderer, ok := c.inst.(provider.Orderer)
	if !ok {
		return domain.Quote{}, ErrNoOrderer
	}
	var quote domain.Quote
	err = e.run(ctx, c, true, func() error {
		var quoteErr error
		quote, quoteErr = orderer.Quote(ctx, c.env, withAccountDefaults(watchSpec(watch, true), *c.account))
		return quoteErr
	})
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
