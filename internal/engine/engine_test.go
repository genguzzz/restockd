package engine

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/notify"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/store"
)

func qty(n int) *int { return &n }

func product(id string, status domain.StockStatus, cents int64) domain.Product {
	return domain.Product{ID: id, Name: "P" + id, Stock: domain.Stock{Status: status, Qty: qty(2)}, Prices: []domain.Price{{Cycle: domain.CycleMonthly, Amount: domain.Money{Cents: cents, Currency: "USD"}}}}
}

func eventTypes(events []domain.Event) map[string]domain.EventType {
	out := map[string]domain.EventType{}
	for _, event := range events {
		out[event.ProductID+":"+string(event.Type)] = event.Type
	}
	return out
}

func TestDiff(t *testing.T) {
	if events := Diff("s", nil, []domain.Product{product("1", domain.StockIn, 100)}); len(events) != 0 {
		t.Fatalf("first poll must be silent, got %v", events)
	}
	prev := []domain.Product{product("1", domain.StockOut, 100), product("2", domain.StockIn, 100), product("3", domain.StockOut, 100)}
	next := []domain.Product{product("1", domain.StockIn, 100), product("2", domain.StockOut, 100), product("3", domain.StockOut, 120), product("4", domain.StockPreorder, 50)}
	got := eventTypes(Diff("s", prev, next))
	want := []string{"1:restock", "2:sold_out", "3:price_change", "4:new_product", "4:restock"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for _, key := range want {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing %s in %v", key, got)
		}
	}
}

func TestSortPrices(t *testing.T) {
	item := domain.Product{Prices: []domain.Price{{Cycle: domain.CycleAnnually}, {Cycle: "weird"}, {Cycle: domain.CycleMonthly}, {Cycle: domain.CycleQuarterly}}}
	item.SortPrices()
	got := []domain.BillingCycle{item.Prices[0].Cycle, item.Prices[1].Cycle, item.Prices[2].Cycle, item.Prices[3].Cycle}
	want := []domain.BillingCycle{domain.CycleMonthly, domain.CycleQuarterly, domain.CycleAnnually, "weird"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order %v", got)
		}
	}
}

func TestBackoffAndJitter(t *testing.T) {
	interval := 10 * time.Second
	if got := backoff(interval, 1, errors.New("x")); got != interval {
		t.Fatalf("first failure %s", got)
	}
	if got := backoff(interval, 3, errors.New("x")); got != 40*time.Second {
		t.Fatalf("third failure %s", got)
	}
	if got := backoff(interval, 30, errors.New("x")); got != maxBackoff {
		t.Fatalf("cap %s", got)
	}
	if got := backoff(interval, 1, &fetch.RateLimitError{RetryAfter: 2 * time.Minute}); got != 2*time.Minute {
		t.Fatalf("retry-after %s", got)
	}
	for i := 0; i < 200; i++ {
		if got := jitter(interval); got < 9*time.Second || got > 11*time.Second {
			t.Fatalf("jitter out of range %s", got)
		}
	}
}

func TestShouldAutoOrder(t *testing.T) {
	engine := &Engine{}
	in := product("1", domain.StockIn, 1000)
	base := domain.Watch{Enabled: true, AutoOrder: true, AccountID: "a", Cycle: domain.CycleMonthly, Qty: 1, MaxOrders: 1}
	cases := []struct {
		name   string
		mutate func(*domain.Watch, *domain.Product)
		want   bool
	}{
		{"eligible", func(*domain.Watch, *domain.Product) {}, true},
		{"disabled", func(w *domain.Watch, _ *domain.Product) { w.Enabled = false }, false},
		{"notify only", func(w *domain.Watch, _ *domain.Product) { w.AutoOrder = false }, false},
		{"no account", func(w *domain.Watch, _ *domain.Product) { w.AccountID = "" }, false},
		{"out of stock", func(_ *domain.Watch, p *domain.Product) { p.Stock.Status = domain.StockOut }, false},
		{"limit reached", func(w *domain.Watch, _ *domain.Product) { w.OrdersPlaced = 1 }, false},
		{"unlimited orders", func(w *domain.Watch, _ *domain.Product) { w.MaxOrders = 0; w.OrdersPlaced = 5 }, true},
		{"cooldown", func(w *domain.Watch, _ *domain.Product) { w.LastTriggered = time.Now() }, false},
		{"cooldown passed", func(w *domain.Watch, _ *domain.Product) { w.LastTriggered = time.Now().Add(-time.Hour) }, true},
		{"price over limit with qty", func(w *domain.Watch, _ *domain.Product) { w.MaxPriceCents = 1500; w.Qty = 2 }, false},
		{"price within limit", func(w *domain.Watch, _ *domain.Product) { w.MaxPriceCents = 1000 }, true},
		{"price limit but cycle missing", func(w *domain.Watch, _ *domain.Product) { w.MaxPriceCents = 5000; w.Cycle = domain.CycleAnnually }, false},
	}
	for _, tc := range cases {
		watch, item := base, in
		tc.mutate(&watch, &item)
		if got := engine.shouldAutoOrder(watch, item); got != tc.want {
			t.Errorf("%s: want %v got %v", tc.name, tc.want, got)
		}
	}
}

func TestBuildSpecFallsBackToAccountDefaults(t *testing.T) {
	watch := domain.Watch{ProductID: "1", Cycle: domain.CycleAnnually, Coupon: "C", OS: "", Qty: 0}
	account := domain.Account{Defaults: domain.OrderDefaults{Gateway: "alipay", OS: "debian-12", SSHKey: "ssh-ed25519 AAA"}}
	spec := BuildSpec(watch, account, true)
	if spec.Gateway != "alipay" || spec.OS != "debian-12" || spec.SSHKey != "ssh-ed25519 AAA" || spec.Qty != 1 || !spec.DryRun || spec.Coupon != "C" {
		t.Fatalf("unexpected spec %+v", spec)
	}
	watch.Gateway, watch.OS = "stripe", "ubuntu"
	spec = BuildSpec(watch, account, false)
	if spec.Gateway != "stripe" || spec.OS != "ubuntu" {
		t.Fatalf("watch values must win: %+v", spec)
	}
}

// ---- 端到端：假 Provider ----

type fakeProvider struct {
	mu       sync.Mutex
	products []domain.Product
	orders   []domain.OrderSpec
	orderErr error
}

var fakeState = &fakeProvider{}

func init() {
	provider.Register(provider.Meta{Kind: "fake", Name: "fake", MinInterval: time.Hour, DefaultInterval: time.Hour, Caps: provider.Capabilities{Catalog: true, Order: true}}, func(domain.Site) (provider.Provider, error) {
		return fakeState, nil
	})
}

func (f *fakeProvider) Meta() provider.Meta {
	meta, _ := provider.Lookup("fake")
	return meta
}

func (f *fakeProvider) Catalog(context.Context, provider.Env) (domain.Catalog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return domain.Catalog{Products: append([]domain.Product(nil), f.products...)}, nil
}

func (f *fakeProvider) Quote(context.Context, provider.Env, domain.OrderSpec) (domain.Quote, error) {
	return domain.Quote{}, nil
}

func (f *fakeProvider) PlaceOrder(_ context.Context, env provider.Env, spec domain.OrderSpec) (domain.OrderResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if env.Account == nil || env.Session.Cookie("sid") != "s1" {
		return domain.OrderResult{}, errors.New("account session not passed")
	}
	f.orders = append(f.orders, spec)
	if f.orderErr != nil {
		return domain.OrderResult{}, f.orderErr
	}
	return domain.OrderResult{OrderID: "100", InvoiceID: "200", PayURL: "https://pay/200", Total: domain.Money{Cents: 500, Currency: "USD"}}, nil
}

func (f *fakeProvider) set(products ...domain.Product) {
	f.mu.Lock()
	f.products = products
	f.mu.Unlock()
}

func (f *fakeProvider) orderCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.orders)
}

type captureNotifier struct {
	mu       sync.Mutex
	messages []notify.Message
}

func (c *captureNotifier) Name() string { return "capture" }
func (c *captureNotifier) Send(_ context.Context, message notify.Message) error {
	c.mu.Lock()
	c.messages = append(c.messages, message)
	c.mu.Unlock()
	return nil
}
func (c *captureNotifier) events() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]int{}
	for _, message := range c.messages {
		out[message.Event]++
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestEngineEndToEndAutoOrder(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	engine, err := New(Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer engine.Stop()
	capture := &captureNotifier{}
	engine.Hub().SetNotifiers([]notify.Notifier{capture}, []string{"restock", "order"})
	fakeState.set(product("41", domain.StockOut, 500), product("42", domain.StockOut, 900))
	site, err := engine.SaveSite(ctx, domain.Site{ID: "fk", Kind: "fake", Name: "Fake", BaseURL: "https://fake.test", Enabled: true, Config: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.PollNow(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	account, err := engine.SaveAccount(ctx, domain.Account{SiteID: site.ID, Label: "main", Defaults: domain.OrderDefaults{Gateway: "alipay"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.SetCookies(ctx, account.ID, []*http.Cookie{{Name: "sid", Value: "s1"}}, ""); err != nil {
		t.Fatal(err)
	}
	watch, err := engine.SaveWatch(ctx, domain.Watch{SiteID: site.ID, ProductID: "41", Cycle: domain.CycleMonthly, Coupon: "SAVE", AccountID: account.ID, AutoOrder: true, MaxOrders: 1, Notify: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if watch.ProductName != "P41" {
		t.Fatalf("product name not filled: %+v", watch)
	}
	if _, err := engine.SaveWatch(ctx, domain.Watch{SiteID: site.ID, ProductID: "42", Cycle: domain.CycleMonthly, AutoOrder: true, Enabled: true}); err == nil {
		t.Fatal("auto order without account must be rejected")
	}
	fakeState.set(product("41", domain.StockIn, 500), product("42", domain.StockIn, 900))
	if err := engine.PollNow(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "auto order", func() bool {
		orders, _ := st.ListOrders(ctx, 10)
		return len(orders) == 1 && orders[0].Status == domain.OrderPlaced
	})
	orders, _ := st.ListOrders(ctx, 10)
	if orders[0].Result.InvoiceID != "200" || orders[0].Spec.Gateway != "alipay" || orders[0].Spec.Coupon != "SAVE" {
		t.Fatalf("unexpected order %+v", orders[0])
	}
	stored, _ := st.GetWatch(ctx, watch.ID)
	if stored.OrdersPlaced != 1 || stored.LastTriggered.IsZero() {
		t.Fatalf("watch counters not updated: %+v", stored)
	}
	// 达到 max_orders 后再次轮询不再下单
	if err := engine.PollNow(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if fakeState.orderCount() != 1 {
		t.Fatalf("max_orders not respected: %d orders", fakeState.orderCount())
	}
	waitFor(t, "notifications", func() bool {
		events := capture.events()
		return events["restock"] == 1 && events["order"] == 1
	})
	events, _ := st.ListEvents(ctx, site.ID, 50)
	types := map[domain.EventType]int{}
	for _, event := range events {
		types[event.Type]++
	}
	if types[domain.EventRestock] != 2 || types[domain.EventOrder] != 1 {
		t.Fatalf("unexpected events %v", types)
	}
}

func TestEngineManualOrderFailureRecorded(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "test.db"))
	defer st.Close()
	engine, _ := New(Options{Store: st})
	_ = engine.Start(ctx)
	defer engine.Stop()
	engine.Hub().SetNotifiers(nil, nil)
	fakeState.set(product("41", domain.StockIn, 500))
	fakeState.mu.Lock()
	fakeState.orders = nil
	fakeState.orderErr = provider.ErrOutOfStock
	fakeState.mu.Unlock()
	defer func() {
		fakeState.mu.Lock()
		fakeState.orderErr = nil
		fakeState.mu.Unlock()
	}()
	site, _ := engine.SaveSite(ctx, domain.Site{ID: "fk2", Kind: "fake", Name: "Fake2", BaseURL: "https://fake.test", Enabled: true, Config: map[string]string{}})
	account, _ := engine.SaveAccount(ctx, domain.Account{SiteID: site.ID, Label: "main"})
	_, _ = engine.SetCookies(ctx, account.ID, []*http.Cookie{{Name: "sid", Value: "s1"}}, "")
	watch, _ := engine.SaveWatch(ctx, domain.Watch{SiteID: site.ID, ProductID: "41", AccountID: account.ID, Enabled: true})
	order, err := engine.OrderWatch(ctx, watch.ID, false)
	if !retryable(errors.New("transient")) || retryable(provider.ErrCouponInvalid) {
		t.Fatal("retry classification wrong")
	}
	if !errors.Is(err, provider.ErrOutOfStock) {
		t.Fatalf("want out of stock, got %v", err)
	}
	if order.Status != domain.OrderFailed || order.Error == "" {
		t.Fatalf("failure not recorded: %+v", order)
	}
	if fakeState.orderCount() != 1 {
		t.Fatalf("out of stock must not be retried, got %d attempts", fakeState.orderCount())
	}
}
