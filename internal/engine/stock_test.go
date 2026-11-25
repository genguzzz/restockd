package engine

import (
	"testing"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/store"
)

func TestFormatSpan(t *testing.T) {
	for d, want := range map[time.Duration]string{
		6 * time.Second:                "6 秒",
		3*time.Minute + 20*time.Second: "3 分钟",
		2 * time.Hour:                  "2 小时",
		2*time.Hour + 5*time.Minute:    "2 小时 5 分钟",
		50 * time.Hour:                 "2 天 2 小时",
	} {
		if got := formatSpan(d); got != want {
			t.Errorf("formatSpan(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestWithStockHistory(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	ended := now.Add(-time.Hour)
	history := store.StockChanges{
		Closed:   map[string]domain.StockWindow{"a": {StartedAt: now.Add(-6 * time.Second), EndedAt: &now}},
		Previous: map[string]domain.StockWindow{"b": {StartedAt: now.Add(-49 * time.Hour), EndedAt: &ended}},
	}
	if got := withStockHistory(domain.Event{Type: domain.EventSoldOut, ProductID: "a"}, history, now); got != "在售 6 秒" {
		t.Errorf("sold out: %q", got)
	}
	if got := withStockHistory(domain.Event{Type: domain.EventRestock, ProductID: "b", Detail: "CAD 19.99 monthly"}, history, now); got != "CAD 19.99 monthly · 上次补货 2 天 1 小时前，在售 2 天" {
		t.Errorf("restock: %q", got)
	}
	if got := withStockHistory(domain.Event{Type: domain.EventRestock, ProductID: "c", Detail: "x"}, history, now); got != "x" {
		t.Errorf("first restock: %q", got)
	}
}
