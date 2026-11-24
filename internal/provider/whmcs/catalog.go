// Package whmcs 通用 WHMCS 适配器：解析商品组页面（Lagom / standard_cart / twenty-one 主题）获取库存与价格，
// 按标准购物车流程（add → confproduct → 优惠码 → checkout）锁单，生成未支付订单与账单。
package whmcs

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/provider/htmlutil"
	"github.com/genguzzz/restockd/internal/solver"
)

// Kind 注册名。
const Kind = "whmcs"

const (
	configGroups   = "groups"
	configCurrency = "currency"
	configLocation = "location"
)

func init() {
	provider.Register(meta(), New)
}

func meta() provider.Meta {
	return provider.Meta{
		Kind:            Kind,
		Name:            "WHMCS",
		Description:     "通用 WHMCS 站点（Lagom / standard_cart / twenty-one 主题），按商品组页面监控，购物车流程锁单",
		Caps:            provider.Capabilities{Catalog: true, Coupon: true, Order: true, SessionCheck: true, Login: true},
		MinInterval:     20 * time.Second,
		DefaultInterval: 60 * time.Second,
		DefaultFetch:    domain.FetchFlareSolverr,
		Captcha:         []solver.Kind{solver.KindCloudflare, solver.KindRecaptchaV2, solver.KindTurnstile},
		ConfigFields: []provider.Field{
			{Key: configGroups, Label: "商品组", Type: provider.FieldList, Required: true, Placeholder: "cox-isp-vps", Help: "store/<组名> 中的组名或 cart.php?gid=<数字>，逗号分隔"},
			{Key: configCurrency, Label: "默认货币", Type: provider.FieldText, Default: "USD", Help: "价格只写 $ 时按此货币解析"},
			{Key: configLocation, Label: "机房位置", Type: provider.FieldText, Help: "页面未标注时显示的位置"},
		},
	}
}

// Provider WHMCS 实现。
type Provider struct {
	baseURL  string
	groups   []string
	currency string
	location string
}

// New 构造实例。
func New(site domain.Site) (provider.Provider, error) {
	groups := splitList(site.Config[configGroups])
	if len(groups) == 0 {
		return nil, fmt.Errorf("whmcs: config %q required", configGroups)
	}
	currency := site.Config[configCurrency]
	if currency == "" {
		currency = "USD"
	}
	return &Provider{baseURL: strings.TrimRight(site.BaseURL, "/"), groups: groups, currency: currency, location: site.Config[configLocation]}, nil
}

func splitList(value string) []string {
	var out []string
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' }) {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// Meta 实现 provider.Provider。
func (p *Provider) Meta() provider.Meta { return meta() }

// GroupURL 商品组页面地址。
func (p *Provider) GroupURL(group string) string {
	if _, err := strconv.Atoi(group); err == nil {
		return p.baseURL + "/cart.php?gid=" + group
	}
	return p.baseURL + "/store/" + group
}

// Catalog 逐组抓取并合并。
func (p *Provider) Catalog(ctx context.Context, env provider.Env) (domain.Catalog, error) {
	var catalog domain.Catalog
	for _, group := range p.groups {
		pageURL := p.GroupURL(group)
		resp, err := env.Client.Do(ctx, env.Session, &fetch.Request{Method: http.MethodGet, URL: pageURL})
		if err != nil {
			return domain.Catalog{}, err
		}
		if resp.StatusCode != http.StatusOK {
			return domain.Catalog{}, &fetch.HTTPError{StatusCode: resp.StatusCode, URL: pageURL}
		}
		products, err := p.ParseGroup(resp.Body, group, pageURL)
		if err != nil {
			return domain.Catalog{}, fmt.Errorf("group %s: %w", group, err)
		}
		catalog.Products = append(catalog.Products, products...)
	}
	return catalog, nil
}

var productIDPattern = regexp.MustCompile(`^product(\d+)$`)

// ParseGroup 解析一个商品组页面。
func (p *Provider) ParseGroup(body []byte, group, pageURL string) ([]domain.Product, error) {
	doc, err := htmlutil.Parse(body)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var products []domain.Product
	doc.Find(`[id^="product"]`).Each(func(_ int, node *goquery.Selection) {
		match := productIDPattern.FindStringSubmatch(node.AttrOr("id", ""))
		if match == nil {
			return
		}
		product := domain.Product{ID: match[1], Group: group, FetchedAt: now, URL: pageURL}
		product.Name = htmlutil.CleanText(node.Find(".package-title, .product-title, header span, h3, h4").First().Text())
		product.Prices = p.parsePrices(node)
		product.Specs = parseSpecs(node)
		if product.Specs.Location == "" {
			product.Specs.Location = p.location
		}
		product.Stock = parseStock(node)
		if href := node.Find("a.btn-order-now").First().AttrOr("href", ""); href != "" && !strings.HasPrefix(href, "#") {
			product.URL = htmlutil.Resolve(pageURL, href)
		}
		if product.Name != "" {
			products = append(products, product)
		}
	})
	if len(products) == 0 && doc.Find("form#frmProductDomain, #order-standard_cart, .products, .packages").Length() == 0 {
		return nil, fmt.Errorf("no products found on page")
	}
	return products, nil
}

func (p *Provider) parsePrices(node *goquery.Selection) []domain.Price {
	amountText := node.Find(".price-amount, .price span.price, .product-pricing .price").First().Text()
	if strings.TrimSpace(amountText) == "" {
		amountText = node.Find(".product-pricing, .price").First().Text()
	}
	amount, ok := htmlutil.ParseMoney(amountText, p.currency)
	if !ok {
		return nil
	}
	cycleText := node.Find(".price-cycle, .billing-cycle, .product-pricing").First().Text()
	cycle := htmlutil.ParseCycle(cycleText)
	if cycle == "" {
		cycle = domain.CycleMonthly
	}
	return []domain.Price{{Cycle: cycle, Amount: amount, Setup: domain.Money{Currency: amount.Currency}}}
}

var (
	ramPattern       = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(MB|GB|G|M)\b.*\b(RAM|Memory|内存)`)
	diskPattern      = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(GB|TB|G|T)\b.*(Storage|Disk|SSD|NVMe|HDD|硬盘)`)
	cpuPattern       = regexp.MustCompile(`(?i)(\d+)\s*x?\s*(vCPU|CPU|Cores?|核)`)
	bandwidthPattern = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(Mbps|Gbps)`)
	trafficPattern   = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(GB|TB)\b.*(Traffic|Transfer|流量)`)
	// 「标签：数值」写法（中文主题常见：内存：512MB、流量：1000GB、位置：日本 东京）。
	ramLabelPattern      = regexp.MustCompile(`(?i)^(?:内存|RAM|Memory)\s*[：:]\s*(\d+(?:\.\d+)?)\s*(MB|GB|G|M)\b`)
	trafficLabelPattern  = regexp.MustCompile(`(?i)^(?:流量|Traffic|Transfer|Bandwidth Quota)\s*[：:]\s*(\d+(?:\.\d+)?)\s*(GB|TB)\b`)
	locationLabelPattern = regexp.MustCompile(`(?i)^(?:位置|机房|地区|Location|Datacenter)\s*[：:]\s*(.+)$`)
	ipLabelPattern       = regexp.MustCompile(`(?i)^IP\s*[：:]\s*(.+)$`)
)

// parseSpecs 从特性列表逐行识别规格；无法归类的行进入 Extra。
func parseSpecs(node *goquery.Selection) domain.Specs {
	specs := domain.Specs{Extra: map[string]string{}}
	content, _ := node.Find(".package-content, .product-desc, .package-body").First().Html()
	extraIndex := 0
	content = linePattern.ReplaceAllString(content, "\x00")
	for _, line := range strings.Split(content, "\x00") {
		text := htmlutil.CleanText(stripTags(line))
		if text == "" {
			continue
		}
		switch {
		case specs.Location == "" && locationLabelPattern.MatchString(text):
			specs.Location = strings.TrimSpace(locationLabelPattern.FindStringSubmatch(text)[1])
		case specs.IPType == "" && ipLabelPattern.MatchString(text):
			specs.IPType = strings.TrimSpace(ipLabelPattern.FindStringSubmatch(text)[1])
		case specs.RAMMB == 0 && ramLabelPattern.MatchString(text):
			match := ramLabelPattern.FindStringSubmatch(text)
			specs.RAMMB = sizeMB(match[1], match[2])
		case specs.TrafficGB == 0 && trafficLabelPattern.MatchString(text):
			match := trafficLabelPattern.FindStringSubmatch(text)
			specs.TrafficGB = sizeGB([]string{text, match[1], match[2]})
		case specs.TrafficGB == 0 && trafficPattern.MatchString(text):
			specs.TrafficGB = sizeGB(trafficPattern.FindStringSubmatch(text))
		case specs.RAMMB == 0 && ramPattern.MatchString(text):
			match := ramPattern.FindStringSubmatch(text)
			specs.RAMMB = sizeMB(match[1], match[2])
		case specs.DiskGB == 0 && diskPattern.MatchString(text):
			specs.DiskGB = sizeGB(diskPattern.FindStringSubmatch(text))
		case specs.CPU == "" && cpuPattern.MatchString(text):
			specs.CPU = cpuPattern.FindStringSubmatch(text)[1]
		case specs.BandwidthMbps == 0 && bandwidthPattern.MatchString(text):
			match := bandwidthPattern.FindStringSubmatch(text)
			value, _ := strconv.ParseFloat(match[1], 64)
			if strings.EqualFold(match[2], "Gbps") {
				value *= 1000
			}
			specs.BandwidthMbps = int(value)
		case specs.IPType == "" && strings.Contains(strings.ToLower(text), "ipv4"):
			specs.IPType = text
		case strings.Contains(strings.ToLower(text), "unlimited") && strings.Contains(strings.ToLower(text), "traffic"):
			specs.Extra["traffic"] = text
		default:
			extraIndex++
			specs.Extra["note"+strconv.Itoa(extraIndex)] = text
		}
	}
	if len(specs.Extra) == 0 {
		specs.Extra = nil
	}
	return specs
}

var (
	tagPattern  = regexp.MustCompile(`<[^>]*>`)
	linePattern = regexp.MustCompile(`(?i)<br\s*/?>|</?(?:li|p|div|tr)[^>]*>`)
)

func stripTags(fragment string) string {
	return htmlUnescape(tagPattern.ReplaceAllString(fragment, " "))
}

func htmlUnescape(text string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader("<p>" + text + "</p>"))
	if err != nil {
		return text
	}
	return doc.Find("p").Text()
}

func sizeMB(value, unit string) int {
	number, _ := strconv.ParseFloat(value, 64)
	if strings.HasPrefix(strings.ToUpper(unit), "G") {
		number *= 1024
	}
	return int(number)
}

func sizeGB(match []string) int {
	number, _ := strconv.ParseFloat(match[1], 64)
	if strings.HasPrefix(strings.ToUpper(match[2]), "T") {
		number *= 1000
	}
	return int(number)
}

var availablePattern = regexp.MustCompile(`(?i)(\d+)\s*(available|可用|剩余|in stock)`)

// parseStock 库存判断顺序：数量文本 → 缺货文本 → 下单按钮可用性 → 补货提醒链接。
func parseStock(node *goquery.Selection) domain.Stock {
	qtyText := htmlutil.CleanText(node.Find(".package-qty, .qty, .product-qty").First().Text())
	lower := strings.ToLower(qtyText)
	if match := availablePattern.FindStringSubmatch(qtyText); match != nil {
		qty, _ := strconv.Atoi(match[1])
		if qty == 0 {
			return domain.Stock{Status: domain.StockOut, Qty: &qty, Reason: qtyText}
		}
		return domain.Stock{Status: domain.StockIn, Qty: &qty}
	}
	if strings.Contains(lower, "out of stock") || strings.Contains(qtyText, "缺货") || strings.Contains(qtyText, "售罄") {
		zero := 0
		return domain.Stock{Status: domain.StockOut, Qty: &zero, Reason: qtyText}
	}
	button := node.Find("a.btn-order-now, a[href*='a=add']").First()
	if button.Length() > 0 {
		if button.HasClass("disabled") {
			return domain.Stock{Status: domain.StockOut, Reason: "order button disabled"}
		}
		return domain.Stock{Status: domain.StockIn}
	}
	if node.Find("a[href*='restock'], a[href*='notify']").Length() > 0 {
		return domain.Stock{Status: domain.StockOut, Reason: "restock notifier"}
	}
	return domain.Stock{Status: domain.StockUnknown}
}
