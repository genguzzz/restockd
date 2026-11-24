// Package usvps24 适配 usvps24.com（Laravel + Inertia 自研计费前端）。
//
// 监控只读公开库存接口 /api/v1/stock（站点要求：不要轮询首页、套餐页、下单页，否则整个出口 IP
// 被暂停访问这些页面约 6 小时，返回 429）。下单页 /orders/create 的 Inertia 数据只在锁单 / 报价 /
// 校验会话时读取，顺带缓存周期价、配置项、系统模板，用来补全目录。
// 锁单走 /cart/checkout，需要登录会话（laravel_session + XSRF-TOKEN）与 Cloudflare Turnstile token。
package usvps24

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
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
const Kind = "usvps24"

func init() {
	provider.Register(meta(), New)
}

func meta() provider.Meta {
	return provider.Meta{
		Kind:        Kind,
		Name:        "usvps24",
		Description: "usvps24.com（Inertia 计费系统）：公开库存接口 /api/v1/stock 监控，登录会话 + Turnstile 锁单",
		Caps:        provider.Capabilities{Catalog: true, Coupon: true, Order: true, SessionCheck: true, Login: true},
		// 接口每 60 秒更新且要求间隔 ≥60 秒；调度有 ±10% 抖动，取 70 秒保证下限。
		MinInterval:     70 * time.Second,
		DefaultInterval: 70 * time.Second,
		DefaultFetch:    domain.FetchDirect,
		Captcha:         []solver.Kind{solver.KindTurnstile},
		ConfigFields: []provider.Field{
			{Key: "products", Label: "只看这些商品", Type: provider.FieldList, Placeholder: "pinnacle-network-2,home-pool", Help: "填 slug（商品 ID 即 slug），留空表示全部"},
		},
	}
}

// Provider usvps24 实现；version 缓存 Inertia 资源版本，版本不符时服务端返回 409；
// props 缓存最近一次下单页数据，用于补全库存接口没有的周期价与配置项。
type Provider struct {
	baseURL string
	only    map[string]bool
	mu      sync.RWMutex
	version string
	props   *checkoutProps
}

var (
	_ provider.Orderer        = (*Provider)(nil)
	_ provider.SessionChecker = (*Provider)(nil)
)

// New 构造实例。
func New(site domain.Site) (provider.Provider, error) {
	base := strings.TrimRight(site.BaseURL, "/")
	if base == "" {
		base = "https://usvps24.com"
	}
	only := map[string]bool{}
	for _, item := range strings.Split(site.Config["products"], ",") {
		if item = strings.TrimSpace(item); item != "" {
			only[item] = true
		}
	}
	return &Provider{baseURL: base, only: only}, nil
}

// Meta 实现 provider.Provider。
func (p *Provider) Meta() provider.Meta { return meta() }

// pageData Inertia 页面对象。
type pageData struct {
	Component string          `json:"component"`
	URL       string          `json:"url"`
	Version   string          `json:"version"`
	Props     json.RawMessage `json:"props"`
}

type checkoutProps struct {
	Errors           map[string]any `json:"errors"`
	TurnstileSiteKey string         `json:"turnstileSiteKey"`
	Promo            *rawPromo      `json:"promo"`
	Products         []rawProduct   `json:"products"`
	Gateways         []string       `json:"gateways"`
	BalanceCents     int64          `json:"balanceCents"`
	IsGuest          bool           `json:"isGuest"`
	Auth             struct {
		User *struct {
			Email string `json:"email"`
			Name  string `json:"name"`
		} `json:"user"`
	} `json:"auth"`
}

type rawPromo struct {
	Code     string `json:"code"`
	Percent  int    `json:"percent"`
	StartsAt int64  `json:"starts_at"`
	EndsAt   int64  `json:"ends_at"`
}

type rawProduct struct {
	ID                int64          `json:"id"`
	Slug              string         `json:"slug"`
	Name              string         `json:"name"`
	Region            string         `json:"region"`
	Specs             map[string]any `json:"specs"`
	ConfigOptions     optionMap      `json:"config_options"`
	NoDiscount        bool           `json:"no_discount"`
	Currency          string         `json:"currency"`
	BillingCycles     []rawCycle     `json:"billing_cycles"`
	Available         bool           `json:"available"`
	UnavailableReason string         `json:"unavailable_reason"`
	MaxQty            int            `json:"max_qty"`
	IsPreorder        bool           `json:"is_preorder"`
	ComingSoon        bool           `json:"coming_soon"`
	OSTemplates       []struct {
		Value string `json:"value"`
		Label string `json:"label"`
	} `json:"os_templates"`
}

// optionMap 兼容 PHP 空数组：无配置项时站点返回 [] 而不是 {}。
type optionMap map[string][]rawOptChoice

// UnmarshalJSON 实现 json.Unmarshaler。
func (m *optionMap) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "[]" || trimmed == "null" {
		*m = optionMap{}
		return nil
	}
	var parsed map[string][]rawOptChoice
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	*m = parsed
	return nil
}

type rawOptChoice struct {
	Value      any   `json:"value"`
	DeltaCents int64 `json:"delta_monthly_cents"`
}

type rawCycle struct {
	ID       int64  `json:"id"`
	Period   string `json:"period"`
	Price    int64  `json:"price_cents"`
	SetupFee int64  `json:"setup_fee_cents"`
}

// stockResponse 公开库存接口 /api/v1/stock 的返回（文档：/docs/stock-api）。
type stockResponse struct {
	GeneratedAt string      `json:"generated_at"`
	Plans       []stockPlan `json:"plans"`
}

type stockPlan struct {
	Slug        string         `json:"slug"`
	Name        string         `json:"name"`
	GroupTitle  string         `json:"group_title"`
	LineLabel   string         `json:"line_label"`
	Region      string         `json:"region"`
	Currency    string         `json:"currency"`
	PriceMonth  *float64       `json:"price_monthly"`
	PriceTBD    bool           `json:"price_tbd"`
	SetupFee    *float64       `json:"setup_fee"`
	Specs       map[string]any `json:"specs"`
	Status      string         `json:"status"`
	Purchasable bool           `json:"purchasable"`
	Stock       int            `json:"stock"`
	URL         string         `json:"url"`
}

// Catalog 实现 provider.Provider：只读公开库存接口。
func (p *Provider) Catalog(ctx context.Context, env provider.Env) (domain.Catalog, error) {
	resp, err := env.Client.Do(ctx, env.Session, &fetch.Request{Method: http.MethodGet, URL: p.baseURL + "/api/v1/stock", Header: http.Header{"Accept": {"application/json"}}})
	if err != nil {
		return domain.Catalog{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return domain.Catalog{}, &fetch.HTTPError{StatusCode: resp.StatusCode, URL: resp.URL}
	}
	var stock stockResponse
	if err := json.Unmarshal(resp.Body, &stock); err != nil {
		return domain.Catalog{}, fmt.Errorf("decode stock api: %w", err)
	}
	return p.stockCatalog(&stock), nil
}

// stockCatalog 以库存接口为准（ID 用 slug），有下单页缓存时补全周期价、配置项与系统模板。
func (p *Provider) stockCatalog(stock *stockResponse) domain.Catalog {
	now := time.Now().UTC()
	catalog := domain.Catalog{}
	enriched := map[string]domain.Product{}
	if props := p.cachedProps(); props != nil {
		cached := p.toCatalog(props)
		catalog.Gateways, catalog.Promo = cached.Gateways, cached.Promo
		for _, product := range cached.Products {
			enriched[product.Specs.Extra["slug"]] = product
		}
	}
	for _, plan := range stock.Plans {
		product := p.planProduct(plan, now)
		if rich, ok := enriched[plan.Slug]; ok {
			product.Prices = rich.Prices
			product.Options = rich.Options
			product.OSTemplates = rich.OSTemplates
			product.NoDiscount = rich.NoDiscount
			product.Specs.Extra["product_id"] = rich.ID
		}
		// 只按 slug 过滤：数字 ID 依赖下单页缓存，重启后为空会把商品全部滤掉。
		if len(p.only) > 0 && !p.only[plan.Slug] {
			continue
		}
		catalog.Products = append(catalog.Products, product)
	}
	return catalog
}

func (p *Provider) planProduct(plan stockPlan, now time.Time) domain.Product {
	product := domain.Product{
		ID:        plan.Slug,
		Name:      firstNonEmpty(strings.TrimSpace(plan.Name), plan.Slug),
		URL:       firstNonEmpty(plan.URL, p.baseURL+"/plans/"+plan.Slug),
		FetchedAt: now,
		Stock:     planStock(plan),
		Specs: domain.Specs{
			CPU:           asString(plan.Specs["cpu"]),
			RAMMB:         asInt(plan.Specs["ram_mb"]),
			DiskGB:        asInt(plan.Specs["disk_gb"]),
			BandwidthMbps: asInt(plan.Specs["bandwidth_mbps"]),
			TrafficGB:     asInt(plan.Specs["traffic_gb"]),
			Location:      plan.Region,
			IPType:        asString(plan.Specs["ip_type"]),
			Extra:         map[string]string{"slug": plan.Slug},
		},
	}
	if plan.Region != "" {
		product.Specs.Extra["region"] = plan.Region
	}
	if plan.LineLabel != "" {
		product.Specs.Extra["line"] = plan.LineLabel
	}
	if carrier := asString(plan.Specs["carrier"]); carrier != "" {
		product.Specs.Extra["carrier"] = carrier
	}
	if plan.PriceMonth != nil && !plan.PriceTBD {
		setup := int64(0)
		if plan.SetupFee != nil {
			setup = yuanToCents(*plan.SetupFee)
		}
		product.Prices = []domain.Price{{Cycle: domain.CycleMonthly, Amount: domain.Money{Cents: yuanToCents(*plan.PriceMonth), Currency: plan.Currency}, Setup: domain.Money{Cents: setup, Currency: plan.Currency}}}
	}
	return product
}

func yuanToCents(value float64) int64 { return int64(value*100 + 0.5) }

// planStock 映射接口状态。invite_only 仍有余量时记为有货（会员账号可下单），Reason 标注邀请制。
func planStock(plan stockPlan) domain.Stock {
	switch plan.Status {
	case "in_stock":
		return domain.Stock{Status: domain.StockIn, Qty: positive(plan.Stock)}
	case "preorder":
		return domain.Stock{Status: domain.StockPreorder, Qty: positive(plan.Stock)}
	case "invite_only":
		if plan.Stock > 0 {
			return domain.Stock{Status: domain.StockIn, Qty: positive(plan.Stock), Reason: "invite_only"}
		}
		zero := 0
		return domain.Stock{Status: domain.StockOut, Qty: &zero, Reason: "invite_only"}
	case "coming_soon":
		return domain.Stock{Status: domain.StockClosed, Reason: "coming_soon"}
	case "sold_out":
		zero := 0
		return domain.Stock{Status: domain.StockOut, Qty: &zero, Reason: "sold_out"}
	default:
		if plan.Purchasable && plan.Stock > 0 {
			return domain.Stock{Status: domain.StockIn, Qty: positive(plan.Stock), Reason: plan.Status}
		}
		zero := 0
		return domain.Stock{Status: domain.StockOut, Qty: &zero, Reason: firstNonEmpty(plan.Status, "unavailable")}
	}
}

func (p *Provider) cachedProps() *checkoutProps {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.props
}

// loadCheckout 优先用 Inertia JSON（体积小）；版本不匹配或首次访问时解析 HTML 中的 data-page。
func (p *Provider) loadCheckout(ctx context.Context, env provider.Env) (*checkoutProps, error) {
	resp, err := env.Client.Do(ctx, env.Session, &fetch.Request{Method: http.MethodGet, URL: p.baseURL + "/orders/create", Header: p.inertiaHeader(env.Session)})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusConflict {
		p.setVersion("")
		resp, err = env.Client.Do(ctx, env.Session, &fetch.Request{Method: http.MethodGet, URL: p.baseURL + "/orders/create"})
		if err != nil {
			return nil, err
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &fetch.HTTPError{StatusCode: resp.StatusCode, URL: resp.URL}
	}
	data, err := ParsePage(resp.Body)
	if err != nil {
		return nil, err
	}
	p.setVersion(data.Version)
	var props checkoutProps
	if err := json.Unmarshal(data.Props, &props); err != nil {
		return nil, fmt.Errorf("decode props: %w", err)
	}
	if len(props.Products) > 0 {
		p.mu.Lock()
		p.props = &props
		p.mu.Unlock()
	}
	return &props, nil
}

var dataPagePattern = regexp.MustCompile(`data-page="([^"]+)"`)

// ParsePage 同时支持 Inertia JSON 响应与 HTML 首屏。
func ParsePage(body []byte) (*pageData, error) {
	trimmed := strings.TrimSpace(string(body))
	raw := trimmed
	if !strings.HasPrefix(trimmed, "{") {
		match := dataPagePattern.FindStringSubmatch(trimmed)
		if match == nil {
			return nil, errors.New("inertia data-page not found")
		}
		raw = html.UnescapeString(match[1])
	}
	var data pageData
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return nil, fmt.Errorf("decode inertia page: %w", err)
	}
	return &data, nil
}

func (p *Provider) inertiaHeader(session *fetch.Session) http.Header {
	header := http.Header{"X-Requested-With": {"XMLHttpRequest"}, "Accept": {"text/html, application/xhtml+xml"}}
	if version := p.getVersion(); version != "" {
		header.Set("X-Inertia", "true")
		header.Set("X-Inertia-Version", version)
	}
	if token := xsrfToken(session); token != "" {
		header.Set("X-XSRF-TOKEN", token)
	}
	return header
}

func xsrfToken(session *fetch.Session) string {
	raw := session.Cookie("XSRF-TOKEN")
	if decoded, err := url.QueryUnescape(raw); err == nil {
		return decoded
	}
	return raw
}

func (p *Provider) getVersion() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.version
}

func (p *Provider) setVersion(version string) {
	p.mu.Lock()
	p.version = version
	p.mu.Unlock()
}

func (p *Provider) toCatalog(props *checkoutProps) domain.Catalog {
	now := time.Now().UTC()
	catalog := domain.Catalog{Gateways: props.Gateways}
	if props.Promo != nil && props.Promo.Code != "" {
		catalog.Promo = &domain.Promo{Code: props.Promo.Code, Percent: props.Promo.Percent, StartsAt: time.UnixMilli(props.Promo.StartsAt).UTC(), EndsAt: time.UnixMilli(props.Promo.EndsAt).UTC()}
	}
	for _, raw := range props.Products {
		id := strconv.FormatInt(raw.ID, 10)
		if len(p.only) > 0 && !p.only[id] && !p.only[raw.Slug] {
			continue
		}
		catalog.Products = append(catalog.Products, p.toProduct(raw, now))
	}
	return catalog
}

var optionLabels = map[string]string{"bandwidth_mbps": "带宽 (Mbps)", "ip_count": "IP 数量"}

func (p *Provider) toProduct(raw rawProduct, now time.Time) domain.Product {
	product := domain.Product{
		ID:         strconv.FormatInt(raw.ID, 10),
		Name:       firstNonEmpty(strings.TrimSpace(raw.Name), raw.Slug),
		URL:        p.baseURL + "/plans/" + raw.Slug,
		NoDiscount: raw.NoDiscount,
		FetchedAt:  now,
		Stock:      toStock(raw),
		Specs: domain.Specs{
			CPU:           asString(raw.Specs["cpu"]),
			RAMMB:         asInt(raw.Specs["ram_mb"]),
			DiskGB:        asInt(raw.Specs["disk_gb"]),
			BandwidthMbps: asInt(raw.Specs["bandwidth_mbps"]),
			TrafficGB:     asInt(raw.Specs["traffic_gb"]),
			Location:      firstNonEmpty(asString(raw.Specs["location"]), raw.Region),
			IPType:        asString(raw.Specs["ip_type"]),
			Extra:         map[string]string{"slug": raw.Slug},
		},
	}
	if raw.Region != "" {
		product.Specs.Extra["region"] = raw.Region
	}
	if badge := asString(raw.Specs["line_badge"]); badge != "" {
		product.Specs.Extra["line"] = badge
	}
	for _, cycle := range raw.BillingCycles {
		product.Prices = append(product.Prices, domain.Price{
			Cycle:  domain.BillingCycle(cycle.Period),
			Amount: domain.Money{Cents: cycle.Price, Currency: raw.Currency},
			Setup:  domain.Money{Cents: cycle.SetupFee, Currency: raw.Currency},
			RefID:  strconv.FormatInt(cycle.ID, 10),
		})
	}
	for _, key := range []string{"bandwidth_mbps", "ip_count"} {
		choices, ok := raw.ConfigOptions[key]
		if !ok {
			continue
		}
		option := domain.ConfigOption{Key: key, Label: optionLabels[key]}
		for _, choice := range choices {
			value := asString(choice.Value)
			option.Choices = append(option.Choices, domain.Choice{Value: value, Label: value, DeltaMonthly: domain.Money{Cents: choice.DeltaCents, Currency: raw.Currency}})
		}
		product.Options = append(product.Options, option)
	}
	for _, template := range raw.OSTemplates {
		product.OSTemplates = append(product.OSTemplates, domain.Choice{Value: template.Value, Label: template.Label})
	}
	return product
}

func toStock(raw rawProduct) domain.Stock {
	switch {
	case raw.Available && raw.IsPreorder:
		return domain.Stock{Status: domain.StockPreorder, Qty: positive(raw.MaxQty)}
	case raw.Available:
		return domain.Stock{Status: domain.StockIn, Qty: positive(raw.MaxQty)}
	case raw.ComingSoon:
		return domain.Stock{Status: domain.StockClosed, Reason: "coming_soon"}
	case raw.UnavailableReason == "closed":
		return domain.Stock{Status: domain.StockClosed, Reason: "closed"}
	default:
		zero := 0
		return domain.Stock{Status: domain.StockOut, Qty: &zero, Reason: raw.UnavailableReason}
	}
}

func positive(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}

func asString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(typed)
	}
}

func asInt(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case string:
		number, _ := strconv.ParseFloat(typed, 64)
		return int(number)
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// CheckSession 已登录时 isGuest=false，并带出余额。
func (p *Provider) CheckSession(ctx context.Context, env provider.Env) (provider.SessionInfo, error) {
	props, err := p.loadCheckout(ctx, env)
	if err != nil {
		return provider.SessionInfo{}, err
	}
	if props.IsGuest {
		return provider.SessionInfo{}, provider.ErrSessionExpired
	}
	info := provider.SessionInfo{Balance: &domain.Money{Cents: props.BalanceCents, Currency: "CNY"}}
	if props.Auth.User != nil {
		info.Identity = firstNonEmpty(props.Auth.User.Email, props.Auth.User.Name)
	}
	return info, nil
}

// cartItem /cart/price 与 /cart/checkout 的条目结构（与站点前端 payload() 一致）。
type cartItem struct {
	ID        string         `json:"id"`
	ProductID int64          `json:"product_id"`
	Period    string         `json:"period"`
	Qty       int            `json:"qty"`
	Specs     map[string]any `json:"specs"`
	OS        string         `json:"os"`
	Hostname  string         `json:"hostname"`
	SSHKey    string         `json:"ssh_key"`
	PickIP    bool           `json:"pick_ip"`
}

// buildItem 用下单页里的数字 ID（目录 ID 是 slug）。
func buildItem(product rawProduct, spec domain.OrderSpec) (cartItem, error) {
	productID := product.ID
	if productID <= 0 {
		return cartItem{}, fmt.Errorf("invalid product id for %q", spec.ProductID)
	}
	qty := spec.Qty
	if qty <= 0 {
		qty = 1
	}
	specs := map[string]any{}
	for key, value := range spec.Options {
		if number, err := strconv.ParseFloat(value, 64); err == nil {
			specs[key] = number
			continue
		}
		specs[key] = value
	}
	return cartItem{ID: randomID(), ProductID: productID, Period: string(spec.Cycle), Qty: qty, Specs: specs, OS: spec.OS, Hostname: spec.Hostname, SSHKey: spec.SSHKey}, nil
}

func randomID() string {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// postJSON 发 axios 风格 JSON 请求（带 X-XSRF-TOKEN）。
func (p *Provider) postJSON(ctx context.Context, env provider.Env, path string, payload any, inertia bool) (*fetch.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	header := http.Header{"X-Requested-With": {"XMLHttpRequest"}, "Accept": {"application/json"}}
	if inertia {
		header = p.inertiaHeader(env.Session)
		header.Set("X-Inertia", "true")
		header.Set("X-Inertia-Version", p.getVersion())
	}
	if token := xsrfToken(env.Session); token != "" {
		header.Set("X-XSRF-TOKEN", token)
	}
	resp, err := env.Client.Do(ctx, env.Session, &fetch.Request{Method: http.MethodPost, URL: p.baseURL + path, JSON: body, Header: header})
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, 419:
		return nil, provider.ErrSessionExpired
	}
	return resp, nil
}

type couponPreview struct {
	Code          string              `json:"code"`
	DiscountCents int64               `json:"discount_cents"`
	Message       string              `json:"message"`
	Errors        map[string][]string `json:"errors"`
}

// Quote 校验优惠码并计算应付金额。
func (p *Provider) Quote(ctx context.Context, env provider.Env, spec domain.OrderSpec) (domain.Quote, error) {
	props, err := p.ensureSession(ctx, env)
	if err != nil {
		return domain.Quote{}, err
	}
	product, err := findProduct(props, spec.ProductID)
	if err != nil {
		return domain.Quote{}, err
	}
	return p.quote(ctx, env, product, spec)
}

func (p *Provider) quote(ctx context.Context, env provider.Env, product rawProduct, spec domain.OrderSpec) (domain.Quote, error) {
	item, err := buildItem(product, spec)
	if err != nil {
		return domain.Quote{}, err
	}
	subtotal := subtotalCents(product, spec, item.Qty)
	quote := domain.Quote{Subtotal: domain.Money{Cents: subtotal, Currency: product.Currency}, Total: domain.Money{Cents: subtotal, Currency: product.Currency}, Discount: domain.Money{Currency: product.Currency}}
	if spec.Coupon == "" {
		return quote, nil
	}
	resp, err := p.postJSON(ctx, env, "/orders/coupon/preview", map[string]any{"coupon_code": spec.Coupon, "product_id": item.ProductID, "period": item.Period, "qty": item.Qty, "specs": item.Specs}, false)
	if err != nil {
		return domain.Quote{}, err
	}
	var preview couponPreview
	_ = json.Unmarshal(resp.Body, &preview)
	if resp.StatusCode >= http.StatusBadRequest {
		quote.Message = firstNonEmpty(preview.Message, firstError(preview.Errors), fmt.Sprintf("coupon rejected (http %d)", resp.StatusCode))
		return quote, nil
	}
	quote.CouponValid = true
	quote.Discount.Cents = preview.DiscountCents
	quote.Total.Cents = subtotal - preview.DiscountCents
	return quote, nil
}

func firstError(errs map[string][]string) string {
	for _, messages := range errs {
		if len(messages) > 0 {
			return messages[0]
		}
	}
	return ""
}

// subtotalCents 本地估算：周期价 + 配置项月增量 × 周期月数，乘数量。仅用于展示与价格上限判断。
func subtotalCents(product rawProduct, spec domain.OrderSpec, qty int) int64 {
	months := map[string]int64{"monthly": 1, "quarterly": 3, "semiannually": 6, "annually": 12, "biennially": 24, "triennially": 36}
	var base int64
	for _, cycle := range product.BillingCycles {
		if cycle.Period == string(spec.Cycle) {
			base = cycle.Price + cycle.SetupFee
		}
	}
	for key, value := range spec.Options {
		for _, choice := range product.ConfigOptions[key] {
			if asString(choice.Value) == value {
				base += choice.DeltaCents * months[string(spec.Cycle)]
			}
		}
	}
	return base * int64(qty)
}

func findProduct(props *checkoutProps, productID string) (rawProduct, error) {
	for _, product := range props.Products {
		if strconv.FormatInt(product.ID, 10) == productID || product.Slug == productID {
			return product, nil
		}
	}
	return rawProduct{}, fmt.Errorf("product %s not found", productID)
}

// ensureSession 拉取页面（刷新 XSRF Cookie 与 Inertia 版本）并确认已登录。
func (p *Provider) ensureSession(ctx context.Context, env provider.Env) (*checkoutProps, error) {
	props, err := p.loadCheckout(ctx, env)
	if err != nil {
		return nil, err
	}
	if props.IsGuest {
		return nil, provider.ErrSessionExpired
	}
	return props, nil
}

var invoicePathPattern = regexp.MustCompile(`/invoices/(\d+)`)

// PlaceOrder 锁单：提交购物车结算，成功后站点跳转到账单页（未支付）。
func (p *Provider) PlaceOrder(ctx context.Context, env provider.Env, spec domain.OrderSpec) (domain.OrderResult, error) {
	props, err := p.ensureSession(ctx, env)
	if err != nil {
		return domain.OrderResult{}, err
	}
	product, err := findProduct(props, spec.ProductID)
	if err != nil {
		return domain.OrderResult{}, err
	}
	if !product.Available {
		return domain.OrderResult{}, provider.ErrOutOfStock
	}
	quote, err := p.quote(ctx, env, product, spec)
	if err != nil {
		return domain.OrderResult{}, err
	}
	if spec.Coupon != "" && !quote.CouponValid {
		return domain.OrderResult{}, fmt.Errorf("%w: %s: %s", provider.ErrCouponInvalid, spec.Coupon, quote.Message)
	}
	gateway := spec.Gateway
	if gateway == "" && len(props.Gateways) > 0 {
		gateway = props.Gateways[0]
	}
	if spec.DryRun {
		return domain.OrderResult{Total: quote.Total, Message: fmt.Sprintf("dry run: product available, gateway=%s, turnstile=%t", gateway, props.TurnstileSiteKey != "")}, nil
	}
	item, err := buildItem(product, spec)
	if err != nil {
		return domain.OrderResult{}, err
	}
	payload := map[string]any{
		"items":              []cartItem{item},
		"coupon_code":        spec.Coupon,
		"gateway":            gateway,
		"preorder_ack":       product.IsPreorder,
		"pick_ip_ack":        map[string]any{},
		"purchase_terms_ack": true,
	}
	if props.TurnstileSiteKey != "" {
		if env.Solver == nil {
			return domain.OrderResult{}, errors.New("checkout requires turnstile but no solver configured")
		}
		solution, err := env.Solver.Solve(ctx, solver.Challenge{Kind: solver.KindTurnstile, SiteKey: props.TurnstileSiteKey, PageURL: p.baseURL + "/cart", SiteID: env.Site.ID, AccountID: accountID(env), Purpose: fmt.Sprintf("%s 锁单 %s", env.Site.Name, product.Slug)})
		if err != nil {
			return domain.OrderResult{}, fmt.Errorf("solve turnstile: %w", err)
		}
		payload["cf_turnstile_response"] = solution.Token
	}
	resp, err := p.postJSON(ctx, env, "/cart/checkout", payload, true)
	if err != nil {
		return domain.OrderResult{}, err
	}
	return parseCheckoutResult(resp, p.baseURL, quote)
}

func accountID(env provider.Env) string {
	if env.Account == nil {
		return ""
	}
	return env.Account.ID
}

// parseCheckoutResult Inertia 提交后跟随重定向：落在 /invoices/{id} 为成功；回到购物车并带 errors 为失败。
func parseCheckoutResult(resp *fetch.Response, baseURL string, quote domain.Quote) (domain.OrderResult, error) {
	result := domain.OrderResult{Total: quote.Total}
	if location := resp.Header.Get("X-Inertia-Location"); location != "" {
		if match := invoicePathPattern.FindStringSubmatch(location); match != nil {
			result.InvoiceID = match[1]
			result.PayURL = location
			result.Message = "order placed, invoice unpaid"
			return result, nil
		}
		result.PayURL = location
		result.Message = "redirected to external payment page"
		return result, nil
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return result, fmt.Errorf("checkout http %d", resp.StatusCode)
	}
	data, err := ParsePage(resp.Body)
	if err != nil {
		return result, fmt.Errorf("checkout response: %w", err)
	}
	var props struct {
		Errors map[string]any `json:"errors"`
		Flash  map[string]any `json:"flash"`
	}
	_ = json.Unmarshal(data.Props, &props)
	if len(props.Errors) > 0 {
		var messages []string
		for key, value := range props.Errors {
			messages = append(messages, key+": "+asString(value))
		}
		message := strings.Join(messages, "; ")
		if strings.Contains(strings.ToLower(message), "stock") || strings.Contains(message, "售罄") || strings.Contains(message, "库存") {
			return result, fmt.Errorf("%w: %s", provider.ErrOutOfStock, message)
		}
		return result, fmt.Errorf("checkout rejected: %s", message)
	}
	if match := invoicePathPattern.FindStringSubmatch(data.URL + " " + resp.URL); match != nil {
		result.InvoiceID = match[1]
		result.PayURL = baseURL + "/invoices/" + match[1]
		result.Message = "order placed, invoice unpaid"
		return result, nil
	}
	return result, fmt.Errorf("unrecognized checkout result: component=%s url=%s", data.Component, data.URL)
}
