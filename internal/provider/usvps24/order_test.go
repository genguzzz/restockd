package usvps24

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/solver"
)

// fakeSite 模拟 usvps24 的 Inertia 接口。
type fakeSite struct {
	t            *testing.T
	mu           sync.Mutex
	page         map[string]any
	version      string
	checkoutBody map[string]any
	checkoutHdr  http.Header
	rejectWith   string
	jsonHits     int
	htmlHits     int
	stockHits    int
}

func newFakeSite(t *testing.T, loggedIn bool) *fakeSite {
	raw, err := os.ReadFile("../../../testdata/usvps24/orders_create.json")
	if err != nil {
		t.Fatal(err)
	}
	var page map[string]any
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	props := page["props"].(map[string]any)
	props["isGuest"] = !loggedIn
	props["balanceCents"] = 1234
	if loggedIn {
		props["auth"] = map[string]any{"user": map[string]any{"email": "me@example.com", "name": "me"}}
	}
	return &fakeSite{t: t, page: page, version: page["version"].(string)}
}

func (f *fakeSite) writePage(w http.ResponseWriter, r *http.Request, page map[string]any) {
	data, _ := json.Marshal(page)
	if r.Header.Get("X-Inertia") == "true" {
		w.Header().Set("X-Inertia", "true")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
		f.jsonHits++
		return
	}
	f.htmlHits++
	fmt.Fprintf(w, `<html><body><div id="app" data-page="%s"></div></body></html>`, html.EscapeString(string(data)))
}

func (f *fakeSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "XSRF-TOKEN", Value: "abc%3D%3D", Path: "/"})
	if r.Header.Get("X-Inertia") == "true" && r.Header.Get("X-Inertia-Version") != f.version {
		w.Header().Set("X-Inertia-Location", r.URL.String())
		w.WriteHeader(http.StatusConflict)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/stock":
		raw, _ := os.ReadFile("../../../testdata/usvps24/stock.json")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
		f.stockHits++
	case r.Method == http.MethodGet && r.URL.Path == "/orders/create":
		f.writePage(w, r, f.page)
	case r.Method == http.MethodPost && r.URL.Path == "/orders/coupon/preview":
		if r.Header.Get("X-XSRF-TOKEN") != "abc==" {
			w.WriteHeader(419)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if body["coupon_code"] != "bf2026" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"优惠码无效","errors":{"coupon_code":["优惠码无效"]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":"bf2026","discount_cents":3760}`))
	case r.Method == http.MethodPost && r.URL.Path == "/cart/checkout":
		f.checkoutHdr = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&f.checkoutBody)
		if f.rejectWith != "" {
			http.Redirect(w, r, "/cart", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/invoices/777", http.StatusFound)
	case r.Method == http.MethodGet && r.URL.Path == "/invoices/777":
		f.writePage(w, r, map[string]any{"component": "Invoices/Show", "url": "/invoices/777", "version": f.version, "props": map[string]any{"errors": map[string]any{}}})
	case r.Method == http.MethodGet && r.URL.Path == "/cart":
		f.writePage(w, r, map[string]any{"component": "Cart", "url": "/cart", "version": f.version, "props": map[string]any{"errors": map[string]any{"items.0": f.rejectWith}}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type tokenSolver struct{ challenge solver.Challenge }

func (s *tokenSolver) Name() string              { return "test" }
func (s *tokenSolver) Supports(solver.Kind) bool { return true }
func (s *tokenSolver) Solve(_ context.Context, challenge solver.Challenge) (solver.Solution, error) {
	s.challenge = challenge
	return solver.Solution{Token: "TURNSTILE-OK"}, nil
}

func newEnv(t *testing.T, fake *fakeSite, slv solver.Solver) (*Provider, provider.Env) {
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	site := domain.Site{ID: "usvps24", Name: "usvps24", Kind: Kind, BaseURL: server.URL}
	instance, _ := New(site)
	client, _ := fetch.NewDirectClient(5*time.Second, "")
	session, _ := fetch.NewSession(server.URL, nil, "")
	return instance.(*Provider), provider.Env{Site: site, Client: client, Session: session, Solver: slv, Account: &domain.Account{ID: "a1"}}
}

func TestLoadCheckoutUsesInertiaJSONAfterFirstLoad(t *testing.T) {
	fake := newFakeSite(t, false)
	p, env := newEnv(t, fake, nil)
	for i := 0; i < 3; i++ {
		props, err := p.loadCheckout(context.Background(), env)
		if err != nil {
			t.Fatal(err)
		}
		if len(props.Products) != 11 {
			t.Fatalf("want 11 products, got %d", len(props.Products))
		}
	}
	if fake.htmlHits != 1 || fake.jsonHits != 2 {
		t.Fatalf("want 1 html + 2 json loads, got html=%d json=%d", fake.htmlHits, fake.jsonHits)
	}
}

func TestLoadCheckoutRecoversFromVersionChange(t *testing.T) {
	fake := newFakeSite(t, false)
	p, env := newEnv(t, fake, nil)
	if _, err := p.loadCheckout(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	fake.version = "new-build"
	fake.page["version"] = "new-build"
	if _, err := p.loadCheckout(context.Background(), env); err != nil {
		t.Fatalf("409 should trigger html reload: %v", err)
	}
	if p.getVersion() != "new-build" {
		t.Fatalf("version not refreshed: %s", p.getVersion())
	}
}

// 监控只能打公开库存接口，绝不能碰下单页（站点会封 IP 6 小时）。
func TestCatalogOnlyHitsStockAPI(t *testing.T) {
	fake := newFakeSite(t, false)
	p, env := newEnv(t, fake, nil)
	for i := 0; i < 3; i++ {
		catalog, err := p.Catalog(context.Background(), env)
		if err != nil {
			t.Fatal(err)
		}
		if len(catalog.Products) != 11 {
			t.Fatalf("want 11 products, got %d", len(catalog.Products))
		}
	}
	if fake.stockHits != 3 || fake.htmlHits != 0 || fake.jsonHits != 0 {
		t.Fatalf("catalog must only use stock api: stock=%d html=%d json=%d", fake.stockHits, fake.htmlHits, fake.jsonHits)
	}
}

func TestCatalogEnrichedAfterOrderPageLoad(t *testing.T) {
	fake := newFakeSite(t, false)
	p, env := newEnv(t, fake, nil)
	if _, err := p.loadCheckout(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	catalog, err := p.Catalog(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	var pinnacle domain.Product
	for _, product := range catalog.Products {
		if product.ID == "pinnacle-network-2" {
			pinnacle = product
		}
	}
	if pinnacle.Specs.Extra["product_id"] != "21" || len(pinnacle.Options) != 2 {
		t.Fatalf("expected enrichment from order page, got %+v", pinnacle)
	}
	if len(catalog.Gateways) != 2 || catalog.Promo == nil {
		t.Fatalf("gateways/promo should come from cached order page: %+v", catalog)
	}
}

func TestPlaceOrderSubmitsCheckout(t *testing.T) {
	fake := newFakeSite(t, true)
	slv := &tokenSolver{}
	p, env := newEnv(t, fake, slv)
	spec := domain.OrderSpec{ProductID: "pinnacle-network-2", Cycle: domain.CycleMonthly, Coupon: "bf2026", Gateway: "qianjimiao", OS: "debian-12", Qty: 1, Options: map[string]string{"bandwidth_mbps": "200", "ip_count": "1"}}
	result, err := p.PlaceOrder(context.Background(), env, spec)
	if err != nil {
		t.Fatal(err)
	}
	if result.InvoiceID != "777" || !strings.HasSuffix(result.PayURL, "/invoices/777") {
		t.Fatalf("unexpected result %+v", result)
	}
	if result.Total != (domain.Money{Cents: 18800 - 3760, Currency: "CNY"}) {
		t.Fatalf("unexpected total %+v", result.Total)
	}
	if slv.challenge.Kind != solver.KindTurnstile || slv.challenge.SiteKey == "" || !strings.HasSuffix(slv.challenge.PageURL, "/cart") {
		t.Fatalf("unexpected challenge %+v", slv.challenge)
	}
	body := fake.checkoutBody
	if body["coupon_code"] != "bf2026" || body["gateway"] != "qianjimiao" || body["cf_turnstile_response"] != "TURNSTILE-OK" || body["purchase_terms_ack"] != true {
		t.Fatalf("unexpected checkout body %v", body)
	}
	items := body["items"].([]any)
	item := items[0].(map[string]any)
	if item["product_id"].(float64) != 21 || item["period"] != "monthly" || item["os"] != "debian-12" || item["qty"].(float64) != 1 {
		t.Fatalf("unexpected item %v", item)
	}
	if specs := item["specs"].(map[string]any); specs["bandwidth_mbps"].(float64) != 200 {
		t.Fatalf("option values must be numeric: %v", specs)
	}
	if fake.checkoutHdr.Get("X-XSRF-TOKEN") != "abc==" || fake.checkoutHdr.Get("X-Inertia") != "true" || fake.checkoutHdr.Get("X-Inertia-Version") == "" {
		t.Fatalf("missing headers %v", fake.checkoutHdr)
	}
}

func TestPlaceOrderRejectedOutOfStock(t *testing.T) {
	fake := newFakeSite(t, true)
	fake.rejectWith = "库存不足"
	p, env := newEnv(t, fake, &tokenSolver{})
	_, err := p.PlaceOrder(context.Background(), env, domain.OrderSpec{ProductID: "21", Cycle: domain.CycleMonthly})
	if !errors.Is(err, provider.ErrOutOfStock) {
		t.Fatalf("want ErrOutOfStock, got %v", err)
	}
}

func TestPlaceOrderGuards(t *testing.T) {
	guest := newFakeSite(t, false)
	p, env := newEnv(t, guest, &tokenSolver{})
	if _, err := p.PlaceOrder(context.Background(), env, domain.OrderSpec{ProductID: "21", Cycle: domain.CycleMonthly}); !errors.Is(err, provider.ErrSessionExpired) {
		t.Fatalf("guest must get ErrSessionExpired, got %v", err)
	}
	fake := newFakeSite(t, true)
	p, env = newEnv(t, fake, &tokenSolver{})
	if _, err := p.PlaceOrder(context.Background(), env, domain.OrderSpec{ProductID: "10", Cycle: domain.CycleMonthly}); !errors.Is(err, provider.ErrOutOfStock) {
		t.Fatalf("sold out product must fail fast, got %v", err)
	}
	if _, err := p.PlaceOrder(context.Background(), env, domain.OrderSpec{ProductID: "21", Cycle: domain.CycleMonthly, Coupon: "WRONG"}); !errors.Is(err, provider.ErrCouponInvalid) || !strings.Contains(err.Error(), "优惠码无效") {
		t.Fatalf("invalid coupon must abort, got %v", err)
	}
	if fake.checkoutBody != nil {
		t.Fatal("checkout must not be submitted when guards fail")
	}
	p, env = newEnv(t, fake, nil)
	if _, err := p.PlaceOrder(context.Background(), env, domain.OrderSpec{ProductID: "21", Cycle: domain.CycleMonthly}); err == nil || !strings.Contains(err.Error(), "no solver") {
		t.Fatalf("want no solver error, got %v", err)
	}
}

func TestDryRunAndSessionCheck(t *testing.T) {
	fake := newFakeSite(t, true)
	p, env := newEnv(t, fake, &tokenSolver{})
	result, err := p.PlaceOrder(context.Background(), env, domain.OrderSpec{ProductID: "21", Cycle: domain.CycleMonthly, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if fake.checkoutBody != nil || !strings.Contains(result.Message, "dry run") {
		t.Fatalf("dry run submitted or wrong message: %+v", result)
	}
	info, err := p.CheckSession(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if info.Identity != "me@example.com" || info.Balance.Cents != 1234 {
		t.Fatalf("unexpected info %+v", info)
	}
}
