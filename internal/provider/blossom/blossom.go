// Package blossom 适配 Blossom Host（blossomhost.us）：自研单页商店，全部走 JSON 接口。
//
//   - 目录：公开的 GET /api/catalog（全部套餐、库存数、各周期价），不需要登录。
//   - 登录：POST /auth/email/login {email, password}，会话在 Cookie 里；GET /api/me 校验。
//   - 报价：POST /api/quote（不占库存），试下单只做到这里。
//   - 锁单：POST /api/checkout 生成订单与托管支付页（站点为其保留服务器 7 分钟，过期未付自动释放）。
//     结账要 Turnstile（sitekey 来自 /api/guard-config，action=checkout），token 放在 _signals.turnstileToken；
//     缺了站点回 428。token 与报价并行提前求解。
//   - 取消：POST /api/orders/{id}/cancel（未付款订单不产生费用）。
//
// 站点的风控（设备 / 行为信号、传输指纹、频率）在服务端判断。这里按普通 API 客户端调用，
// _signals 里只放真实求解的 Turnstile token，不伪造设备 / 行为信号；被风控拒绝时原样报告。
package blossom

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/solver"
)

// Kind 注册名。
const Kind = "blossom"

const (
	// legalVersion / seattleIPVersion 站点前端提交的条款版本（app.js 的 LEGAL_VERSION / SEATTLE_IP_VERSION）。
	// 站点改条款后服务端会拒绝旧版本，届时从报错里看到新版本号再更新。
	legalVersion     = "2026-09-20"
	seattleIPVersion = "2026-09-29-v1"
	defaultGateway   = "alipay"
)

func init() {
	provider.Register(meta(), New)
}

func meta() provider.Meta {
	return provider.Meta{
		Kind:            Kind,
		Name:            "Blossom Host",
		Description:     "blossomhost.us（自研商店）：公开 /api/catalog 监控库存，邮箱密码登录后报价 / 结账锁单（支付页保留 7 分钟）",
		Caps:            provider.Capabilities{Catalog: true, Coupon: true, Order: true, SessionCheck: true, Login: true, Cancel: true},
		Captcha:         []solver.Kind{solver.KindTurnstile},
		MinInterval:     30 * time.Second,
		DefaultInterval: 60 * time.Second,
		DefaultFetch:    domain.FetchDirect,
		ConfigFields: []provider.Field{
			{Key: "families", Label: "只看这些系列", Type: provider.FieldList, Placeholder: "isp,metal", Help: "套餐的 family（isp 家宽线路、metal 独服），留空表示全部"},
		},
	}
}

// Provider Blossom Host 实现。
type Provider struct {
	baseURL  string
	families map[string]bool
	mu       sync.Mutex
	// siteKey 结账 Turnstile 的 sitekey（/api/guard-config），首次锁单时取并缓存。
	siteKey string
}

var (
	_ provider.Orderer        = (*Provider)(nil)
	_ provider.SessionChecker = (*Provider)(nil)
	_ provider.Authenticator  = (*Provider)(nil)
	_ provider.Canceler       = (*Provider)(nil)
)

// New 构造实例。
func New(site domain.Site) (provider.Provider, error) {
	families := map[string]bool{}
	for _, family := range provider.SplitList(site.Config["families"]) {
		families[strings.ToLower(family)] = true
	}
	return &Provider{baseURL: cmp.Or(strings.TrimRight(site.BaseURL, "/"), "https://blossomhost.us"), families: families}, nil
}

// Meta 实现 provider.Provider。
func (p *Provider) Meta() provider.Meta { return meta() }

// ---- catalog ----

type rawCatalog struct {
	Plans          []rawPlan `json:"plans"`
	PaymentMethods []struct {
		Key    string `json:"key"`
		Status string `json:"status"`
	} `json:"payment_methods"`
}

type rawPlan struct {
	Key                string             `json:"key"`
	Name               string             `json:"name"`
	Family             string             `json:"family"`
	OfferLabel         string             `json:"offer_label"`
	Location           string             `json:"location"`
	Carrier            string             `json:"carrier"`
	IPLabel            string             `json:"ip_label"`
	StorageLabel       string             `json:"storage_label"`
	VCPU               int                `json:"vcpu"`
	RAMGB              float64            `json:"ram_gb"`
	DiskGB             int                `json:"disk_gb"`
	HDDGB              int                `json:"hdd_gb"`
	BandwidthGbps      float64            `json:"bandwidth_gbps"`
	TransferGB         int                `json:"transfer_gb"`
	ComingSoon         bool               `json:"coming_soon"`
	Physical           bool               `json:"physical"`
	StockCount         int                `json:"stock_count"`
	InStock            bool               `json:"in_stock"`
	AvailabilityStatus string             `json:"availability_status"`
	TermPrices         map[string]float64 `json:"term_prices"`
	// Preorder 预售套餐（如 AT&T 洛杉矶）：Available 为开放预订，Remaining 剩余名额；下单须带 Version 表示接受预售条款。
	Preorder *struct {
		Available      bool     `json:"available"`
		Version        string   `json:"version"`
		Remaining      int      `json:"remaining"`
		PaymentMethods []string `json:"payment_methods"`
	} `json:"preorder"`
	OSImages []struct {
		Key  string `json:"key"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	} `json:"os_images"`
}

// termCycles 站点的付费月数 ↔ 计费周期。
var termCycles = map[int]domain.BillingCycle{1: domain.CycleMonthly, 3: domain.CycleQuarterly, 6: domain.CycleSemiannually, 12: domain.CycleAnnually}

func cycleMonths(cycle domain.BillingCycle) (int, bool) {
	for months, candidate := range termCycles {
		if candidate == cycle {
			return months, true
		}
	}
	return 0, false
}

// Catalog 实现 provider.Provider。
func (p *Provider) Catalog(ctx context.Context, env provider.Env) (domain.Catalog, error) {
	catalog, err := p.loadCatalog(ctx, env)
	if err != nil {
		return domain.Catalog{}, err
	}
	out := domain.Catalog{}
	for _, method := range catalog.PaymentMethods {
		if method.Status == "live" {
			out.Gateways = append(out.Gateways, method.Key)
		}
	}
	for _, plan := range catalog.Plans {
		if len(p.families) > 0 && !p.families[strings.ToLower(plan.Family)] {
			continue
		}
		out.Products = append(out.Products, p.product(plan))
	}
	return out, nil
}

func (p *Provider) loadCatalog(ctx context.Context, env provider.Env) (rawCatalog, error) {
	var catalog rawCatalog
	if err := p.call(ctx, env, http.MethodGet, "/api/catalog", nil, &catalog); err != nil {
		return rawCatalog{}, fmt.Errorf("catalog: %w", err)
	}
	if len(catalog.Plans) == 0 {
		return rawCatalog{}, errors.New("catalog: no plans in response")
	}
	return catalog, nil
}

func (p *Provider) product(plan rawPlan) domain.Product {
	product := domain.Product{
		ID:    plan.Key,
		Group: cmp.Or(plan.OfferLabel, plan.Family),
		Name:  strings.Join(nonEmpty(plan.Name, plan.Location), " · "),
		URL:   p.baseURL + "/#/buy/" + plan.Key,
		Specs: domain.Specs{
			CPU:           strconv.Itoa(plan.VCPU),
			RAMMB:         int(math.Round(plan.RAMGB * 1024)),
			DiskGB:        plan.DiskGB,
			BandwidthMbps: int(math.Round(plan.BandwidthGbps * 1000)),
			TrafficGB:     plan.TransferGB,
			Location:      plan.Location,
			IPType:        plan.IPLabel,
			Extra:         map[string]string{},
		},
		Stock: stock(plan),
	}
	for key, value := range map[string]string{"carrier": plan.Carrier, "storage": plan.StorageLabel} {
		if value != "" {
			product.Specs.Extra[key] = value
		}
	}
	if plan.HDDGB > 0 {
		product.Specs.Extra["hdd_gb"] = strconv.Itoa(plan.HDDGB)
	}
	terms := make([]int, 0, len(plan.TermPrices))
	for term := range plan.TermPrices {
		if months, err := strconv.Atoi(term); err == nil {
			terms = append(terms, months)
		}
	}
	sort.Ints(terms)
	for _, months := range terms {
		cycle, ok := termCycles[months]
		if !ok || (plan.Physical && months != 1) { // 独服只按月
			continue
		}
		product.Prices = append(product.Prices, domain.Price{Cycle: cycle, Amount: usd(plan.TermPrices[strconv.Itoa(months)]), RefID: strconv.Itoa(months)})
	}
	for _, image := range plan.OSImages {
		product.OSTemplates = append(product.OSTemplates, domain.Choice{Value: image.Key, Label: image.Name})
	}
	return product
}

// stock 站点库存状态：available 可买；sold_out / reserved（剩余库存正被他人结账）无货；
// 预售套餐看 preorder.available（开放时可下单，名额为 remaining），关闭时为 closed；coming_soon 未开卖。
func stock(plan rawPlan) domain.Stock {
	qty := plan.StockCount
	status := plan.AvailabilityStatus
	switch {
	case plan.ComingSoon:
		return domain.Stock{Status: domain.StockClosed, Reason: "coming_soon"}
	case plan.InStock || status == "available":
		// 现货优先：有些现货套餐还带着已结束的旧预售字段（如 Texas）。
		return domain.Stock{Status: domain.StockIn, Qty: &qty, Reason: status}
	case plan.Preorder != nil && plan.Preorder.Available:
		remaining := plan.Preorder.Remaining
		return domain.Stock{Status: domain.StockPreorder, Qty: &remaining, Reason: status}
	case plan.Preorder != nil || strings.HasPrefix(status, "preorder"):
		return domain.Stock{Status: domain.StockClosed, Reason: status}
	default:
		zero := 0
		return domain.Stock{Status: domain.StockOut, Qty: &zero, Reason: cmp.Or(status, "sold_out")}
	}
}

// ---- session ----

type me struct {
	PlatformID any    `json:"platform_id"`
	Display    string `json:"display"`
	Email      string `json:"email"`
}

// CheckSession 实现 provider.SessionChecker：GET /api/me。
func (p *Provider) CheckSession(ctx context.Context, env provider.Env) (provider.SessionInfo, error) {
	var who me
	if err := p.call(ctx, env, http.MethodGet, "/api/me", nil, &who); err != nil {
		return provider.SessionInfo{}, err
	}
	if who.PlatformID == nil {
		return provider.SessionInfo{}, provider.ErrSessionExpired
	}
	return provider.SessionInfo{Identity: cmp.Or(who.Email, who.Display)}, nil
}

// Login 实现 provider.Authenticator：邮箱密码登录（站点目前不要求验证码）。
func (p *Provider) Login(ctx context.Context, env provider.Env, creds domain.Credentials) (provider.SessionInfo, error) {
	err := p.call(ctx, env, http.MethodPost, "/auth/email/login", map[string]string{"email": creds.Username, "password": creds.Password}, nil)
	var apiErr *apiError
	if errors.As(err, &apiErr) && (apiErr.status == http.StatusUnauthorized || apiErr.status == http.StatusBadRequest) {
		return provider.SessionInfo{}, fmt.Errorf("%w: %s", provider.ErrLoginFailed, apiErr.message)
	}
	if err != nil {
		return provider.SessionInfo{}, fmt.Errorf("login: %w", err)
	}
	return p.CheckSession(ctx, env)
}

// ---- ordering ----

// orderPayload 与站点前端 checkoutPayload() 一致。
type orderPayload struct {
	Plan              string   `json:"plan"`
	OS                string   `json:"os"`
	TermMonths        int      `json:"term_months"`
	Addons            []string `json:"addons"`
	Coupon            *string  `json:"coupon"`
	Ref               *string  `json:"ref"`
	Provider          string   `json:"provider"`
	TermsAccepted     bool     `json:"terms_accepted"`
	TermsVersion      string   `json:"terms_version"`
	PreorderAccepted  bool     `json:"preorder_accepted"`
	PreorderVersion   *string  `json:"preorder_version"`
	SeattleIPAccepted bool     `json:"seattle_ip_accepted"`
	SeattleIPVersion  string   `json:"seattle_ip_version"`
	// Signals 仅结账时带：站点前端在这里放风控信号与 Turnstile token，我们只放 token。
	Signals *signals `json:"_signals,omitempty"`
}

type signals struct {
	TurnstileToken string `json:"turnstileToken"`
}

type quoteResponse struct {
	Total          float64 `json:"total"`
	TermSubtotal   float64 `json:"term_subtotal"`
	CouponCode     *string `json:"coupon_code"`
	CouponDiscount float64 `json:"coupon_discount"`
	Months         int     `json:"months"`
}

// payload 组装下单参数：周期换成月数；OS 未指定时取套餐第一个 Linux 镜像；附加项来自 Options 里值为 1/true 的键。
func (p *Provider) payload(ctx context.Context, env provider.Env, spec domain.OrderSpec) (orderPayload, rawPlan, error) {
	catalog, err := p.loadCatalog(ctx, env)
	if err != nil {
		return orderPayload{}, rawPlan{}, err
	}
	var plan *rawPlan
	for i := range catalog.Plans {
		if catalog.Plans[i].Key == spec.ProductID {
			plan = &catalog.Plans[i]
		}
	}
	if plan == nil {
		return orderPayload{}, rawPlan{}, fmt.Errorf("%w: unknown plan %q", provider.ErrOrderRejected, spec.ProductID)
	}
	months, ok := cycleMonths(cmp.Or(spec.Cycle, domain.CycleMonthly))
	if !ok {
		return orderPayload{}, rawPlan{}, fmt.Errorf("%w: cycle %s not offered (1/3/6/12 months)", provider.ErrOrderRejected, spec.Cycle)
	}
	os := spec.OS
	if os == "" && len(plan.OSImages) > 0 {
		os = plan.OSImages[0].Key
	}
	payload := orderPayload{
		Plan: plan.Key, OS: os, TermMonths: months, Addons: []string{}, Provider: cmp.Or(spec.Gateway, defaultGateway),
		TermsAccepted: true, TermsVersion: legalVersion, SeattleIPAccepted: strings.Contains(plan.Key, "seattle"), SeattleIPVersion: seattleIPVersion,
	}
	if spec.Coupon != "" {
		payload.Coupon = &spec.Coupon
	}
	for key, value := range spec.Options {
		if value == "1" || strings.EqualFold(value, "true") {
			payload.Addons = append(payload.Addons, key)
		}
	}
	sort.Strings(payload.Addons)
	if stock(*plan).Status == domain.StockPreorder {
		// 按预售购买（没有现货、预售开放）：等同网页上勾选「交付最长 30 天」的预售条款；预售不支持加密货币支付。
		payload.PreorderAccepted, payload.PreorderVersion = true, &plan.Preorder.Version
		if methods := plan.Preorder.PaymentMethods; payload.Provider == "crypto" || (len(methods) > 0 && !slices.Contains(methods, payload.Provider)) {
			return orderPayload{}, rawPlan{}, fmt.Errorf("%w: preorder accepts %v, not %s", provider.ErrOrderRejected, plan.Preorder.PaymentMethods, payload.Provider)
		}
	}
	if plan.Physical { // 独服：只按月、只刷卡、无附加项与优惠码（与前端一致）
		payload.TermMonths, payload.Addons, payload.Coupon, payload.Provider = 1, []string{}, nil, "stripe"
	}
	return payload, *plan, nil
}

// Quote 实现 provider.Orderer：POST /api/quote，不占库存。
func (p *Provider) Quote(ctx context.Context, env provider.Env, spec domain.OrderSpec) (domain.Quote, error) {
	payload, _, err := p.payload(ctx, env, spec)
	if err != nil {
		return domain.Quote{}, err
	}
	return p.quote(ctx, env, payload)
}

func (p *Provider) quote(ctx context.Context, env provider.Env, payload orderPayload) (domain.Quote, error) {
	var q quoteResponse
	if err := p.call(ctx, env, http.MethodPost, "/api/quote", payload, &q); err != nil {
		return domain.Quote{}, fmt.Errorf("quote: %w", rejected(err))
	}
	quote := domain.Quote{Subtotal: usd(q.TermSubtotal), Discount: usd(q.CouponDiscount), Total: usd(q.Total), CouponValid: q.CouponCode != nil}
	if payload.Coupon != nil && q.CouponCode == nil {
		quote.Message = "coupon does not apply to this selection"
	}
	return quote, nil
}

type checkoutResponse struct {
	OrderID     json.Number `json:"order_id"`
	CheckoutURL string      `json:"checkout_url"`
	Provider    string      `json:"provider"`
	Status      string      `json:"status"`
	Mode        string      `json:"mode"`
}

// PlaceOrder 实现 provider.Orderer：报价确认可买后 POST /api/checkout；试下单停在报价。
func (p *Provider) PlaceOrder(ctx context.Context, env provider.Env, spec domain.OrderSpec) (domain.OrderResult, error) {
	timings := provider.NewTimings()
	var pre *provider.Prefetch
	var challenge solver.Challenge
	if !spec.DryRun {
		siteKey, err := p.checkoutSiteKey(ctx, env)
		if err != nil {
			return domain.OrderResult{}, err
		}
		challenge = solver.Challenge{Kind: solver.KindTurnstile, SiteKey: siteKey, PageURL: p.baseURL + "/#/buy", Action: "checkout", Purpose: env.Site.Name + " 锁单 " + spec.ProductID}
		pre = env.Prefetch(ctx, challenge) // 与读目录、报价并行
		defer pre.Stop()
	}
	payload, plan, err := p.payload(ctx, env, spec)
	if err != nil {
		return domain.OrderResult{}, err
	}
	if status := stock(plan).Status; !status.Orderable() {
		return domain.OrderResult{}, fmt.Errorf("%w: %s is %s", provider.ErrOutOfStock, plan.Key, plan.AvailabilityStatus)
	}
	quote, err := p.quote(ctx, env, payload)
	if err != nil {
		return domain.OrderResult{}, err
	}
	if spec.Coupon != "" && !quote.CouponValid {
		return domain.OrderResult{Total: quote.Total}, fmt.Errorf("%w: %s: %s", provider.ErrCouponInvalid, spec.Coupon, quote.Message)
	}
	if spec.DryRun {
		return domain.OrderResult{Total: quote.Total, Message: fmt.Sprintf("dry run: quoted, checkout not submitted; plan=%s os=%s months=%d gateway=%s stock=%d", payload.Plan, payload.OS, payload.TermMonths, payload.Provider, plan.StockCount)}, nil
	}
	timings.Lap("quote")
	solution, err := pre.Solve(ctx, env, challenge)
	if err != nil {
		return domain.OrderResult{Total: quote.Total}, err
	}
	timings.Lap("captcha_wait")
	timings.Note("captcha_solve", pre.Elapsed())
	payload.Signals = &signals{TurnstileToken: solution.Token}
	var resp checkoutResponse
	err = p.call(ctx, env, http.MethodPost, "/api/checkout", payload, &resp)
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.status == http.StatusPreconditionRequired {
		// token 单次有效、5 分钟过期：站点仍要验证就现解一个再试一次。
		if solution, err = env.Solve(ctx, challenge); err != nil {
			return domain.OrderResult{Total: quote.Total}, err
		}
		payload.Signals = &signals{TurnstileToken: solution.Token}
		resp = checkoutResponse{}
		err = p.call(ctx, env, http.MethodPost, "/api/checkout", payload, &resp)
	}
	timings.Lap("checkout")
	if err != nil {
		return domain.OrderResult{Total: quote.Total}, fmt.Errorf("checkout: %w; %s", rejected(err), timings)
	}
	switch {
	case resp.Status == "availability":
		return domain.OrderResult{Total: quote.Total}, fmt.Errorf("%w: site reports the plan is no longer available", provider.ErrOutOfStock)
	case resp.Status == "queue":
		// 最后几台正被他人结账：站点让排队等释放，稍后重试。
		return domain.OrderResult{Total: quote.Total}, errors.New("checkout queued: the last servers are being checked out by others, retry shortly")
	case resp.Mode == "elements":
		return domain.OrderResult{Total: quote.Total}, fmt.Errorf("%w: gateway %s needs the in-page card form; use alipay or crypto", provider.ErrOrderRejected, payload.Provider)
	case resp.OrderID == "":
		return domain.OrderResult{Total: quote.Total}, fmt.Errorf("%w: checkout returned no order (provider %s)", provider.ErrOrderRejected, resp.Provider)
	}
	return domain.OrderResult{
		OrderID: resp.OrderID.String(), PayURL: resp.CheckoutURL, Total: quote.Total,
		Message: timings.Append(fmt.Sprintf("payment page via %s; the site holds the server for 7 minutes", cmp.Or(resp.Provider, payload.Provider))),
	}, nil
}

// checkoutSiteKey 结账 Turnstile 的 sitekey（与站点前端一样读 /api/guard-config），缓存在实例上。
func (p *Provider) checkoutSiteKey(ctx context.Context, env provider.Env) (string, error) {
	p.mu.Lock()
	key := p.siteKey
	p.mu.Unlock()
	if key != "" {
		return key, nil
	}
	var config struct {
		TurnstileSiteKey string `json:"turnstile_site_key"`
	}
	if err := p.call(ctx, env, http.MethodGet, "/api/guard-config", nil, &config); err != nil {
		return "", fmt.Errorf("guard config: %w", err)
	}
	if config.TurnstileSiteKey == "" {
		return "", errors.New("guard config: no turnstile site key")
	}
	p.mu.Lock()
	p.siteKey = config.TurnstileSiteKey
	p.mu.Unlock()
	return config.TurnstileSiteKey, nil
}

// CancelOrder 实现 provider.Canceler：取消未付款订单。
func (p *Provider) CancelOrder(ctx context.Context, env provider.Env, result domain.OrderResult) error {
	if result.OrderID == "" {
		return errors.New("order has no order id")
	}
	if err := p.call(ctx, env, http.MethodPost, "/api/orders/"+result.OrderID+"/cancel", map[string]any{}, nil); err != nil {
		return fmt.Errorf("cancel order %s: %w", result.OrderID, rejected(err))
	}
	return nil
}

// ---- transport ----

// apiError 站点接口的错误响应：{"error"|"detail": "...", "code": "..."}。
type apiError struct {
	status  int
	message string
	code    string
}

func (e *apiError) Error() string {
	text := fmt.Sprintf("http %d: %s", e.status, e.message)
	if e.code != "" {
		text += " (" + e.code + ")"
	}
	return text
}

// rejected 4xx 业务拒绝（参数、风控 403/428、条款版本）包装为 ErrOrderRejected，不重试；429 与 5xx 原样返回。
func rejected(err error) error {
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.status >= 400 && apiErr.status < 500 && apiErr.status != http.StatusTooManyRequests {
		return fmt.Errorf("%w: %w", provider.ErrOrderRejected, err)
	}
	return err
}

// call 发 JSON 请求（与站点前端同样的头），401 视为会话失效；out 非空时解码响应。
func (p *Provider) call(ctx context.Context, env provider.Env, method, path string, body any, out any) error {
	header := http.Header{"Accept": {"application/json"}, "X-Blossom": {"1"}, "Origin": {p.baseURL}, "Referer": {p.baseURL + "/"}}
	req := &fetch.Request{Method: method, URL: p.baseURL + path, Header: header}
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		req.JSON = encoded
	}
	resp, err := env.Client.Do(ctx, env.Session, req)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized && path != "/auth/email/login" {
		return provider.ErrSessionExpired
	}
	if resp.StatusCode >= http.StatusBadRequest {
		var payload struct {
			Error  string `json:"error"`
			Detail any    `json:"detail"`
			Code   string `json:"code"`
		}
		_ = json.Unmarshal(resp.Body, &payload)
		message := payload.Error
		if message == "" && payload.Detail != nil {
			message = fmt.Sprint(payload.Detail)
		}
		if message == "" {
			message = strings.TrimSpace(string(resp.Body[:min(len(resp.Body), 200)]))
		}
		return &apiError{status: resp.StatusCode, message: message, code: payload.Code}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func usd(amount float64) domain.Money {
	return domain.Money{Cents: int64(math.Round(amount * 100)), Currency: "USD"}
}

func nonEmpty(parts ...string) []string {
	out := parts[:0:0]
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
