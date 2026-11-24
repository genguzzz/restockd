package usvps24

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
)

func loadProps(t *testing.T, path string) (*pageData, *checkoutProps) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := ParsePage(body)
	if err != nil {
		t.Fatal(err)
	}
	var props checkoutProps
	if err := json.Unmarshal(data.Props, &props); err != nil {
		t.Fatal(err)
	}
	return data, &props
}

func TestParsePageHTMLAndJSONAgree(t *testing.T) {
	htmlData, htmlProps := loadProps(t, "../../../testdata/usvps24/orders_create.html")
	jsonData, jsonProps := loadProps(t, "../../../testdata/usvps24/orders_create.json")
	if htmlData.Version == "" || htmlData.Version != jsonData.Version {
		t.Fatalf("version mismatch %q vs %q", htmlData.Version, jsonData.Version)
	}
	if len(htmlProps.Products) != 11 || len(jsonProps.Products) != 11 {
		t.Fatalf("want 11 products, got %d / %d", len(htmlProps.Products), len(jsonProps.Products))
	}
	if htmlProps.TurnstileSiteKey == "" {
		t.Fatal("turnstile site key expected")
	}
}

func loadStock(t *testing.T) *stockResponse {
	t.Helper()
	raw, err := os.ReadFile("../../../testdata/usvps24/stock.json")
	if err != nil {
		t.Fatal(err)
	}
	var stock stockResponse
	if err := json.Unmarshal(raw, &stock); err != nil {
		t.Fatal(err)
	}
	return &stock
}

// 有下单页缓存时，库存接口的商品补全周期价、配置项、优惠标记、数字 ID，目录带上支付方式与促销。
func TestStockCatalogEnrichedFromCachedOrderPage(t *testing.T) {
	_, props := loadProps(t, "../../../testdata/usvps24/orders_create.json")
	instance, _ := New(domain.Site{ID: "usvps24", BaseURL: "https://usvps24.com"})
	p := instance.(*Provider)
	p.props = props
	catalog := p.stockCatalog(loadStock(t))
	if catalog.Promo == nil || catalog.Promo.Code != "bf2026" || catalog.Promo.Percent != 20 {
		t.Fatalf("unexpected promo %+v", catalog.Promo)
	}
	if len(catalog.Gateways) != 2 {
		t.Fatalf("unexpected gateways %v", catalog.Gateways)
	}
	byID := map[string]domain.Product{}
	for _, product := range catalog.Products {
		byID[product.ID] = product
	}
	pinnacle := byID["pinnacle-network-2"]
	monthly, ok := pinnacle.PriceFor(domain.CycleMonthly)
	if !ok || monthly.Amount != (domain.Money{Cents: 18800, Currency: "CNY"}) || monthly.RefID != "77" {
		t.Fatalf("unexpected monthly price %+v", monthly)
	}
	if len(pinnacle.Prices) < 2 || pinnacle.Specs.Extra["product_id"] != "21" || pinnacle.Specs.Extra["slug"] != "pinnacle-network-2" {
		t.Fatalf("cycles / ids not enriched: %+v", pinnacle)
	}
	if len(pinnacle.Options) != 2 || pinnacle.Options[0].Key != "bandwidth_mbps" || pinnacle.Options[0].Choices[1].DeltaMonthly.Cents != 8800 {
		t.Fatalf("unexpected options %+v", pinnacle.Options)
	}
	if pinnacle.Stock.Status != domain.StockIn || *pinnacle.Stock.Qty != 12 {
		t.Fatalf("stock must come from the stock api: %+v", pinnacle.Stock)
	}
	if !byID["pro-gw-mini-pool"].NoDiscount {
		t.Fatal("no_discount flag lost")
	}
}

func TestEmptyNameFallsBackToSlug(t *testing.T) {
	instance, _ := New(domain.Site{ID: "usvps24"})
	product := instance.(*Provider).planProduct(stockPlan{Slug: "pinnacle-network-2", Name: "  "}, time.Now())
	if product.Name != "pinnacle-network-2" || product.URL != "https://usvps24.com/plans/pinnacle-network-2" {
		t.Fatalf("want slug fallback, got %q %q", product.Name, product.URL)
	}
}

func TestCatalogFilter(t *testing.T) {
	instance, _ := New(domain.Site{ID: "usvps24", Config: map[string]string{"products": "pinnacle-network-2,\nhome-pool"}})
	catalog := instance.(*Provider).stockCatalog(loadStock(t))
	if len(catalog.Products) != 2 {
		t.Fatalf("want 2 filtered products, got %d", len(catalog.Products))
	}
}

func TestLaravelErrors(t *testing.T) {
	got := laravelErrors(map[string]any{"z": "last", "a": []any{"first", ""}, "m": ""})
	if len(got) != 2 || got[0] != "first" || got[1] != "last" || first(nil) != "" {
		t.Fatalf("want sorted non-empty messages, got %v", got)
	}
}

func TestSubtotalCents(t *testing.T) {
	_, props := loadProps(t, "../../../testdata/usvps24/orders_create.json")
	product, err := findProduct(props, "21")
	if err != nil {
		t.Fatal(err)
	}
	spec := domain.OrderSpec{ProductID: "21", Cycle: domain.CycleQuarterly, Options: map[string]string{"bandwidth_mbps": "300"}}
	if got, want := subtotalCents(product, spec, 2), int64((53580+8800*3)*2); got != want {
		t.Fatalf("subtotal %d want %d", got, want)
	}
}

func TestStockCatalogMapping(t *testing.T) {
	instance, _ := New(domain.Site{ID: "usvps24", BaseURL: "https://usvps24.com"})
	catalog := instance.(*Provider).stockCatalog(loadStock(t))
	byID := map[string]domain.Product{}
	for _, product := range catalog.Products {
		byID[product.ID] = product
	}
	if len(byID) != 11 {
		t.Fatalf("want 11 products, got %d", len(byID))
	}
	pinnacle := byID["pinnacle-network-2"]
	if pinnacle.Stock.Status != domain.StockIn || pinnacle.Stock.Reason != "invite_only" || *pinnacle.Stock.Qty != 12 {
		t.Fatalf("invite-only with stock should be in stock + reason, got %+v", pinnacle.Stock)
	}
	monthly, ok := pinnacle.PriceFor(domain.CycleMonthly)
	if !ok || monthly.Amount != (domain.Money{Cents: 18800, Currency: "CNY"}) {
		t.Fatalf("unexpected monthly price %+v", monthly)
	}
	if home := byID["home-pool"]; home.Stock.Status != domain.StockOut || home.Stock.Reason != "sold_out" {
		t.Fatalf("want sold out, got %+v", home.Stock)
	}
	if inv := byID["pinnacle-network"]; inv.Stock.Status != domain.StockOut || inv.Stock.Reason != "invite_only" {
		t.Fatalf("invite-only without stock should be out, got %+v", inv.Stock)
	}
	if byID["att-dedicated-1"].Specs.Extra["carrier"] != "AT&T" || byID["att-dedicated-1"].Specs.TrafficGB != 4000 {
		t.Fatalf("specs lost: %+v", byID["att-dedicated-1"].Specs)
	}
	for status, want := range map[string]domain.StockStatus{"in_stock": domain.StockIn, "preorder": domain.StockPreorder, "coming_soon": domain.StockClosed} {
		if got := planStock(stockPlan{Status: status, Stock: 3}).Status; got != want {
			t.Fatalf("%s: want %s, got %s", status, want, got)
		}
	}
}
