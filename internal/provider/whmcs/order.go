package whmcs

import (
	"bytes"
	"context"
	"crypto/rand"
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

// do 需要登录的页面：fetchPage 之后落到登录页即视为会话失效；落到「选择账户」页（WHMCS 8 用记住登录
// 恢复会话时未选定客户账户）则自动选定后重试一次。
func (p *Provider) do(ctx context.Context, env provider.Env, req *fetch.Request) (*page, error) {
	result, err := fetchPage(ctx, env, req)
	if err != nil {
		return nil, err
	}
	if chooser := accountChooser(result); chooser != nil {
		if err := p.chooseAccount(ctx, env, result, chooser); err != nil {
			return nil, err
		}
		if result, err = fetchPage(ctx, env, req); err != nil {
			return nil, err
		}
	}
	if isLoginPage(result.doc, result.resp.URL) {
		return nil, provider.ErrSessionExpired
	}
	return result, nil
}

// accountChooser WHMCS 8 的 /user/accounts 选择账户页：返回切换表单（找不到返回 nil）。
func accountChooser(result *page) *goquery.Selection {
	if !strings.Contains(result.resp.URL, "/user/accounts") {
		return nil
	}
	form := result.doc.Find(`form[action*="/user/accounts"]`).FilterFunction(func(_ int, form *goquery.Selection) bool {
		return form.Find(`input[name="id"]`).Length() > 0
	}).First()
	if form.Length() == 0 || result.doc.Find(".select-account a[data-id], a[data-id]").Length() == 0 {
		return nil
	}
	return form
}

// chooseAccount 选定客户账户：优先标为所有者（Owner）的，否则第一个。
func (p *Provider) chooseAccount(ctx context.Context, env provider.Env, result *page, form *goquery.Selection) error {
	links := result.doc.Find(".select-account a[data-id], a[data-id]")
	choice := links.First()
	links.EachWithBreak(func(_ int, link *goquery.Selection) bool {
		text := link.Text()
		if strings.Contains(text, "所有者") || strings.Contains(strings.ToLower(text), "owner") {
			choice = link
			return false
		}
		return true
	})
	values := htmlutil.FormValues(form)
	values.Set("id", choice.AttrOr("data-id", ""))
	if values.Get("token") == "" {
		values.Set("token", htmlutil.CSRFToken(result.doc, result.resp.Body))
	}
	action := htmlutil.Resolve(result.resp.URL, form.AttrOr("action", "/user/accounts"))
	if _, err := fetchPage(ctx, env, &fetch.Request{Method: http.MethodPost, URL: action, Form: values}); err != nil {
		return fmt.Errorf("choose account: %w", err)
	}
	return nil
}

// fetchPage 发请求、检查状态码并解析 HTML。
func fetchPage(ctx context.Context, env provider.Env, req *fetch.Request) (*page, error) {
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
	if err := maintenanceError(htmlutil.CleanText(doc.Find("title").First().Text())); err != nil {
		return nil, err
	}
	return &page{resp: resp, doc: doc}, nil
}

// maintenanceError WHMCS 停机页（标题「Down for Maintenance (Err N)」：数据库 / 授权 / 升级异常）。
func maintenanceError(title string) error {
	if strings.Contains(strings.ToLower(title), "down for maintenance") {
		return fmt.Errorf("%w: %s", provider.ErrSiteMaintenance, title)
	}
	return nil
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
	group, err := fetchPage(ctx, env, &fetch.Request{Method: http.MethodGet, URL: p.GroupURL(p.groups[0])})
	var httpErr *fetch.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound && p.useRouteParam(p.groups[0]) {
		// 重启后还没轮询过目录、不知道站点没开短网址：与 Catalog 一样切到 index.php?rp= 再试。
		group, err = fetchPage(ctx, env, &fetch.Request{Method: http.MethodGet, URL: p.GroupURL(p.groups[0])})
	}
	if err != nil {
		return provider.SessionInfo{}, err
	}
	doc := group.doc
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
	_, quote, _, err := p.prepareCheckout(ctx, env, spec)
	return quote, err
}

// PlaceOrder 锁单：提交结算表单，生成未支付订单与账单（不付款）。
func (p *Provider) PlaceOrder(ctx context.Context, env provider.Env, spec domain.OrderSpec) (domain.OrderResult, error) {
	timings := provider.NewTimings()
	purpose := fmt.Sprintf("%s 锁单 pid=%s", env.Site.Name, spec.ProductID)
	checkoutURL := p.baseURL + "/cart.php?a=checkout"
	var pre *provider.Prefetch
	if captcha, ok := p.knownCheckoutCaptcha(); ok && !spec.DryRun {
		// 加购到打开结算页要好几个往返，验证码同时在后台解。
		pre = env.Prefetch(ctx, captcha.challenge(checkoutURL, purpose))
		defer pre.Stop()
	}
	checkout, quote, promo, err := p.prepareCheckout(ctx, env, spec)
	if err != nil {
		return domain.OrderResult{}, err
	}
	timings.Lap("cart")
	if spec.Coupon != "" && promo == promoRejected {
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
		if found, ok := detectCaptcha(checkout.doc, checkout.resp.Body, captchaCheckout); ok {
			captcha = string(found.kind)
		}
		message := fmt.Sprintf("dry run: stopped before checkout submit; gateway=%s, captcha=%s; %s", gateway, captcha, timings)
		if quote.Message != "" {
			message += "; " + quote.Message
		}
		return domain.OrderResult{Total: quote.Total, Message: message}, nil
	}
	form, err := p.checkoutForm(ctx, env, checkout, spec, pre, purpose)
	if err != nil {
		return domain.OrderResult{}, err
	}
	timings.Lap("captcha_wait")
	timings.Note("captcha_solve", pre.Elapsed())
	done, err := p.post(ctx, env, "/cart.php?a=checkout", form)
	if err != nil {
		return domain.OrderResult{}, err
	}
	timings.Lap("submit")
	result, err := parseCompletion(done, p.baseURL, quote)
	result.Message = timings.Append(result.Message)
	return result, err
}

// prepareCheckout 清空购物车并把目标商品放入，应用优惠码，返回结算页、报价与优惠码结果（无优惠码为 promoUnknown）。
func (p *Provider) prepareCheckout(ctx context.Context, env provider.Env, spec domain.OrderSpec) (*page, domain.Quote, promoResult, error) {
	if err := p.emptyCart(ctx, env); err != nil {
		return nil, domain.Quote{}, promoUnknown, fmt.Errorf("empty cart: %w", err)
	}
	if err := p.addProduct(ctx, env, spec); err != nil {
		return nil, domain.Quote{}, promoUnknown, err
	}
	quote := domain.Quote{}
	promo := promoUnknown
	if spec.Coupon != "" {
		var err error
		promo, quote.Message, err = p.applyPromo(ctx, env, spec.Coupon)
		if err != nil {
			return nil, domain.Quote{}, promoUnknown, fmt.Errorf("apply promo: %w", err)
		}
		quote.CouponValid = promo == promoAccepted
	}
	checkout, err := p.get(ctx, env, "/cart.php?a=checkout")
	if err != nil {
		return nil, domain.Quote{}, promoUnknown, fmt.Errorf("checkout page: %w", err)
	}
	if checkout, err = p.acceptTerms(ctx, env, checkout); err != nil {
		return nil, domain.Quote{}, promoUnknown, err
	}
	if captcha, ok := detectCaptcha(checkout.doc, checkout.resp.Body, captchaCheckout); ok {
		p.rememberCheckoutCaptcha(captcha)
	}
	if checkout.doc.Find(`#frmCheckout, form[name="orderfrm"], form[action*="a=checkout"]`).Length() == 0 {
		return nil, domain.Quote{}, promoUnknown, fmt.Errorf("checkout form not found (cart empty or product unavailable)")
	}
	fillTotals(checkout.doc, p.currency, &quote)
	return checkout, quote, promo, nil
}

// emptyCart 无条件清空购物车：部分主题 a=view 会直接跳转结算页，按条目判断不可靠。
// 注意：购物车随 WHMCS 会话共享，浏览器里同一账号的购物车也会被清空。
func (p *Provider) emptyCart(ctx context.Context, env provider.Env) error {
	view, err := p.get(ctx, env, "/cart.php?a=view")
	if err != nil {
		return err
	}
	token := htmlutil.CSRFToken(view.doc, view.resp.Body)
	// 清空后站点会重定向回购物车页，内容用不上：不跟随，省一次往返。
	resp, err := env.Client.Do(ctx, env.Session, &fetch.Request{Method: http.MethodPost, URL: p.baseURL + "/cart.php?a=empty", Form: url.Values{"token": {token}}, NoRedirect: true})
	if err != nil {
		return err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return &fetch.HTTPError{StatusCode: resp.StatusCode, URL: resp.URL}
	}
	return nil
}

// configureForm 商品配置表单：优先 #frmConfigureProduct；否则在 action 含 confproduct 的表单中取字段最多的。
// 注意 goquery 的 Find("A, B").First() 按文档顺序取第一个，而部分主题（如 AaITR）在页首还有一个空的
// confproduct 表单，不能直接用。
func configureForm(doc *goquery.Document) *goquery.Selection {
	if form := doc.Find("#frmConfigureProduct").First(); form.Length() > 0 {
		return form
	}
	var best *goquery.Selection
	bestFields := 0
	doc.Find("form[action*='confproduct']").Each(func(_ int, form *goquery.Selection) {
		if fields := form.Find("input[name], select[name], textarea[name]").Length(); best == nil || fields > bestFields {
			best, bestFields = form, fields
		}
	})
	return best
}

func (p *Provider) addProduct(ctx context.Context, env provider.Env, spec domain.OrderSpec) error {
	query := url.Values{"a": {"add"}, "pid": {spec.ProductID}}
	if spec.Cycle != "" {
		query.Set("billingcycle", string(spec.Cycle))
	}
	conf, err := p.get(ctx, env, "/cart.php?"+query.Encode())
	if err != nil {
		return fmt.Errorf("add to cart: %w", err)
	}
	form := configureForm(conf.doc)
	if form == nil {
		if inCart(conf.resp.URL) {
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
	// 忽略 ajax 的主题会跳转：落到购物车 / 结算 / 域名页即已入车，页面上的提示不是配置错误。
	if inCart(result.resp.URL) {
		return nil
	}
	if message := ajaxErrors(result); message != "" {
		if outOfStockPattern.MatchString(message) {
			return provider.ErrOutOfStock
		}
		return fmt.Errorf("%w: configure product: %s", provider.ErrOrderRejected, message)
	}
	return nil
}

// inCart 落在购物车 / 结算 / 域名页，说明商品已入车。
func inCart(pageURL string) bool {
	for _, marker := range []string{"a=view", "a=checkout", "a=confdomains"} {
		if strings.Contains(pageURL, marker) {
			return true
		}
	}
	return false
}

// ajaxErrors 解析 a=confproduct&ajax=1 的响应：空表示通过，否则拼接各条 <li> 错误。
// 若站点忽略 ajax 返回了整页，则只看可见的错误提示（整页里的导航 <li> 不能当错误）。
func ajaxErrors(result *page) string {
	doc := result.doc
	if bytes.Contains(bytes.ToLower(result.resp.Body), []byte("<body")) {
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
			hostname = "vps-" + strings.ToLower(rand.Text()[:6]) + ".local"
		}
		values.Set("hostname", hostname)
	}
	// 部分主题 HTML 里 rootpw 预填 "N/A"，靠前端 JS 生成真实密码。
	if _, ok := values["rootpw"]; ok && (values.Get("rootpw") == "" || strings.EqualFold(values.Get("rootpw"), "N/A")) {
		values.Set("rootpw", "Rk"+rand.Text()[:16]+"#9")
	}
	if _, ok := values["ns1prefix"]; ok && values.Get("ns1prefix") == "" {
		values.Set("ns1prefix", "ns1")
	}
	if _, ok := values["ns2prefix"]; ok && values.Get("ns2prefix") == "" {
		values.Set("ns2prefix", "ns2")
	}
}

// promoResult 优惠码校验结果。
type promoResult int

const (
	// promoUnknown 页面没有明确拒绝也没有明确接受：不阻止下单，交给结算页金额体现。
	promoUnknown promoResult = iota
	promoAccepted
	promoRejected
)

// promoCodeSelector 主题里展示已应用优惠码的元素。
// 不能看页面是否含 "promo"：输入框 name="promocode" 永远在页面上。
const promoCodeSelector = `.promo-code, .view-cart-promotion-code, .promotion-code, .promo-code-applied`

func (p *Provider) applyPromo(ctx context.Context, env provider.Env, code string) (promoResult, string, error) {
	view, err := p.get(ctx, env, "/cart.php?a=view")
	if err != nil {
		return promoUnknown, "", err
	}
	token := htmlutil.CSRFToken(view.doc, view.resp.Body)
	result, err := p.post(ctx, env, "/cart.php?a=view", url.Values{"token": {token}, "promocode": {code}, "validatepromo": {"Validate Code"}})
	if err != nil {
		return promoUnknown, "", err
	}
	if message := errorMessage(result.doc); message != "" {
		return promoRejected, message, nil
	}
	if promoApplied(result.doc, code) {
		return promoAccepted, "promo applied", nil
	}
	return promoUnknown, "promo not confirmed by page", nil
}

// promoApplied 购物车出现移除优惠码链接，或展示优惠码的元素里写着这个码。
func promoApplied(doc *goquery.Document, code string) bool {
	if doc.Find(`a[href*="removepromo"]`).Length() > 0 {
		return true
	}
	applied := false
	doc.Find(promoCodeSelector).EachWithBreak(func(_ int, node *goquery.Selection) bool {
		applied = strings.Contains(strings.ToLower(node.Text()), strings.ToLower(code))
		return !applied
	})
	return applied
}

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
func (p *Provider) checkoutForm(ctx context.Context, env provider.Env, checkout *page, spec domain.OrderSpec, pre *provider.Prefetch, purpose string) (url.Values, error) {
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
	if _, err := solveCaptcha(ctx, env, checkout, values, captchaCheckout, p.baseURL+"/cart.php?a=checkout", purpose, pre); err != nil {
		return nil, err
	}
	return values, nil
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
			return result, fmt.Errorf("%w: checkout rejected: %s", provider.ErrOrderRejected, message)
		}
		return result, fmt.Errorf("order confirmation not recognized at %s", done.resp.URL)
	}
	result.Message = "order placed, invoice unpaid"
	return result, nil
}
