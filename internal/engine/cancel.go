package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/provider"
)

func cancelLockKey(orderID string) string { return "cancel:" + orderID }

// CancelOrder 在站点取消一笔已锁单（未付款）的订单：成功后标记 cancelled、广播并记事件（手动操作，不推送）。
// 站点不允许客户取消时返回包装了 provider.ErrNotSupported 的错误，订单状态不变。
func (e *Engine) CancelOrder(ctx context.Context, orderID string) (domain.Order, error) {
	lock := e.orderLocks.Get(cancelLockKey(orderID))
	if !lock.TryLock() {
		return domain.Order{}, ErrOrderInFlight
	}
	defer lock.Unlock()
	order, err := e.store.GetOrder(ctx, orderID)
	if err != nil {
		return domain.Order{}, err
	}
	if order.Status != domain.OrderPlaced {
		return order, invalidf("only placed orders can be cancelled (status %s)", order.Status)
	}
	if order.AccountID == "" || (order.Result.InvoiceID == "" && order.Result.OrderID == "") {
		return order, invalidf("order %s has no account or site order / invoice id", order.ID)
	}
	site, err := e.store.GetSite(ctx, order.SiteID)
	if err != nil {
		return order, err
	}
	c, err := e.prepare(ctx, site, order.AccountID)
	if err != nil {
		return order, err
	}
	canceler, ok := c.inst.(provider.Canceler)
	if !ok {
		return order, fmt.Errorf("%w: %s does not support cancelling orders", provider.ErrNotSupported, site.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, e.interactiveTimeout())
	defer cancel()
	if err := e.run(ctx, c, true, func() error { return canceler.CancelOrder(ctx, c.env, order.Result) }); err != nil {
		return order, err
	}
	order.Status = domain.OrderCancelled
	order.CancelledAt = time.Now().UTC()
	if err := e.store.SaveOrder(ctx, order); err != nil {
		return order, err
	}
	e.bus.Publish("order", order)
	e.recordEvent(ctx, domain.Event{Type: domain.EventOrder, SiteID: site.ID, ProductID: order.ProductID, Title: fmt.Sprintf("%s 已取消订单: %s", site.Name, order.ProductName), Detail: "账单 " + order.Result.InvoiceID}, nil)
	return order, nil
}
