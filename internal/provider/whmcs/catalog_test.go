package whmcs

import (
	"os"
	"testing"

	"github.com/genguzzz/restockd/internal/domain"
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
