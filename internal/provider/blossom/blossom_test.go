package blossom

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
)

const catalogJSON = `{"plans":[
 {"key":"isp-texas-xxs","name":"ISP-XXS","family":"isp","offer_label":"ISP — Odessa, TX · NetWest · 1 Gbps","location":"Odessa, TX","carrier":"NetWest","ip_label":"NetWest ISP IPv4","storage_label":"NVMe","vcpu":1,"ram_gb":1,"disk_gb":15,"hdd_gb":0,"bandwidth_gbps":1,"transfer_gb":1000,"coming_soon":false,"physical":false,"stock_count":39,"in_stock":true,"availability_status":"available","term_prices":{"1":17.0,"3":51.0,"6":102.0,"12":204.0},"os_images":[{"key":"debian-12","name":"Debian 12","kind":"linux"},{"key":"rocky-9","name":"Rocky Linux 9","kind":"linux"}]},
 {"key":"isp-xs","name":"ISP-XS","family":"isp","offer_label":"ISP — Washington, DC","location":"Washington, DC","vcpu":2,"ram_gb":2,"disk_gb":20,"bandwidth_gbps":1,"stock_count":0,"in_stock":false,"availability_status":"sold_out","term_prices":{"1":19.0},"os_images":[]},
 {"key":"isp-att-xs","name":"ISP-XS","family":"isp","location":"Los Angeles, CA","stock_count":0,"in_stock":false,"availability_status":"preorder_closed","term_prices":{"1":19.0}},
 {"key":"metal-427","name":"AMD Epyc 4545P","family":"metal","location":"Amsterdam","physical":true,"stock_count":1,"in_stock":true,"availability_status":"available","term_prices":{"1":199.0,"3":597.0}}
],"payment_methods":[{"key":"stripe","status":"live"},{"key":"alipay","status":"live"},{"key":"crypto","status":"unavailable"}]}`

// fakeSite 模拟 blossomhost.us 的 JSON 接口；checkout 的返回由 checkout 字段决定。
type fakeSite struct {
	t         *testing.T
	checkout  func(w http.ResponseWriter, payload orderPayload)
	lastQuote orderPayload
	cancelled string
	checkouts int
}

func (f *fakeSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Blossom") != "1" {
		http.Error(w, `{"error":"missing header"}`, http.StatusBadRequest)
		return
	}
	authed := func() bool {
		cookie, err := r.Cookie("bh_session")
		return err == nil && cookie.Value == "ok"
	}
	decode := func() orderPayload {
		var payload orderPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			f.t.Fatalf("decode payload: %v", err)
		}
		return payload
	}
	switch r.URL.Path {
	case "/api/catalog":
		_, _ = w.Write([]byte(catalogJSON))
	case "/auth/email/login":
		var creds map[string]string
		_ = json.NewDecoder(r.Body).Decode(&creds)
		if creds["email"] != "me@example.com" || creds["password"] != "right" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"Email or password is incorrect."}`))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "bh_session", Value: "ok", Path: "/"})
		_, _ = w.Write([]byte(`{"ok":true,"next":"#/dashboard"}`))
	case "/api/me":
		if !authed() {
			_, _ = w.Write([]byte(`{"platform":null,"platform_id":null}`))
			return
		}
		_, _ = w.Write([]byte(`{"platform":"email","platform_id":"7","email":"me@example.com"}`))
	case "/api/quote":
		if !authed() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.lastQuote = decode()
		coupon := "null"
		if f.lastQuote.Coupon != nil && *f.lastQuote.Coupon == "GOOD" {
			coupon = `"GOOD"`
		}
		_, _ = w.Write([]byte(`{"total":17.0,"term_subtotal":17.0,"coupon_code":` + coupon + `,"coupon_discount":0.0,"months":1}`))
	case "/api/checkout":
		f.checkouts++
		f.checkout(w, decode())
	case "/api/orders/501/cancel":
		f.cancelled = "501"
		_, _ = w.Write([]byte(`{"ok":true,"refunded":false}`))
	default:
		http.NotFound(w, r)
	}
}

func newEnv(t *testing.T, fake *fakeSite) (*Provider, provider.Env) {
	t.Helper()
	fake.t = t
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	site := domain.Site{ID: "blossom", Kind: Kind, BaseURL: server.URL}
	instance, _ := New(site)
	client, _ := fetch.NewHTTPClient(5*time.Second, "")
	session, _ := fetch.NewSession(server.URL, nil, "")
	return instance.(*Provider), provider.Env{Site: site, Client: client, Session: session, Account: &domain.Account{ID: "a1"}}
}

func TestCatalog(t *testing.T) {
	p, env := newEnv(t, &fakeSite{})
	catalog, err := p.Catalog(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Products) != 4 || len(catalog.Gateways) != 2 {
		t.Fatalf("products=%d gateways=%v", len(catalog.Products), catalog.Gateways)
	}
	texas := catalog.Products[0]
	if texas.ID != "isp-texas-xxs" || texas.Name != "ISP-XXS · Odessa, TX" || texas.Stock.Status != domain.StockIn || *texas.Stock.Qty != 39 {
		t.Fatalf("texas: %+v", texas)
	}
	if texas.Specs.RAMMB != 1024 || texas.Specs.BandwidthMbps != 1000 || texas.Specs.TrafficGB != 1000 || len(texas.OSTemplates) != 2 {
		t.Fatalf("texas specs: %+v", texas.Specs)
	}
	if len(texas.Prices) != 4 || texas.Prices[0].Cycle != domain.CycleMonthly || texas.Prices[0].Amount.Cents != 1700 || texas.Prices[3].Cycle != domain.CycleAnnually {
		t.Fatalf("texas prices: %+v", texas.Prices)
	}
	for i, want := range []domain.StockStatus{domain.StockIn, domain.StockOut, domain.StockClosed, domain.StockIn} {
		if got := catalog.Products[i].Stock.Status; got != want {
			t.Errorf("product %s stock %s, want %s", catalog.Products[i].ID, got, want)
		}
	}
	if metal := catalog.Products[3]; len(metal.Prices) != 1 {
		t.Fatalf("bare metal is monthly only: %+v", metal.Prices)
	}
	p.families = map[string]bool{"metal": true}
	if catalog, _ = p.Catalog(context.Background(), env); len(catalog.Products) != 1 {
		t.Fatalf("family filter: %d", len(catalog.Products))
	}
}

func TestLoginAndSession(t *testing.T) {
	p, env := newEnv(t, &fakeSite{})
	ctx := context.Background()
	if _, err := p.CheckSession(ctx, env); !errors.Is(err, provider.ErrSessionExpired) {
		t.Fatalf("anonymous session: %v", err)
	}
	if _, err := p.Login(ctx, env, domain.Credentials{Username: "me@example.com", Password: "wrong"}); !errors.Is(err, provider.ErrLoginFailed) {
		t.Fatalf("wrong password: %v", err)
	}
	info, err := p.Login(ctx, env, domain.Credentials{Username: "me@example.com", Password: "right"})
	if err != nil || info.Identity != "me@example.com" {
		t.Fatalf("login: %v %+v", err, info)
	}
}

func TestPlaceOrder(t *testing.T) {
	fake := &fakeSite{checkout: func(w http.ResponseWriter, payload orderPayload) {
		_, _ = w.Write([]byte(`{"order_id":501,"checkout_url":"https://pay.example/501","provider":"alipay"}`))
	}}
	p, env := newEnv(t, fake)
	ctx := context.Background()
	spec := domain.OrderSpec{ProductID: "isp-texas-xxs", Cycle: domain.CycleMonthly, Qty: 1}
	if _, err := p.PlaceOrder(ctx, env, spec); !errors.Is(err, provider.ErrSessionExpired) {
		t.Fatalf("quote without session must ask for login: %v", err)
	}
	if _, err := p.Login(ctx, env, domain.Credentials{Username: "me@example.com", Password: "right"}); err != nil {
		t.Fatal(err)
	}
	dry := spec
	dry.DryRun = true
	result, err := p.PlaceOrder(ctx, env, dry)
	if err != nil || result.Total.Cents != 1700 || fake.checkouts != 0 {
		t.Fatalf("dry run: %v %+v checkouts=%d", err, result, fake.checkouts)
	}
	if got := fake.lastQuote; got.OS != "debian-12" || got.TermMonths != 1 || got.Provider != "alipay" || !got.TermsAccepted || got.TermsVersion != legalVersion {
		t.Fatalf("payload: %+v", got)
	}
	result, err = p.PlaceOrder(ctx, env, spec)
	if err != nil || result.OrderID != "501" || result.PayURL != "https://pay.example/501" {
		t.Fatalf("order: %v %+v", err, result)
	}
	if err := p.CancelOrder(ctx, env, result); err != nil || fake.cancelled != "501" {
		t.Fatalf("cancel: %v %q", err, fake.cancelled)
	}
	// 无货 / 排队 / 风控拒绝 / 需要页面内刷卡。
	cases := []struct {
		reply string
		code  int
		want  error
	}{
		{`{"status":"availability"}`, 200, provider.ErrOutOfStock},
		{`{"mode":"elements","client_secret":"x"}`, 200, provider.ErrOrderRejected},
		{`{"error":"Checkout is unavailable right now."}`, 403, provider.ErrOrderRejected},
	}
	for _, tc := range cases {
		fake.checkout = func(w http.ResponseWriter, _ orderPayload) {
			w.WriteHeader(tc.code)
			_, _ = w.Write([]byte(tc.reply))
		}
		if _, err := p.PlaceOrder(ctx, env, spec); !errors.Is(err, tc.want) {
			t.Errorf("reply %s: got %v, want %v", tc.reply, err, tc.want)
		}
	}
	fake.checkout = func(w http.ResponseWriter, _ orderPayload) { _, _ = w.Write([]byte(`{"status":"queue"}`)) }
	if _, err := p.PlaceOrder(ctx, env, spec); err == nil || errors.Is(err, provider.ErrOrderRejected) {
		t.Errorf("queue must be a retryable error: %v", err)
	}
	if _, err := p.PlaceOrder(ctx, env, domain.OrderSpec{ProductID: "isp-xs", Cycle: domain.CycleMonthly}); !errors.Is(err, provider.ErrOutOfStock) {
		t.Errorf("sold out plan: %v", err)
	}
	if _, err := p.PlaceOrder(ctx, env, domain.OrderSpec{ProductID: "isp-texas-xxs", Cycle: domain.CycleMonthly, Coupon: "BAD", DryRun: true}); !errors.Is(err, provider.ErrCouponInvalid) {
		t.Errorf("bad coupon: %v", err)
	}
}
