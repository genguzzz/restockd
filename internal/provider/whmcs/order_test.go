package whmcs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/provider/htmlutil"
	"github.com/genguzzz/restockd/internal/solver"
)

const fakeToken = "a1b2c3d4e5f60718293a4b5c6d7e8f90aabbccdd"

// fakeWHMCS 模拟 WHMCS 购物车流程的最小服务端。
type fakeWHMCS struct {
	mu          sync.Mutex
	inStock     bool
	captcha     bool
	cart        []url.Values
	promo       string
	silentPromo bool
	checkout    url.Values
	emptied     int
	checkoutHit int
}

func (f *fakeWHMCS) page(body string) string {
	return `<html><body><a href="/logout.php">Logout</a><input type="hidden" name="token" value="` + fakeToken + `"/>` + body + `</body></html>`
}

func (f *fakeWHMCS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = r.ParseForm()
	if r.URL.Path == "/store/cox-isp-vps" {
		fmt.Fprint(w, f.page(`<a class="dropdown-toggle"><span class="item-text">Alice</span></a>`))
		return
	}
	if r.URL.Path == "/clientarea.php" {
		fmt.Fprint(w, f.page(`<div class="client-credit">$5.00 CAD</div>`))
		return
	}
	if r.URL.Path != "/cart.php" {
		http.NotFound(w, r)
		return
	}
	action := r.URL.Query().Get("a")
	if r.Method == http.MethodPost && r.PostForm.Get("token") != fakeToken {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, f.page(`<div class="alert alert-danger">Invalid CSRF token</div>`))
		return
	}
	switch {
	case r.Method == http.MethodGet && action == "view":
		items := ""
		for range f.cart {
			items += `<div class="item-title">LAS-COX Lite</div>`
		}
		fmt.Fprint(w, f.page(items))
	case r.Method == http.MethodPost && action == "empty":
		f.cart = nil
		f.promo = ""
		f.emptied++
		http.Redirect(w, r, "/cart.php?a=view", http.StatusFound)
	case r.Method == http.MethodGet && action == "add":
		if !f.inStock {
			fmt.Fprint(w, f.page(`<div class="alert alert-danger">This product is currently Out of Stock</div>`))
			return
		}
		http.Redirect(w, r, "/cart.php?a=confproduct&i=0", http.StatusFound)
	case r.Method == http.MethodGet && action == "confproduct":
		fmt.Fprint(w, f.page(`<form id="frmConfigureProduct" method="post" action="/cart.php?a=confproduct">
<input type="hidden" name="token" value="`+fakeToken+`"/>
<input type="hidden" name="i" value="0"/>
<div class="alert alert-danger hidden" id="containerProductValidationErrors"><h6>Please correct the following errors before continuing:</h6><ul id="containerProductValidationErrorsList"></ul></div>
<select name="billingcycle"><option value="monthly" selected>Monthly</option><option value="annually">Annually</option></select>
<select name="configoption[5]"><option value="11" selected>Ubuntu 22.04</option><option value="12">Debian 12</option></select>
<input type="text" name="hostname" value=""/>
<input type="text" name="rootpw" value="N/A"/>
<input type="text" name="ns1prefix" value=""/><input type="text" name="ns2prefix" value=""/>
<input type="checkbox" name="addons[1]" value="on"/>
</form>`))
	case r.Method == http.MethodPost && action == "" && r.PostForm.Get("a") == "confproduct":
		if r.PostForm.Get("ajax") != "1" {
			// Lagom：非 AJAX 提交停留在配置页且不入车。
			http.Redirect(w, r, "/cart.php?a=confproduct&i=0", http.StatusFound)
			return
		}
		if pw := r.PostForm.Get("rootpw"); pw == "" || pw == "N/A" {
			fmt.Fprint(w, `<li>Root password is required</li>`)
			return
		}
		f.cart = append(f.cart, r.PostForm)
	case r.Method == http.MethodPost && action == "view":
		code := r.PostForm.Get("promocode")
		if f.silentPromo && code != "SAVE10" {
			// 部分主题拒绝优惠码时不显示错误，只是重新渲染带 promocode 输入框的购物车。
			fmt.Fprint(w, f.page(`<form><input type="text" name="promocode" placeholder="Enter promo code"/><button name="validatepromo">Validate</button></form>`))
			return
		}
		if code != "SAVE10" {
			fmt.Fprint(w, f.page(`<div class="alert alert-danger">The promotion code entered does not exist</div>`))
			return
		}
		f.promo = code
		fmt.Fprint(w, f.page(`<div class="promo">SAVE10 - 10% <a href="cart.php?a=removepromo">Remove</a></div>`))
	case r.Method == http.MethodGet && action == "checkout":
		if len(f.cart) == 0 {
			fmt.Fprint(w, f.page(`<p>Your cart is empty</p>`))
			return
		}
		captcha := ""
		if f.captcha {
			captcha = `<div class="g-recaptcha" data-sitekey="SITEKEY123"></div>`
		}
		total := "$19.99 CAD"
		if f.promo != "" {
			total = "$17.99 CAD"
		}
		fmt.Fprint(w, f.page(`<form id="frmCheckout" method="post" action="/cart.php?a=checkout">
<input type="hidden" name="token" value="`+fakeToken+`"/>
<input type="radio" name="paymentmethod" value="paypal" checked/><input type="radio" name="paymentmethod" value="alipay"/>
<input type="checkbox" name="accepttos"/>
<textarea name="notes"></textarea>`+captcha+`
<span id="subtotal">$19.99 CAD</span><span id="totalDueToday">`+total+`</span>
</form>`))
	case r.Method == http.MethodPost && action == "checkout":
		f.checkoutHit++
		f.checkout = r.PostForm
		if f.captcha && r.PostForm.Get("g-recaptcha-response") == "" {
			fmt.Fprint(w, f.page(`<div class="checkout-error-feedback">Please complete the captcha</div>`))
			return
		}
		if r.PostForm.Get("accepttos") != "on" {
			fmt.Fprint(w, f.page(`<div class="checkout-error-feedback">You must accept the TOS</div>`))
			return
		}
		http.Redirect(w, r, "/cart.php?a=complete", http.StatusFound)
	case r.Method == http.MethodGet && action == "complete":
		fmt.Fprint(w, f.page(`<p>Your Order Number is: <strong>9876</strong></p><a href="viewinvoice.php?id=555">Continue To Invoice</a>`))
	default:
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "unexpected %s %s", r.Method, r.URL)
	}
}

type recordingSolver struct {
	mu        sync.Mutex
	challenge solver.Challenge
	token     string
	calls     int
}

func (s *recordingSolver) Name() string              { return "test" }
func (s *recordingSolver) Supports(solver.Kind) bool { return true }
func (s *recordingSolver) Solve(_ context.Context, challenge solver.Challenge) (solver.Solution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.challenge = challenge
	s.calls++
	return solver.Solution{Token: s.token}, nil
}

func newOrderEnv(t *testing.T, fake *fakeWHMCS, slv solver.Solver) (*Provider, provider.Env) {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	site := domain.Site{ID: "nv", Name: "NovixLink", Kind: Kind, BaseURL: server.URL, Config: map[string]string{"groups": "cox-isp-vps", "currency": "CAD"}}
	instance, err := New(site)
	if err != nil {
		t.Fatal(err)
	}
	client, _ := fetch.NewHTTPClient(5*time.Second, "")
	session, _ := fetch.NewSession(server.URL, nil, "")
	return instance.(*Provider), provider.Env{Site: site, Client: client, Session: session, Solver: slv, Account: &domain.Account{ID: "acc1"}}
}

func TestPlaceOrderFullFlow(t *testing.T) {
	fake := &fakeWHMCS{inStock: true, cart: []url.Values{{"stale": {"1"}}}}
	p, env := newOrderEnv(t, fake, nil)
	spec := domain.OrderSpec{ProductID: "41", Cycle: domain.CycleMonthly, Coupon: "SAVE10", Gateway: "alipay", Options: map[string]string{"configoption[5]": "12"}, Hostname: "my.host"}
	result, err := p.PlaceOrder(context.Background(), env, spec)
	if err != nil {
		t.Fatal(err)
	}
	if result.OrderID != "9876" || result.InvoiceID != "555" || !strings.HasSuffix(result.PayURL, "/viewinvoice.php?id=555") {
		t.Fatalf("unexpected result %+v", result)
	}
	if result.Total != (domain.Money{Cents: 1799, Currency: "CAD"}) {
		t.Fatalf("discounted total not parsed: %+v", result.Total)
	}
	if fake.emptied != 1 {
		t.Fatalf("cart should be emptied once before ordering, got %d", fake.emptied)
	}
	if len(fake.cart) != 1 {
		t.Fatalf("want 1 configured item, got %d", len(fake.cart))
	}
	conf := fake.cart[0]
	if conf.Get("configoption[5]") != "12" || conf.Get("billingcycle") != "monthly" || conf.Get("hostname") != "my.host" || conf.Get("configure") != "true" {
		t.Fatalf("configure form wrong: %v", conf)
	}
	if conf.Get("rootpw") == "" || conf.Get("ns1prefix") != "ns1" {
		t.Fatalf("server fields not filled: %v", conf)
	}
	if _, present := conf["addons[1]"]; present {
		t.Fatal("unchecked checkbox must not be submitted")
	}
	checkout := fake.checkout
	if checkout.Get("paymentmethod") != "alipay" || checkout.Get("accepttos") != "on" || checkout.Get("custtype") != "existing" || checkout.Get("submit") != "true" {
		t.Fatalf("checkout form wrong: %v", checkout)
	}
}

func TestPlaceOrderDryRunDoesNotSubmit(t *testing.T) {
	fake := &fakeWHMCS{inStock: true}
	p, env := newOrderEnv(t, fake, nil)
	result, err := p.PlaceOrder(context.Background(), env, domain.OrderSpec{ProductID: "41", Cycle: domain.CycleMonthly, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if fake.checkoutHit != 0 {
		t.Fatal("dry run must not POST checkout")
	}
	if result.Total.Cents != 1999 || !strings.Contains(result.Message, "dry run") {
		t.Fatalf("unexpected dry run result %+v", result)
	}
}

func TestPlaceOrderOutOfStock(t *testing.T) {
	p, env := newOrderEnv(t, &fakeWHMCS{inStock: false}, nil)
	_, err := p.PlaceOrder(context.Background(), env, domain.OrderSpec{ProductID: "41", Cycle: domain.CycleMonthly})
	if !errors.Is(err, provider.ErrOutOfStock) {
		t.Fatalf("want ErrOutOfStock, got %v", err)
	}
}

func TestPlaceOrderCaptchaUsesSolver(t *testing.T) {
	fake := &fakeWHMCS{inStock: true, captcha: true}
	slv := &recordingSolver{token: "RECAPTCHA-TOKEN"}
	p, env := newOrderEnv(t, fake, slv)
	if _, err := p.PlaceOrder(context.Background(), env, domain.OrderSpec{ProductID: "41", Cycle: domain.CycleMonthly}); err != nil {
		t.Fatal(err)
	}
	if slv.challenge.Kind != solver.KindRecaptchaV2 || slv.challenge.SiteKey != "SITEKEY123" || slv.challenge.AccountID != "acc1" {
		t.Fatalf("unexpected challenge %+v", slv.challenge)
	}
	if fake.checkout.Get("g-recaptcha-response") != "RECAPTCHA-TOKEN" {
		t.Fatalf("token not submitted: %v", fake.checkout)
	}
	if fake.checkout.Get("paymentmethod") != "paypal" {
		t.Fatalf("default gateway should be first offered, got %q", fake.checkout.Get("paymentmethod"))
	}
}

func TestPlaceOrderCaptchaWithoutSolverFails(t *testing.T) {
	fake := &fakeWHMCS{inStock: true, captcha: true}
	p, env := newOrderEnv(t, fake, nil)
	if _, err := p.PlaceOrder(context.Background(), env, domain.OrderSpec{ProductID: "41", Cycle: domain.CycleMonthly}); err == nil || !strings.Contains(err.Error(), "no solver") {
		t.Fatalf("want no-solver error, got %v", err)
	}
	if fake.checkoutHit != 0 {
		t.Fatal("must not submit checkout without captcha token")
	}
}

func TestPlaceOrderAbortsOnRejectedPromo(t *testing.T) {
	fake := &fakeWHMCS{inStock: true}
	p, env := newOrderEnv(t, fake, nil)
	_, err := p.PlaceOrder(context.Background(), env, domain.OrderSpec{ProductID: "41", Cycle: domain.CycleMonthly, Coupon: "BOGUS"})
	if !errors.Is(err, provider.ErrCouponInvalid) {
		t.Fatalf("want ErrCouponInvalid, got %v", err)
	}
	if fake.checkoutHit != 0 {
		t.Fatal("must not checkout at full price when coupon rejected")
	}
}

func TestQuoteInvalidPromoAndUnknownGateway(t *testing.T) {
	fake := &fakeWHMCS{inStock: true}
	p, env := newOrderEnv(t, fake, nil)
	quote, err := p.Quote(context.Background(), env, domain.OrderSpec{ProductID: "41", Cycle: domain.CycleMonthly, Coupon: "BOGUS"})
	if err != nil {
		t.Fatal(err)
	}
	if quote.CouponValid || !strings.Contains(quote.Message, "does not exist") || quote.Total.Cents != 1999 {
		t.Fatalf("unexpected quote %+v", quote)
	}
	_, err = p.PlaceOrder(context.Background(), env, domain.OrderSpec{ProductID: "41", Cycle: domain.CycleMonthly, Gateway: "stripe"})
	if err == nil || !strings.Contains(err.Error(), "not offered") {
		t.Fatalf("want gateway error, got %v", err)
	}
}

func TestCheckSession(t *testing.T) {
	p, env := newOrderEnv(t, &fakeWHMCS{}, nil)
	info, err := p.CheckSession(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if info.Identity != "Alice" || info.Balance == nil || info.Balance.Cents != 500 {
		t.Fatalf("unexpected session info %+v", info)
	}
}

func TestCheckSessionIgnoresClientAreaLoginRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/store/g":
			fmt.Fprint(w, `<a href="/logout.php">Logout</a>`)
		case "/clientarea.php":
			http.Redirect(w, r, "/login", http.StatusFound)
		default:
			fmt.Fprint(w, `<form><input name="username"/><input type="password" name="password"/></form>`)
		}
	}))
	defer server.Close()
	instance, _ := New(domain.Site{BaseURL: server.URL, Config: map[string]string{"groups": "g"}})
	client, _ := fetch.NewHTTPClient(5*time.Second, "")
	session, _ := fetch.NewSession(server.URL, nil, "")
	if _, err := instance.(*Provider).CheckSession(context.Background(), provider.Env{Client: client, Session: session}); err != nil {
		t.Fatalf("store page shows logout, session must be valid: %v", err)
	}
}

func TestSessionExpiredOnLoginRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			fmt.Fprint(w, `<form><input name="username"/><input type="password" name="password"/></form>`)
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	defer server.Close()
	instance, _ := New(domain.Site{BaseURL: server.URL, Config: map[string]string{"groups": "x"}})
	client, _ := fetch.NewHTTPClient(5*time.Second, "")
	session, _ := fetch.NewSession(server.URL, nil, "")
	_, err := instance.(*Provider).CheckSession(context.Background(), provider.Env{Client: client, Session: session})
	if !errors.Is(err, provider.ErrSessionExpired) {
		t.Fatalf("want ErrSessionExpired, got %v", err)
	}
}

func TestAjaxErrorsAndHiddenTemplate(t *testing.T) {
	parse := func(body string) *page {
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return &page{resp: &fetch.Response{Body: []byte(body)}, doc: doc}
	}
	if got := ajaxErrors(parse("")); got != "" {
		t.Fatalf("empty ajax response must pass, got %q", got)
	}
	if got := ajaxErrors(parse(`<li>Hostname is invalid</li><li>Root password is required</li>`)); got != "Hostname is invalid; Root password is required" {
		t.Fatalf("unexpected ajax errors %q", got)
	}
	full := `<html><body><ul class="nav"><li>Home</li></ul><div class="alert alert-danger hidden"><h6>Please correct the following errors before continuing:</h6><ul></ul></div></body></html>`
	if got := ajaxErrors(parse(full)); got != "" {
		t.Fatalf("hidden template / nav items must not count as errors, got %q", got)
	}
	for _, cartURL := range []string{"https://shop.test/cart.php?a=view", "https://shop.test/cart.php?a=checkout", "https://shop.test/cart.php?a=confdomains"} {
		if !inCart(cartURL) {
			t.Fatalf("redirect to %s means the item was added", cartURL)
		}
	}
	if inCart("https://shop.test/cart.php?a=confproduct&i=0") {
		t.Fatal("configure page is not in cart")
	}
	visible := `<html><body><div class="alert alert-danger hidden">template</div><div class="alert alert-danger">Out of Stock</div></body></html>`
	if got := errorMessage(parse(visible).doc); got != "Out of Stock" {
		t.Fatalf("expected visible error, got %q", got)
	}
}

// 优惠码被静默拒绝（SixtyNet：跳过购物车、提示随 302 丢失）：结算页没确认也没折扣，锁单必须中止，不能原价成交。
func TestPlaceOrderAbortsOnSilentlyRejectedPromo(t *testing.T) {
	fake := &fakeWHMCS{inStock: true, silentPromo: true}
	p, env := newOrderEnv(t, fake, nil)
	_, err := p.PlaceOrder(context.Background(), env, domain.OrderSpec{ProductID: "41", Cycle: domain.CycleMonthly, Coupon: "BOGUS"})
	if !errors.Is(err, provider.ErrCouponInvalid) || fake.checkoutHit != 0 {
		t.Fatalf("silent promo rejection must abort before checkout: err=%v checkoutHit=%d", err, fake.checkoutHit)
	}
}

// 页面只是重新渲染了优惠码输入框（name="promocode"），不能当作优惠码已生效。
func TestQuoteSilentPromoRejectionIsNotValid(t *testing.T) {
	fake := &fakeWHMCS{inStock: true, silentPromo: true}
	p, env := newOrderEnv(t, fake, nil)
	quote, err := p.Quote(context.Background(), env, domain.OrderSpec{ProductID: "41", Cycle: domain.CycleMonthly, Coupon: "BOGUS"})
	if err != nil {
		t.Fatal(err)
	}
	if quote.CouponValid {
		t.Fatalf("unconfirmed promo must not be reported valid: %+v", quote)
	}
	quote, err = p.Quote(context.Background(), env, domain.OrderSpec{ProductID: "41", Cycle: domain.CycleMonthly, Coupon: "SAVE10"})
	if err != nil || !quote.CouponValid {
		t.Fatalf("removepromo link means accepted: %+v %v", quote, err)
	}
}

func TestPromoApplied(t *testing.T) {
	parse := func(body string) *goquery.Document {
		doc, _ := goquery.NewDocumentFromReader(strings.NewReader(body))
		return doc
	}
	if !promoApplied(parse(`<div class="view-cart-promotion-code">SAVE10 - 10% Recurring</div>`), "save10") {
		t.Fatal("promo code element must count as applied")
	}
	if promoApplied(parse(`<div class="view-cart-promotion-code">OTHER</div><input name="promocode" value="SAVE10"/>`), "SAVE10") {
		t.Fatal("input value or another code must not count as applied")
	}
}

// 商品组页 500 是站点故障，不能当成会话失效触发重登。
func TestCheckSessionReportsHTTPErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	instance, _ := New(domain.Site{BaseURL: server.URL, Config: map[string]string{"groups": "g"}})
	client, _ := fetch.NewHTTPClient(5*time.Second, "")
	session, _ := fetch.NewSession(server.URL, nil, "")
	_, err := instance.(*Provider).CheckSession(context.Background(), provider.Env{Client: client, Session: session})
	var httpErr *fetch.HTTPError
	if !errors.As(err, &httpErr) || errors.Is(err, provider.ErrSessionExpired) {
		t.Fatalf("want http error, got %v", err)
	}
}

// AaITR：页首有一个空的 confproduct 表单，真正的字段在后面的 #frmConfigureProduct 里。
func TestConfigureFormPrefersRealForm(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`<html><body>
<form method="post" action="cart.php?a=confproduct&i=0"><input type="hidden" name="token" value="t"/></form>
<form id="frmConfigureProduct"><input name="hostname" value="AaITR-1"/><input type="password" name="rootpw" value="x"/><input name="ns1prefix" value="ns1"/></form>
</body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	form := configureForm(doc)
	if form == nil || form.Find(`input[name="hostname"]`).Length() != 1 {
		t.Fatal("must pick #frmConfigureProduct, not the first confproduct form")
	}
	fallback, _ := goquery.NewDocumentFromReader(strings.NewReader(`<form action="cart.php?a=confproduct"></form><form action="/cart.php?a=confproduct"><input name="hostname"/><input name="rootpw"/></form>`))
	if form := configureForm(fallback); form == nil || form.Find(`input[name="rootpw"]`).Length() != 1 {
		t.Fatal("without the id, the confproduct form with most fields wins")
	}
}

// 见过结算页验证码后，下一单在开始时就预取求解，结算时直接用（只解一次）；结果附分步耗时。
func TestPlaceOrderPrefetchesKnownCaptcha(t *testing.T) {
	fake := &fakeWHMCS{inStock: true, captcha: true}
	slv := &recordingSolver{token: "RECAPTCHA-TOKEN"}
	p, env := newOrderEnv(t, fake, slv)
	spec := domain.OrderSpec{ProductID: "41", Cycle: domain.CycleMonthly}
	if _, err := p.PlaceOrder(context.Background(), env, spec); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.knownCheckoutCaptcha(); !ok || slv.calls != 1 {
		t.Fatalf("first order must learn the captcha and solve once, calls=%d", slv.calls)
	}
	result, err := p.PlaceOrder(context.Background(), env, spec)
	if err != nil {
		t.Fatal(err)
	}
	if slv.calls != 2 || fake.checkout.Get("g-recaptcha-response") != "RECAPTCHA-TOKEN" {
		t.Fatalf("second order must reuse the prefetched token (one solve per order), calls=%d", slv.calls)
	}
	if !strings.Contains(result.Message, "timings: cart=") || !strings.Contains(result.Message, "submit=") {
		t.Fatalf("result must carry step timings: %q", result.Message)
	}
}

func TestMaintenanceError(t *testing.T) {
	if err := maintenanceError("Down for Maintenance (Err 3)"); !errors.Is(err, provider.ErrSiteMaintenance) {
		t.Fatalf("maintenance page not detected: %v", err)
	}
	if err := maintenanceError("Shopping Cart - AaITR"); err != nil {
		t.Fatalf("normal page flagged: %v", err)
	}
}

// 繁体中文界面（SixtyNet 账号语言）：缺货页与下单完成页的订单号都要认得。
func TestTraditionalChineseTexts(t *testing.T) {
	if !outOfStockPattern.MatchString("缺貨中 很抱歉，本產品 / 服務目前處於缺貨中") {
		t.Fatal("繁体「缺貨」必须识别为缺货")
	}
	for body, want := range map[string]string{
		`<p>您的訂單編號是： <span>12345</span></p>`:     "12345",
		`<p>您的订单号是：<strong>678</strong></p>`:     "678",
		`<p>Your order number is: <b>42</b></p>`: "42",
	} {
		match := orderNumberPattern.FindStringSubmatch(body)
		if match == nil || match[1] != want {
			t.Fatalf("order number in %q: got %v, want %s", body, match, want)
		}
	}
}

// Lagom 结算页：真正的 #frmCheckout 前面还有一个也提交到 a=checkout 的小表单（无支付方式），不能选错。
func TestCheckoutFormSkipsEarlierMiniForm(t *testing.T) {
	doc, _ := htmlutil.Parse([]byte(`<html><body>
<form method="post" action="cart.php?a=checkout"><input name="token" value="t"/></form>
<form method="post" action="/cart.php?a=checkout" name="orderfrm" id="frmCheckout"><input type="radio" name="paymentmethod" value="paypal" checked/></form>
<form method="post" action="/x?a=checkout"><input type="radio" name="paymentmethod" value="other"/></form>
</body></html>`))
	if gateway, err := pickGateway(checkoutFormNode(doc), ""); err != nil || gateway != "paypal" {
		t.Fatalf("must pick #frmCheckout's gateway, got %q %v", gateway, err)
	}
	doc, _ = htmlutil.Parse([]byte(`<form action="cart.php?a=checkout"></form><form action="cart.php?a=checkout"><input type="radio" name="paymentmethod" value="stripe"/></form>`))
	if gateway, err := pickGateway(checkoutFormNode(doc), ""); err != nil || gateway != "stripe" {
		t.Fatalf("without #frmCheckout, the a=checkout form with gateways wins, got %q %v", gateway, err)
	}
}
