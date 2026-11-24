package engine

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/notify"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/store"
)

func orderNotices(capture *captureNotifier) []string {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	var titles []string
	for _, message := range capture.messages {
		if message.Event == string(domain.EventOrder) {
			titles = append(titles, message.Title)
		}
	}
	return titles
}

func newOrderEngine(t *testing.T) (*Engine, *store.Store, *captureNotifier) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	engine, _ := New(Options{Store: st})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Stop)
	capture := &captureNotifier{}
	engine.Hub().SetNotifiers([]notify.Notifier{capture}, nil)
	return engine, st, capture
}

func TestOrderSetupFailureIsFinalized(t *testing.T) {
	ctx := context.Background()
	engine, st, capture := newOrderEngine(t)
	// fakelazy 不支持下单：准备阶段就失败。
	site, _ := engine.SaveSite(ctx, domain.Site{ID: "nz", Kind: "fakelazy", Name: "NoOrder", BaseURL: "https://lazy.test", Enabled: true, Config: map[string]string{}})
	account, _ := engine.SaveAccount(ctx, AccountInput{SiteID: site.ID, Label: "main"})
	watch, _ := engine.SaveWatch(ctx, domain.Watch{SiteID: site.ID, ProductID: "1", AccountID: account.ID, Enabled: true})
	order, err := engine.OrderWatch(ctx, watch.ID, false, true)
	if !errors.Is(err, ErrNoOrderer) || order.Status != domain.OrderFailed {
		t.Fatalf("want failed order with ErrNoOrderer, got %v %+v", err, order)
	}
	// 账号被绕过引擎删掉：加载失败同样落为 failed。
	_ = st.DeleteAccount(ctx, account.ID)
	if _, err := engine.OrderWatch(ctx, watch.ID, false, true); err == nil {
		t.Fatal("missing account must fail")
	}
	orders, _ := st.ListOrders(ctx, 10)
	if len(orders) != 2 {
		t.Fatalf("want 2 orders, got %d", len(orders))
	}
	for _, stored := range orders {
		if stored.Status != domain.OrderFailed || stored.Error == "" {
			t.Fatalf("order left unfinished: %+v", stored)
		}
	}
	waitFor(t, "failure notices", func() bool { return len(orderNotices(capture)) == 2 })
}

func TestOrderProductWithoutWatch(t *testing.T) {
	ctx := context.Background()
	engine, st, capture := newOrderEngine(t)
	fakeState.set(product("41", domain.StockIn, 500))
	site, _ := engine.SaveSite(ctx, domain.Site{ID: "fk4", Kind: "fake", Name: "Fake4", BaseURL: "https://fake.test", Enabled: true, Config: map[string]string{}})
	if _, err := engine.PollNow(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	account, _ := engine.SaveAccount(ctx, AccountInput{SiteID: site.ID, Label: "main", Defaults: domain.OrderDefaults{Gateway: "alipay", OS: "debian-12"}, Cookies: []*http.Cookie{{Name: "sid", Value: "s1"}}})
	other, _ := engine.SaveSite(ctx, domain.Site{ID: "fk5", Kind: "fake", Name: "Fake5", BaseURL: "https://fake.test", Config: map[string]string{}})
	foreign, _ := engine.SaveAccount(ctx, AccountInput{SiteID: other.ID, Label: "x"})
	for name, input := range map[string]ProductOrderInput{
		"no account":      {},
		"foreign account": {AccountID: foreign.ID},
		"unknown cycle":   {AccountID: account.ID, OrderSpec: domain.OrderSpec{Cycle: domain.CycleAnnually}},
	} {
		if _, err := engine.OrderProduct(ctx, site.ID, "41", input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: want ErrInvalid, got %v", name, err)
		}
	}
	order, err := engine.OrderProduct(ctx, site.ID, "41", ProductOrderInput{AccountID: account.ID, OrderSpec: domain.OrderSpec{Coupon: "SAVE", OS: "ubuntu", DryRun: true}})
	if err != nil || order.Status != domain.OrderDryRun || order.WatchID != "" {
		t.Fatalf("dry run failed: %v %+v", err, order)
	}
	if order.Spec.ProductID != "41" || order.Spec.Cycle != domain.CycleMonthly || order.Spec.Qty != 1 || order.Spec.Gateway != "alipay" || order.Spec.OS != "ubuntu" || order.Spec.Coupon != "SAVE" {
		t.Fatalf("spec not merged with account defaults: %+v", order.Spec)
	}
	order, err = engine.OrderProduct(ctx, site.ID, "41", ProductOrderInput{AccountID: account.ID})
	if err != nil || order.Status != domain.OrderPlaced || order.Result.InvoiceID != "200" {
		t.Fatalf("real order failed: %v %+v", err, order)
	}
	waitFor(t, "order notice", func() bool { return len(orderNotices(capture)) >= 1 })
	time.Sleep(100 * time.Millisecond)
	if titles := orderNotices(capture); len(titles) != 1 || !strings.Contains(titles[0], "锁单成功") {
		t.Fatalf("only the real order may notify, got %v", titles)
	}
	if orders, _ := st.ListOrders(ctx, 10); len(orders) != 2 {
		t.Fatalf("want 2 orders, got %d", len(orders))
	}

	// 24 小时防重：再次真实下单被拒（带上已有订单，不建记录）；试下单与 force 不受限。
	placed := order
	_, err = engine.OrderProduct(ctx, site.ID, "41", ProductOrderInput{AccountID: account.ID})
	var duplicate *DuplicateOrderError
	if !errors.As(err, &duplicate) || !errors.Is(err, ErrDuplicateOrder) || duplicate.Existing.ID != placed.ID {
		t.Fatalf("want duplicate rejection with existing order, got %v", err)
	}
	watch, _ := engine.SaveWatch(ctx, domain.Watch{SiteID: site.ID, ProductID: "41", AccountID: account.ID, Enabled: true})
	if _, err := engine.OrderWatch(ctx, watch.ID, false, false); !errors.Is(err, ErrDuplicateOrder) {
		t.Fatalf("manual watch order must share the duplicate guard, got %v", err)
	}
	if order, err := engine.OrderWatch(ctx, watch.ID, true, false); err != nil || order.Status != domain.OrderDryRun {
		t.Fatalf("dry run is exempt: %v %+v", err, order)
	}
	if order, err := engine.OrderProduct(ctx, site.ID, "41", ProductOrderInput{AccountID: account.ID, Force: true}); err != nil || order.Status != domain.OrderPlaced {
		t.Fatalf("force must bypass the guard: %v %+v", err, order)
	}
	if orders, _ := st.ListOrders(ctx, 10); len(orders) != 4 {
		t.Fatalf("rejected duplicates must not create records, got %d orders", len(orders))
	}
}

func TestHiddenProductsAreSilentAndNeverAutoOrdered(t *testing.T) {
	ctx := context.Background()
	engine, st, capture := newOrderEngine(t)
	engine.Hub().SetNotifiers([]notify.Notifier{capture}, nil)
	fakeState.mu.Lock()
	fakeState.orders = nil
	fakeState.mu.Unlock()
	fakeState.set(product("41", domain.StockOut, 500), product("42", domain.StockOut, 500))
	site, _ := engine.SaveSite(ctx, domain.Site{ID: "fk6", Kind: "fake", Name: "Fake6", BaseURL: "https://fake.test", Enabled: true, Config: map[string]string{}})
	if _, err := engine.PollNow(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	account, _ := engine.SaveAccount(ctx, AccountInput{SiteID: site.ID, Label: "main", Cookies: []*http.Cookie{{Name: "sid", Value: "s1"}}})
	if _, err := engine.SaveWatch(ctx, domain.Watch{SiteID: site.ID, ProductID: "41", AccountID: account.ID, AutoOrder: true, Notify: true, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.SetProductHidden(ctx, site.ID, "41", true); err != nil {
		t.Fatal(err)
	}
	// 普通保存站点不会清掉隐藏列表。
	if saved, err := engine.SaveSite(ctx, site); err != nil || !saved.Hidden("41") {
		t.Fatalf("SaveSite must preserve hidden products: %v %+v", err, saved)
	}
	fakeState.set(product("41", domain.StockIn, 500), product("42", domain.StockIn, 500), product("43", domain.StockIn, 500))
	if _, err := engine.PollNow(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if fakeState.orderCount() != 0 {
		t.Fatalf("hidden product must not be auto-ordered, got %d orders", fakeState.orderCount())
	}
	events, _ := st.ListEvents(ctx, site.ID, 50)
	seen := map[string]bool{}
	for _, event := range events {
		seen[event.ProductID] = true
	}
	if seen["41"] || !seen["42"] || !seen["43"] {
		t.Fatalf("hidden product events must be skipped, visible kept: %+v", events)
	}
	capture.mu.Lock()
	for _, message := range capture.messages {
		if strings.Contains(message.Title, "P41") {
			t.Errorf("hidden product notified: %+v", message)
		}
	}
	capture.mu.Unlock()
	// 取消隐藏：Diff 基于完整目录，不会补发积压的补货事件。
	if _, err := engine.SetProductHidden(ctx, site.ID, "41", false); err != nil {
		t.Fatal(err)
	}
	stockEvents := func() int {
		events, _ := st.ListEvents(ctx, site.ID, 50)
		count := 0
		for _, event := range events {
			if event.Type != domain.EventOrder {
				count++
			}
		}
		return count
	}
	before := stockEvents()
	if _, err := engine.PollNow(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	if after := stockEvents(); after != before {
		t.Fatalf("unhide must not produce a burst: %d → %d events", before, after)
	}
	waitFor(t, "auto order after unhide", func() bool { return fakeState.orderCount() == 1 })
}

func TestDryRunNeverNotifies(t *testing.T) {
	ctx := context.Background()
	engine, st, capture := newOrderEngine(t)
	fakeState.set(product("41", domain.StockIn, 500))
	fakeState.mu.Lock()
	fakeState.orderErr = provider.ErrOutOfStock
	fakeState.mu.Unlock()
	defer func() {
		fakeState.mu.Lock()
		fakeState.orderErr = nil
		fakeState.mu.Unlock()
	}()
	site, _ := engine.SaveSite(ctx, domain.Site{ID: "fk3", Kind: "fake", Name: "Fake3", BaseURL: "https://fake.test", Enabled: true, Config: map[string]string{}})
	account, _ := engine.SaveAccount(ctx, AccountInput{SiteID: site.ID, Label: "main", Cookies: []*http.Cookie{{Name: "sid", Value: "s1"}}})
	watch, _ := engine.SaveWatch(ctx, domain.Watch{SiteID: site.ID, ProductID: "41", AccountID: account.ID, Enabled: true})
	if order, _ := engine.OrderWatch(ctx, watch.ID, true, true); order.Status != domain.OrderFailed {
		t.Fatalf("dry run should fail: %+v", order)
	}
	events, _ := st.ListEvents(ctx, site.ID, 10)
	if len(events) == 0 || !strings.Contains(events[0].Title, "试下单失败") {
		t.Fatalf("dry-run failure event must say 试下单失败: %+v", events)
	}
	// 手动真实下单失败照常通知。
	_, _ = engine.OrderWatch(ctx, watch.ID, false, true)
	waitFor(t, "real order notice", func() bool { return len(orderNotices(capture)) >= 1 })
	time.Sleep(100 * time.Millisecond)
	if titles := orderNotices(capture); len(titles) != 1 || !strings.Contains(titles[0], "锁单失败") {
		t.Fatalf("only the real order may notify, got %v", titles)
	}
}
