package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/notify"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/solver"
)

// 事件与通知的构造集中在这里：每个构造函数返回 (事件, 通知)，通知为 nil 表示只记事件不推送；
// recordEvent 是唯一出口。

const notifyTimeout = 30 * time.Second

// recordEvent 写库、广播，并按类型异步发送通知（随引擎停止而取消，Stop 等待其退出）。
func (e *Engine) recordEvent(ctx context.Context, event domain.Event, message *notify.Message) {
	if err := e.store.AddEvent(ctx, &event); err != nil {
		e.log.Warn("save event", "err", err)
	}
	e.bus.Publish("event", event)
	if message == nil {
		return
	}
	message.Event = string(event.Type)
	e.wg.Add(1)
	go func(message notify.Message) {
		defer e.wg.Done()
		notifyCtx, cancel := context.WithTimeout(e.rootCtx, notifyTimeout)
		defer cancel()
		for channel, err := range e.hub.Notify(notifyCtx, message) {
			if err != nil {
				e.log.Warn("notify failed", "channel", channel, "err", err)
			}
		}
	}(*message)
}

// onChallenge 人工验证挂起：推送界面并通知用户去处理。
func (e *Engine) onChallenge(pending solver.Pending) {
	e.bus.Publish("challenge", pending)
	event, message := challengeNotice(pending, e.publicURL)
	e.recordEvent(e.rootCtx, event, message)
}

// TestNotify 向所有启用通道发送测试消息，返回各通道结果：ok / 错误信息 / disabled / not configured。
func (e *Engine) TestNotify(ctx context.Context) map[string]string {
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	results := e.hub.Channels() // 各通道 disabled / not_configured / ok，未就绪的通道不会发送
	for channel, err := range e.hub.Notify(ctx, notify.Message{Title: "restockd 测试通知", Body: "如果你看到这条消息，说明通知通道配置正确。"}) {
		results[channel] = "ok"
		if err != nil {
			results[channel] = err.Error()
		}
	}
	return results
}

// stockNotice 目录变化事件：只有被关注商品的补货 / 价格变动才推送；新品上架对所有商品推送。
func stockNotice(site domain.Site, event domain.Event, watched map[string]bool, products map[string]domain.Product) (domain.Event, *notify.Message) {
	switch event.Type {
	case domain.EventRestock, domain.EventPriceChange:
		if !watched[event.ProductID] {
			return event, nil
		}
	case domain.EventNewProduct:
	default:
		return event, nil
	}
	return event, &notify.Message{Title: "[" + site.Name + "] " + event.Title, Body: event.Detail, URL: products[event.ProductID].URL}
}

// pollErrorNotice 轮询失败只记事件（站点状态里也有），不推送。
func pollErrorNotice(site domain.Site, err error) (domain.Event, *notify.Message) {
	detail := err.Error()
	if fetch.IsChallenge(err) {
		detail += "；请在站点设置中启用 FlareSolverr，或给监控账号粘贴同一 IP/UA 获取的 cf_clearance Cookie"
	}
	return domain.Event{Type: domain.EventPollError, SiteID: site.ID, Title: site.Name + " 轮询失败", Detail: detail}, nil
}

// orderNotice 锁单结果；试下单无论成败都只记事件不推送。
func orderNotice(site domain.Site, order domain.Order) (domain.Event, *notify.Message) {
	kind := "锁单"
	if order.Spec.DryRun {
		kind = "试下单"
	}
	event := domain.Event{Type: domain.EventOrder, SiteID: site.ID, ProductID: order.ProductID}
	switch order.Status {
	case domain.OrderFailed:
		event.Title = fmt.Sprintf("%s %s失败: %s", site.Name, kind, order.ProductName)
		event.Detail = order.Error
	case domain.OrderDryRun:
		event.Title = fmt.Sprintf("%s 试下单通过: %s", site.Name, order.ProductName)
		event.Detail = order.Result.Message
	default:
		event.Title = fmt.Sprintf("%s 锁单成功: %s", site.Name, order.ProductName)
		event.Detail = order.Result.PayURL
	}
	if order.Spec.DryRun {
		return event, nil
	}
	body := order.Error
	if order.Status != domain.OrderFailed {
		body = fmt.Sprintf("周期 %s · 数量 %d · 优惠码 %s\n应付 %s\n订单 %s · 账单 %s，请尽快支付", order.Spec.Cycle, order.Spec.Qty, cmp.Or(order.Spec.Coupon, "无"), order.Result.Total,
			cmp.Or(order.Result.OrderID, "-"), cmp.Or(order.Result.InvoiceID, "-"))
	}
	return event, &notify.Message{Title: event.Title, Body: body, URL: order.Result.PayURL}
}

// sessionExpiredNotice 会话失效且无法自动恢复。
func sessionExpiredNotice(account domain.Account) (domain.Event, *notify.Message) {
	title := "账号会话失效: " + account.Label
	return domain.Event{Type: domain.EventAccount, SiteID: account.SiteID, Title: title, Detail: "请重新粘贴 Cookie，或给账号设置用户名密码以自动登录"},
		&notify.Message{Title: title, Body: "站点 " + account.SiteID + " 需要重新粘贴 Cookie，或设置用户名密码自动登录"}
}

// loginFailedNotice 自动登录失败；站点拒绝密码时说明已暂停自动登录。
func loginFailedNotice(account domain.Account, err error) (domain.Event, *notify.Message) {
	title := "账号自动登录失败: " + account.Label
	body := "站点 " + account.SiteID + "：" + err.Error()
	if errors.Is(err, provider.ErrLoginFailed) {
		body += "\n站点拒绝了当前密码，已暂停自动登录；修改密码或手动点「登录」后恢复"
	}
	return domain.Event{Type: domain.EventAccount, SiteID: account.SiteID, Title: title, Detail: err.Error()}, &notify.Message{Title: title, Body: body}
}

// challengeNotice 人工验证挂起，链接指向收件箱（配置了 PublicURL 时）。
func challengeNotice(pending solver.Pending, publicURL string) (domain.Event, *notify.Message) {
	link := ""
	if publicURL != "" {
		link = publicURL + "/challenges"
	}
	challenge := pending.Challenge
	return domain.Event{Type: domain.EventChallenge, SiteID: challenge.SiteID, Title: "需要人工验证: " + challenge.Purpose, Detail: fmt.Sprintf("%s，%s 前处理", challenge.Kind, pending.ExpiresAt.Local().Format("15:04:05"))},
		&notify.Message{Title: "需要人工验证 " + string(challenge.Kind), Body: challenge.Purpose + "\n请在过期前打开收件箱粘贴 token / Cookie", URL: link}
}
