package store

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestAccountCookiesRoundTrip(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	account := domain.Account{ID: "a", SiteID: "s", Label: "x", Cookies: []*http.Cookie{{Name: "sid", Value: "v", Path: "/", Expires: expires, HttpOnly: true}}}
	if err := st.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetAccount(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cookies) != 1 || got.Cookies[0].Value != "v" || !got.Cookies[0].HttpOnly || !got.Cookies[0].Expires.Equal(expires) {
		t.Fatalf("cookie not preserved: %+v", got.Cookies[0])
	}
	if len(got.CookieNames) != 1 || got.CookieNames[0] != "sid" {
		t.Fatalf("names %v", got.CookieNames)
	}
	if _, err := st.GetAccount(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestReplaceProductsAndCascadeDelete(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	_ = st.SaveSite(ctx, domain.Site{ID: "s"})
	_ = st.ReplaceProducts(ctx, "s", []domain.Product{{ID: "10"}, {ID: "9"}, {ID: "100"}})
	products, _ := st.ListProducts(ctx, "s")
	if len(products) != 3 || products[0].ID != "9" || products[2].ID != "100" || products[0].SiteID != "s" {
		t.Fatalf("numeric order / site id wrong: %+v", products)
	}
	_ = st.ReplaceProducts(ctx, "s", []domain.Product{{ID: "10"}})
	if products, _ = st.ListProducts(ctx, "s"); len(products) != 1 {
		t.Fatalf("delisted products not removed: %d", len(products))
	}
	_ = st.SaveWatch(ctx, domain.Watch{ID: "w", SiteID: "s"})
	_ = st.SaveAccount(ctx, domain.Account{ID: "a", SiteID: "s"})
	if err := st.DeleteSite(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	watches, _ := st.ListWatches(ctx, "")
	accounts, _ := st.ListAccounts(ctx, "")
	products, _ = st.ListProducts(ctx, "")
	if len(watches)+len(accounts)+len(products) != 0 {
		t.Fatal("cascade delete incomplete")
	}
}

func TestCountPlacedSinceIgnoresDryRunAndFailures(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i, status := range []domain.OrderStatus{domain.OrderPlaced, domain.OrderDryRun, domain.OrderFailed, domain.OrderPlaced} {
		_ = st.SaveOrder(ctx, domain.Order{ID: string(rune('a' + i)), SiteID: "s", Status: status, CreatedAt: now})
	}
	_ = st.SaveOrder(ctx, domain.Order{ID: "old", SiteID: "s", Status: domain.OrderPlaced, CreatedAt: now.Add(-48 * time.Hour)})
	count, err := st.CountPlacedSince(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("want 2 placed in window, got %d", count)
	}
}
