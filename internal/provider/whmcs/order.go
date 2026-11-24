package whmcs

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/provider/htmlutil"
	"github.com/genguzzz/restockd/internal/solver"
)

var (
	_ provider.Orderer        = (*Provider)(nil)
	_ provider.SessionChecker = (*Provider)(nil)
)

// page 一次请求得到的已解析页面。
type page struct {
	resp *fetch.Response
	doc  *goquery.Document
}

func (p *Provider) get(ctx context.Context, env provider.Env, path string) (*page, error) {
	return p.do(ctx, env, &fetch.Request{Method: http.MethodGet, URL: p.baseURL + path})
}

func (p *Provider) post(ctx context.Context, env provider.Env, path string, form url.Values) (*page, error) {
	return p.do(ctx, env, &fetch.Request{Method: http.MethodPost, URL: p.baseURL + path, Form: form})
}

func (p *Provider) do(ctx context.Context, env provider.Env, req *fetch.Request) (*page, error) {
	resp, err := env.Client.Do(ctx, env.Session, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, &fetch.HTTPError{StatusCode: resp.StatusCode, URL: resp.URL}
	}
	doc, err := htmlutil.Parse(resp.Body)
	if err != nil {
		return nil, err
	}
	if isLoginPage(doc, resp.URL) {
		return nil, provider.ErrSessionExpired
	}
	return &page{resp: resp, doc: doc}, nil
}

func isLoginPage(doc *goquery.Document, finalURL string) bool {
	if strings.Contains(finalURL, "/login") || strings.Contains(finalURL, "login.php") {
		return true
	}
	return doc.Find(`form input[name="password"]`).Length() > 0 && doc.Find(`a[href*="logout"]`).Length() == 0 && doc.Find(`input[name="username"], input[name="email"]`).Length() > 0
}

// CheckSession 以商品组页面是否带退出链接判断登录态；部分站点 clientarea.php 会强制跳登录页，不可靠。
// 余额与身份尽力从客户中心读取，读不到不影响结论。
func (p *Provider) CheckSession(ctx context.Context, env provider.Env) (provider.SessionInfo, error) {
	resp, err := env.Client.Do(ctx, env.Session, &fetch.Request{Method: http.MethodGet, URL: p.GroupURL(p.groups[0])})
	if err != nil {
		return provider.SessionInfo{}, err
	}
	doc, err := htmlutil.Parse(resp.Body)
	if err != nil {
		return provider.SessionInfo{}, err
	}
	if doc.Find(`a[href*="logout"]`).Length() == 0 {
		return provider.SessionInfo{}, provider.ErrSessionExpired
	}
	info := provider.SessionInfo{Identity: htmlutil.CleanText(doc.Find(".client-name, .user-name, .dropdown-toggle .item-text, .panel-title-user").First().Text())}
	if area, err := p.get(ctx, env, "/clientarea.php"); err == nil {
		if info.Identity == "" {
			info.Identity = htmlutil.CleanText(area.doc.Find(".client-name, .user-name, .panel-title-user").First().Text())
		}
		if money, ok := htmlutil.ParseMoney(area.doc.Find(".client-credit, .credit-balance, [data-credit-balance]").First().Text(), p.currency); ok {
			info.Balance = &money
		}
	}
	return info, nil
}

// Quote 走完 加购 → 配置 → 优惠码 → 结算页，读取应付金额，不提交订单。
func (p *Provider) Quote(ctx context.Context, env provider.Env, spec domain.OrderSpec) (domain.Quote, error) {
	_, quote, err := p.prepareCheckout(ctx, env, spec)
	return quote, err
}

// PlaceOrder 锁单：提交结算表单，生成未支付订单与账单（不付款）。
func (p *Provider) PlaceOrder(ctx context.Context, env provider.Env, spec domain.OrderSpec) (domain.OrderResult, error) {
	checkout, quote, err := p.prepareCheckout(ctx, env, spec)
	if err != nil {
		return domain.OrderResult{}, err
	}
	if spec.Coupon != "" && !quote.CouponValid && quote.Message != promoUnconfirmed {
		return domain.OrderResult{Total: quote.Total}, fmt.Errorf("%w: %s: %s", provider.ErrCouponInvalid, spec.Coupon, quote.Message)
	}
	if spec.DryRun {
		// 结算表单能组装（支付方式可选）才算链路通；验证码只探测不求解。
		form := checkout.doc.Find(`#frmCheckout, form[name="orderfrm"], form[action*="a=checkout"]`).First()
		gateway, err := pickGateway(form, spec.Gateway)
		if err != nil {
			return domain.OrderResult{Total: quote.Total}, err
		}
		captcha := "none"
		if kind, _, found := htmlutil.DetectCaptcha(checkout.doc); found {
			captcha = string(kind)
		}
		message := fmt.Sprintf("dry run: stopped before checkout submit; gateway=%s, captcha=%s", gateway, captcha)
		if quote.Message != "" {
			message += "; " + quote.Message
		}
		return domain.OrderResult{Total: quote.Total, Message: message}, nil
	}
	form, err := p.checkoutForm(ctx, env, checkout, spec)
	if err != nil {
		return domain.OrderResult{}, err
	}
	done, err := p.post(ctx, env, "/cart.php?a=checkout", form)
	if err != nil {
		return domain.OrderResult{}, err
	}
	return parseCompletion(done, p.baseURL, quote)
}

// prepareCheckout 清空购物车并把目标商品放入，应用优惠码，返回结算页与报价。
func (p *Provider) prepareCheckout(ctx context.Context, env provider.Env, spec domain.OrderSpec) (*page, domain.Quote, error) {
	if err := p.emptyCart(ctx, env); err != nil {
		return nil, domain.Quote{}, fmt.Errorf("empty cart: %w", err)
	}
	if err := p.addProduct(ctx, env, spec); err != nil {
		return nil, domain.Quote{}, err
	}
	quote := domain.Quote{}
	if spec.Coupon != "" {
		valid, message, err := p.applyPromo(ctx, env, spec.Coupon)
		if err != nil {
			return nil, domain.Quote{}, fmt.Errorf("apply promo: %w", err)
		}
		quote.CouponValid = valid
		quote.Message = message
	}
	checkout, err := p.get(ctx, env, "/cart.php?a=checkout")
	if err != nil {
		return nil, domain.Quote{}, fmt.Errorf("checkout page: %w", err)
	}
	if checkout.doc.Find(`#frmCheckout, form[name="orderfrm"], form[action*="a=checkout"]`).Length() == 0 {
		return nil, domain.Quote{}, fmt.Errorf("checkout form not found (cart empty or product unavailable)")
	}
	fillTotals(checkout.doc, p.currency, &quote)
	return checkout, quote, nil
}

// emptyCart 无条件清空购物车：部分主题 a=view 会直接跳转结算页，按条目判断不可靠。
// 注意：购物车随 WHMCS 会话共享，浏览器里同一账号的购物车也会被清空。
func (p *Provider) emptyCart(ctx context.Context, env provider.Env) error {
	view, err := p.get(ctx, env, "/cart.php?a=view")
	if err != nil {
		return err
	}
	token := htmlutil.CSRFToken(view.doc, view.resp.Body)
	_, err = p.post(ctx, env, "/cart.php?a=empty", url.Values{"token": {token}})
	return err
}

var outOfStockPattern = regexp.MustCompile(`(?i)out of stock|currently unavailable|缺货|售罄|库存不足`)

func (p *Provider) addProduct(ctx context.Context, env provider.Env, spec domain.OrderSpec) error {
	query := url.Values{"a": {"add"}, "pid": {spec.ProductID}}
	if spec.Cycle != "" {
		query.Set("billingcycle", string(spec.Cycle))
	}
	conf, err := p.get(ctx, env, "/cart.php?"+query.Encode())
	if err != nil {
		return fmt.Errorf("add to cart: %w", err)
	}
	form := conf.doc.Find("#frmConfigureProduct, form[action*='confproduct']").First()
	if form.Length() == 0 {
		if strings.Contains(conf.resp.URL, "a=view") || strings.Contains(conf.resp.URL, "a=confdomains") {
			return nil
		}
		if outOfStockPattern.Match(conf.resp.Body) {
			return provider.ErrOutOfStock
		}
		return fmt.Errorf("configure form not found at %s", conf.resp.URL)
	}
	values := htmlutil.FormValues(form)
	values.Set("a", "confproduct")
	values.Set("configure", "true")
	if values.Get("i") == "" {
		values.Set("i", "0")
	}
	if spec.Cycle != "" {
		values.Set("billingcycle", string(spec.Cycle))
	}
	applyServerFields(values, spec)
	for key, value := range spec.Options {
		values.Set(key, value)
	}
	if values.Get("token") == "" {
		values.Set("token", htmlutil.CSRFToken(conf.doc, conf.resp.Body))
	}
	// 与浏览器一致走 AJAX 校验：成功时响应为空，失败时返回 <li> 错误列表。
	// 非 AJAX 提交在部分主题（Lagom）下会停留在配置页且不入车，页面里还常驻一个隐藏的
	// “请更正下列错误”模板，无法据此判断成败。
	values.Set("ajax", "1")
	result, err := p.post(ctx, env, "/cart.php", values)
	if err != nil {
		return fmt.Errorf("configure product: %w", err)
	}
	if message := ajaxErrors(result); message != "" {
		if outOfStockPattern.MatchString(message) {
			return provider.ErrOutOfStock
		}
		return fmt.Errorf("configure product: %s", message)
	}
	return nil
}

// ajaxErrors 解析 a=confproduct&ajax=1 的响应：空表示通过，否则拼接各条 <li> 错误。
// 若站点忽略 ajax 返回了整页，则只看可见的错误提示（整页里的导航 <li> 不能当错误）。
func ajaxErrors(result *page) string {
	doc := result.doc
	if bytes.Contains(bytes.ToLower(result.resp.Body), []byte("<body")) {
		// 忽略 ajax 的主题会跳转：落到购物车 / 结算 / 域名页即已入车，页面上的提示不是配置错误。
		for _, marker := range []string{"a=view", "a=checkout", "a=confdomains"} {
			if strings.Contains(result.resp.URL, marker) {
				return ""
			}
		}
		return errorMessage(doc)
	}
	var messages []string
	doc.Find("li").Each(func(_ int, node *goquery.Selection) {
		if text := htmlutil.CleanText(node.Text()); text != "" {
			messages = append(messages, text)
		}
	})
	if len(messages) > 0 {
		return strings.Join(messages, "; ")
	}
	return errorMessage(doc)
}

// applyServerFields 填写 VPS 商品常见的主机名 / root 密码 / NS 前缀，避免必填校验失败。
func applyServerFields(values url.Values, spec domain.OrderSpec) {
	if _, ok := values["hostname"]; ok && (values.Get("hostname") == "" || spec.Hostname != "") {
		hostname := spec.Hostname
		if hostname == "" {
			hostname = "vps-" + randomHex(3) + ".local"
		}
		values.Set("hostname", hostname)
	}
	// 部分主题 HTML 里 rootpw 预填 "N/A"，靠前端 JS 生成真实密码。
	if _, ok := values["rootpw"]; ok && (values.Get("rootpw") == "" || strings.EqualFold(values.Get("rootpw"), "N/A")) {
		values.Set("rootpw", "Rk"+randomHex(8)+"#9")
	}
	if _, ok := values["ns1prefix"]; ok && values.Get("ns1prefix") == "" {
		values.Set("ns1prefix", "ns1")
	}
	if _, ok := values["ns2prefix"]; ok && values.Get("ns2prefix") == "" {
		values.Set("ns2prefix", "ns2")
	}
}

func (p *Provider) applyPromo(ctx context.Context, env provider.Env, code string) (bool, string, error) {
	view, err := p.get(ctx, env, "/cart.php?a=view")
	if err != nil {
		return false, "", err
	}
	token := htmlutil.CSRFToken(view.doc, view.resp.Body)
	result, err := p.post(ctx, env, "/cart.php?a=view", url.Values{"token": {token}, "promocode": {code}, "validatepromo": {"Validate Code"}})
	if err != nil {
		return false, "", err
	}
	if message := errorMessage(result.doc); message != "" {
		return false, message, nil
	}
	body := strings.ToLower(string(result.resp.Body))
	if strings.Contains(body, strings.ToLower(code)) && (strings.Contains(body, "removepromo") || strings.Contains(body, "promotion code accepted") || strings.Contains(body, "promo")) {
		return true, "promo applied", nil
	}
	return false, promoUnconfirmed, nil
}

// promoUnconfirmed 页面没有明确拒绝也没有明确接受：不阻止下单，交给结算页金额体现。
const promoUnconfirmed = "promo not confirmed by page"

// errorMessage 取第一条可见的错误提示；跳过主题常驻的隐藏模板（.hidden / .d-none / display:none）。
func errorMessage(doc *goquery.Document) string {
	message := ""
	doc.Find(".alert-danger, .checkout-error-feedback, .errorbox, #errorBox").EachWithBreak(func(_ int, node *goquery.Selection) bool {
		if isHidden(node) {
			return true
		}
		message = htmlutil.CleanText(node.Text())
		return message == ""
	})
	return message
}

func isHidden(node *goquery.Selection) bool {
	for current := node; current.Length() > 0; current = current.Parent() {
		if current.HasClass("hidden") || current.HasClass("d-none") || current.HasClass("w-hidden") {
			return true
		}
		style := strings.ReplaceAll(strings.ToLower(current.AttrOr("style", "")), " ", "")
		if strings.Contains(style, "display:none") {
			return true
		}
	}
	return false
}

func fillTotals(doc *goquery.Document, currency string, quote *domain.Quote) {
	if money, ok := htmlutil.ParseMoney(doc.Find("#totalDueToday, .total-due-today .amt, #totalCartPrice, .order-summary .total").First().Text(), currency); ok {
		quote.Total = money
	}
	if money, ok := htmlutil.ParseMoney(doc.Find("#subtotal, .subtotal span:last-child").First().Text(), currency); ok {
		quote.Subtotal = money
	} else {
		quote.Subtotal = quote.Total
	}
	if money, ok := htmlutil.ParseMoney(doc.Find("#discount, .bordered-totals .discount, .promo-discount").First().Text(), currency); ok {
		quote.Discount = money
	}
}

// checkoutForm 组装结算表单：已登录用户 custtype=existing，选择支付方式，同意条款，处理验证码。
func (p *Provider) checkoutForm(ctx context.Context, env provider.Env, checkout *page, spec domain.OrderSpec) (url.Values, error) {
	form := checkout.doc.Find(`#frmCheckout, form[name="orderfrm"], form[action*="a=checkout"]`).First()
	values := htmlutil.FormValues(form)
	values.Set("submit", "true")
	values.Set("custtype", "existing")
	values.Set("accepttos", "on")
	if values.Get("token") == "" {
		values.Set("token", htmlutil.CSRFToken(checkout.doc, checkout.resp.Body))
	}
	gateway, err := pickGateway(form, spec.Gateway)
	if err != nil {
		return nil, err
	}
	values.Set("paymentmethod", gateway)
	if kind, siteKey, found := htmlutil.DetectCaptcha(checkout.doc); found {
		if env.Solver == nil {
			return nil, fmt.Errorf("checkout requires %s but no solver configured", kind)
		}
		solution, err := env.Solver.Solve(ctx, solver.Challenge{Kind: kind, SiteKey: siteKey, PageURL: p.baseURL + "/cart.php?a=checkout", SiteID: env.Site.ID, AccountID: accountID(env), Purpose: fmt.Sprintf("%s 锁单 pid=%s", env.Site.Name, spec.ProductID)})
		if err != nil {
			return nil, fmt.Errorf("solve %s: %w", kind, err)
		}
		values.Set(htmlutil.CaptchaField(kind), solution.Token)
	}
	return values, nil
}

func accountID(env provider.Env) string {
	if env.Account == nil {
		return ""
	}
	return env.Account.ID
}

func pickGateway(form *goquery.Selection, preferred string) (string, error) {
	var gateways []string
	form.Find(`input[name="paymentmethod"], select[name="paymentmethod"] option`).Each(func(_ int, node *goquery.Selection) {
		if value := node.AttrOr("value", ""); value != "" {
			gateways = append(gateways, value)
		}
	})
	if preferred != "" {
		for _, gateway := range gateways {
			if strings.EqualFold(gateway, preferred) {
				return gateway, nil
			}
		}
		if len(gateways) > 0 {
			return "", fmt.Errorf("gateway %q not offered, available: %s", preferred, strings.Join(gateways, ","))
		}
		return preferred, nil
	}
	if len(gateways) == 0 {
		return "", errors.New("no payment method on checkout page")
	}
	return gateways[0], nil
}

var (
	orderNumberPattern = regexp.MustCompile(`(?i)order number is:?\s*(?:<[^>]+>\s*)*(\d+)`)
	invoicePattern     = regexp.MustCompile(`viewinvoice\.php\?id=(\d+)`)
	orderIDPattern     = regexp.MustCompile(`[?&]orderid=(\d+)|[?&]id=(\d+)`)
)

func parseCompletion(done *page, baseURL string, quote domain.Quote) (domain.OrderResult, error) {
	result := domain.OrderResult{Total: quote.Total}
	body := string(done.resp.Body)
	if match := invoicePattern.FindStringSubmatch(done.resp.URL + " " + body); match != nil {
		result.InvoiceID = match[1]
		result.PayURL = baseURL + "/viewinvoice.php?id=" + match[1]
	}
	if match := orderNumberPattern.FindStringSubmatch(body); match != nil {
		result.OrderID = match[1]
	} else if strings.Contains(done.resp.URL, "a=complete") {
		if match := orderIDPattern.FindStringSubmatch(done.resp.URL); match != nil {
			result.OrderID = match[1] + match[2]
		}
	}
	if result.OrderID == "" && result.InvoiceID == "" {
		if message := errorMessage(done.doc); message != "" {
			if outOfStockPattern.MatchString(message) {
				return result, provider.ErrOutOfStock
			}
			return result, fmt.Errorf("checkout rejected: %s", message)
		}
		return result, fmt.Errorf("order confirmation not recognized at %s", done.resp.URL)
	}
	result.Message = "order placed, invoice unpaid"
	return result, nil
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
