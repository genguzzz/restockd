package htmlutil

import (
	"testing"

	"github.com/genguzzz/restockd/internal/domain"
)

func TestParseMoney(t *testing.T) {
	cases := map[string]domain.Money{
		"¥1000.00CNY":   {Cents: 100000, Currency: "CNY"},
		"¥149.00CNY":    {Cents: 14900, Currency: "CNY"},
		"$1,234.50 USD": {Cents: 123450, Currency: "USD"},
		"$19.99 CAD":    {Cents: 1999, Currency: "CAD"},
		"12000 JPY":     {Cents: 1200000, Currency: "JPY"},
	}
	for text, want := range cases {
		got, ok := ParseMoney(text, "USD")
		if !ok || got != want {
			t.Errorf("ParseMoney(%q) = %+v, %v; want %+v", text, got, ok, want)
		}
	}
}
