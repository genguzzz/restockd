package htmlutil

import (
	"testing"

	"github.com/genguzzz/restockd/internal/domain"
)

func TestParseMoney(t *testing.T) {
	cases := map[string]domain.Money{
		"¥1000.00CNY":   {Cents: 100000, Currency: "CNY"},
		"$1,234.50 USD": {Cents: 123450, Currency: "USD"},
		"$19.99 CAD":    {Cents: 1999, Currency: "CAD"},
		"12000 JPY":     {Cents: 1200000, Currency: "JPY"},
		"$22.36USD":     {Cents: 2236, Currency: "USD"},
		"¥149.00CNY":    {Cents: 14900, Currency: "CNY"},
	}
	for text, want := range cases {
		got, ok := ParseMoney(text, "USD")
		if !ok || got != want {
			t.Errorf("ParseMoney(%q) = %+v, %v; want %+v", text, got, ok, want)
		}
	}
}

func TestParseCycleChinese(t *testing.T) {
	cases := map[string]domain.BillingCycle{"每年": domain.CycleAnnually, "月缴": domain.CycleMonthly, "每月": domain.CycleMonthly, "每季": domain.CycleQuarterly, "每半年": domain.CycleSemiannually, "两年": domain.CycleBiennially, "Monthly": domain.CycleMonthly}
	for text, want := range cases {
		if got := ParseCycle(text); got != want {
			t.Errorf("ParseCycle(%q) = %q, want %q", text, got, want)
		}
	}
}
