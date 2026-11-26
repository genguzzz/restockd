package whmcs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
)

func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	instance, err := New(domain.Site{ID: "novixlink", Kind: Kind, BaseURL: "https://novixlink.com", Config: map[string]string{"groups": "cox-isp-vps", "currency": "CAD"}})
	if err != nil {
		t.Fatal(err)
	}
	return instance.(*Provider)
}

func TestParseGroupOutOfStock(t *testing.T) {
	body, err := os.ReadFile("../../../testdata/whmcs/lagom2_store_oos.html")
	if err != nil {
		t.Fatal(err)
	}
	products, err := newTestProvider(t).ParseGroup(body, "cox-isp-vps", "https://novixlink.com/store/cox-isp-vps")
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 4 {
		t.Fatalf("want 4 products, got %d", len(products))
	}
	lite := products[0]
	if lite.ID != "41" || lite.Name != "LAS-COX Lite" {
		t.Fatalf("unexpected first product %+v", lite)
	}
	if lite.Stock.Status != domain.StockOut || lite.Stock.Qty == nil || *lite.Stock.Qty != 0 {
		t.Fatalf("want out of stock with qty 0, got %+v", lite.Stock)
	}
	if len(lite.Prices) != 1 || lite.Prices[0].Cycle != domain.CycleMonthly || lite.Prices[0].Amount != (domain.Money{Cents: 1999, Currency: "CAD"}) {
		t.Fatalf("unexpected price %+v", lite.Prices)
	}
	specs := lite.Specs
	if specs.CPU != "1" || specs.RAMMB != 768 || specs.DiskGB != 10 || specs.BandwidthMbps != 200 || specs.TrafficGB != 1000 {
		t.Fatalf("unexpected specs %+v", specs)
	}
	if specs.IPType == "" {
		t.Fatalf("ip type not parsed: %+v", specs)
	}
	ultra := products[3]
	if ultra.Specs.CPU != "4" || ultra.Specs.RAMMB != 4096 || ultra.Specs.DiskGB != 40 || ultra.Specs.TrafficGB != 8000 {
		t.Fatalf("unexpected ultra specs %+v", ultra.Specs)
	}
}

func TestParseGroupInStock(t *testing.T) {
	body, err := os.ReadFile("../../../testdata/whmcs/lagom2_store_instock.html")
	if err != nil {
		t.Fatal(err)
	}
	products, err := newTestProvider(t).ParseGroup(body, "cox-isp-vps", "https://novixlink.com/store/cox-isp-vps")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]domain.Product{}
	for _, product := range products {
		byID[product.ID] = product
	}
	lite := byID["41"]
	if lite.Stock.Status != domain.StockIn || lite.Stock.Qty == nil || *lite.Stock.Qty != 3 {
		t.Fatalf("want in stock qty 3, got %+v", lite.Stock)
	}
	if lite.URL != "https://novixlink.com/store/cox-isp-vps/las-cox-lite" {
		t.Fatalf("unexpected order url %s", lite.URL)
	}
	if byID["43"].Stock.Status != domain.StockIn || *byID["43"].Stock.Qty != 1 {
		t.Fatalf("want pro in stock qty 1, got %+v", byID["43"].Stock)
	}
	if byID["42"].Stock.Status != domain.StockOut {
		t.Fatalf("want basic out of stock, got %+v", byID["42"].Stock)
	}
}

func TestParseGroupRejectsUnrelatedPage(t *testing.T) {
	_, err := newTestProvider(t).ParseGroup([]byte("<html><body><h1>Maintenance</h1></body></html>"), "g", "https://x")
	if err == nil {
		t.Fatal("want error for page without products")
	}
}

func TestGroupURL(t *testing.T) {
	provider := newTestProvider(t)
	if got := provider.GroupURL("cox-isp-vps"); got != "https://novixlink.com/store/cox-isp-vps" {
		t.Fatalf("slug group url: %s", got)
	}
	if got := provider.GroupURL("12"); got != "https://novixlink.com/cart.php?gid=12" {
		t.Fatalf("numeric group url: %s", got)
	}
}

// AaITR：standard_cart + 自定义「库存充足 / 已经售罄」标签，规格为「标签：数值」中文写法。
func TestParseGroupStandardCartChinese(t *testing.T) {
	body, err := os.ReadFile("../../../testdata/whmcs/aaitr_store_rbn.html")
	if err != nil {
		t.Fatal(err)
	}
	instance, _ := New(domain.Site{ID: "aaitr", BaseURL: "https://www.aaitr.com", Config: map[string]string{"groups": "rbn", "currency": "CNY"}})
	products, err := instance.(*Provider).ParseGroup(body, "rbn", "https://www.aaitr.com/store/rbn")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]domain.Product{}
	for _, product := range products {
		byName[product.Name] = product
	}
	verizon, softbank := byName["美国Verizon动态NAT"], byName["日本SoftBank动态NAT"]
	if verizon.ID != "5" || verizon.Stock.Status != domain.StockOut {
		t.Fatalf("verizon should be sold out: %+v", verizon)
	}
	if softbank.ID != "4" || softbank.Stock.Status != domain.StockIn {
		t.Fatalf("softbank should be in stock: %+v", softbank)
	}
	if softbank.Prices[0].Amount != (domain.Money{Cents: 14900, Currency: "CNY"}) {
		t.Fatalf("unexpected price %+v", softbank.Prices)
	}
	specs := softbank.Specs
	if specs.CPU != "1" || specs.RAMMB != 512 || specs.DiskGB != 8 || specs.BandwidthMbps != 50 || specs.TrafficGB != 1000 {
		t.Fatalf("unexpected specs %+v", specs)
	}
	if specs.Location != "日本 东京 (普通民宅)" || !strings.Contains(specs.IPType, "SoftBank") {
		t.Fatalf("label-style location / ip not parsed: %+v", specs)
	}
}

// CloudSilk：未开库存控制的商品只有可用的下单按钮（友好 URL）；周期写「每年 / 每月」。
func TestParseGroupFriendlyURLAndChineseCycle(t *testing.T) {
	body, err := os.ReadFile("../../../testdata/whmcs/cloudsilk_store_de.html")
	if err != nil {
		t.Fatal(err)
	}
	instance, _ := New(domain.Site{ID: "cloudsilk", BaseURL: "https://cloudsilk.io", Config: map[string]string{"groups": "de", "currency": "CNY"}})
	products, err := instance.(*Provider).ParseGroup(body, "de", "https://cloudsilk.io/store/de")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]domain.Product{}
	for _, product := range products {
		byID[product.ID] = product
		if product.Stock.Status == domain.StockUnknown {
			t.Errorf("product %s stock unknown", product.ID)
		}
	}
	if a := byID["28"]; a.Stock.Status != domain.StockOut || a.Prices[0].Cycle != domain.CycleAnnually || a.Prices[0].Amount.Cents != 29999 {
		t.Fatalf("套餐A: want sold out, annual ¥299.99, got %+v %+v", a.Stock, a.Prices)
	}
	if c := byID["30"]; c.Stock.Status != domain.StockIn || c.Prices[0].Cycle != domain.CycleMonthly {
		t.Fatalf("套餐C: want in stock monthly, got %+v %+v", c.Stock, c.Prices)
	}
}

func parseCard(t *testing.T, card string) *goquery.Selection {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(card))
	if err != nil {
		t.Fatal(err)
	}
	return doc.Find("body").Children().First()
}

// 没有数量标签、按钮仍可点，但徽标写着缺货：不能报有货（否则会触发自动锁单）。
func TestParseStockOutOfStockBadge(t *testing.T) {
	for _, card := range []string{
		`<div id="product7"><span class="label label-danger">Out of Stock</span><a class="btn-order-now" href="/cart.php?a=add&pid=7">Order</a></div>`,
		`<div id="product7"><a class="btn btn-order-now" href="/store/g/x">已售罄</a></div>`,
		`<div id="product7"><span class="badge">缺货</span><a href="cart.php?a=add&pid=7">购买</a></div>`,
	} {
		stock := parseStock(parseCard(t, card))
		if stock.Status != domain.StockOut || stock.Qty == nil || *stock.Qty != 0 {
			t.Fatalf("%s: want out of stock qty 0, got %+v", card, stock)
		}
	}
	disabled := parseStock(parseCard(t, `<div id="product7"><a class="btn-order-now disabled" href="#">Order</a></div>`))
	if disabled.Status != domain.StockOut || disabled.Qty == nil || *disabled.Qty != 0 {
		t.Fatalf("disabled button must be out of stock with qty 0, got %+v", disabled)
	}
	if in := parseStock(parseCard(t, `<div id="product7"><a class="btn-order-now" href="/store/g/x">Order Now</a></div>`)); in.Status != domain.StockIn {
		t.Fatalf("enabled button without out-of-stock text means in stock, got %+v", in)
	}
}

func TestParseSpecsTable(t *testing.T) {
	card := parseCard(t, `<div id="product1"><div class="package-content"><ul>
<li>2 vCPU / 2 GB 内存</li><li>20 GB NVMe 硬盘</li><li>1 TB 流量 @ 1Gbps</li><li>Unlimited Traffic</li><li>KVM &amp; 原生 IP</li></ul></div></div>`)
	specs := parseSpecs(card)
	if specs.CPU != "2" || specs.RAMMB != 2048 || specs.DiskGB != 20 || specs.TrafficGB != 1000 || specs.BandwidthMbps != 1000 {
		t.Fatalf("every unset field must be tried on every line: %+v", specs)
	}
	if specs.Extra["traffic"] != "Unlimited Traffic" || specs.Extra["note1"] != "KVM & 原生 IP" || len(specs.Extra) != 2 {
		t.Fatalf("only unmatched lines go to extra (html unescaped): %+v", specs.Extra)
	}
}

// SixtyNet：Lagom2、「N Available」库存，未开短网址（商品链接为 index.php?rp=/store/...）。
func TestParseGroupSixtyNet(t *testing.T) {
	body, err := os.ReadFile("../../../testdata/whmcs/sixtynet_att_residential.html")
	if err != nil {
		t.Fatal(err)
	}
	instance, _ := New(domain.Site{ID: "sixtynet", BaseURL: "https://sixtynet.net", Config: map[string]string{"groups": "at-and-t-mobility-residential", "currency": "USD"}})
	products, err := instance.(*Provider).ParseGroup(body, "at-and-t-mobility-residential", "https://sixtynet.net/index.php?rp=/store/at-and-t-mobility-residential")
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 4 {
		t.Fatalf("products: %d", len(products))
	}
	starter := products[0]
	if starter.ID != "25" || starter.Name != "Residential-STARTER" || starter.Specs.RAMMB != 2048 || starter.Specs.TrafficGB != 5000 ||
		starter.Stock.Status != domain.StockOut || starter.Prices[0].Amount.Cents != 2500 ||
		starter.URL != "https://sixtynet.net/index.php?rp=/store/at-and-t-mobility-residential/att-moble-starter" {
		t.Fatalf("starter: %+v", starter)
	}
}

func TestCatalogFallsBackToRouteParam(t *testing.T) {
	body, err := os.ReadFile("../../../testdata/whmcs/sixtynet_att_residential.html")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		if r.URL.Path == "/index.php" && r.URL.Query().Get("rp") == "/store/at-and-t-mobility-residential" {
			_, _ = w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	site := domain.Site{ID: "sixtynet", BaseURL: server.URL, Config: map[string]string{"groups": "at-and-t-mobility-residential"}}
	instance, _ := New(site)
	client, _ := fetch.NewHTTPClient(5*time.Second, "")
	session, _ := fetch.NewSession(server.URL, nil, "")
	env := provider.Env{Site: site, Client: client, Session: session}
	for range 2 {
		catalog, err := instance.Catalog(context.Background(), env)
		if err != nil || len(catalog.Products) != 4 {
			t.Fatalf("catalog: %v %d", err, len(catalog.Products))
		}
	}
	// 第一次 /store/ 404 后切到 rp=，第二次直接用 rp=。
	if len(paths) != 3 || paths[0] != "/store/at-and-t-mobility-residential" || paths[2] != "/index.php?rp=/store/at-and-t-mobility-residential" {
		t.Fatalf("requests: %v", paths)
	}
}
