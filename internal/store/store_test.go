package store

import (
	"context"
	"database/sql"
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

func TestMigrateAddsSecretsColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// v0 表结构：accounts 没有 secrets 列，orders 没有 product_id / account_id 列。
	if _, err := old.Exec(`CREATE TABLE accounts (id TEXT PRIMARY KEY, site_id TEXT NOT NULL, data TEXT NOT NULL, cookies TEXT NOT NULL DEFAULT '[]');
INSERT INTO accounts(id, site_id, data, cookies) VALUES('a1', 's1', '{"id":"a1","site_id":"s1","label":"old"}', '[{"name":"sid","value":"v"}]');
CREATE TABLE orders (id TEXT PRIMARY KEY, watch_id TEXT NOT NULL DEFAULT '', site_id TEXT NOT NULL, created_at INTEGER NOT NULL, data TEXT NOT NULL);
INSERT INTO orders(id, site_id, created_at, data) VALUES('o1', 's1', 1, '{"id":"o1","site_id":"s1","product_id":"p1","account_id":"a1","status":"placed"}');`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	ctx := context.Background()
	for i := 0; i < 2; i++ { // 第二次打开时列已存在，必须幂等
		st, err := Open(path)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		account, err := st.GetAccount(ctx, "a1")
		if err != nil || account.Label != "old" || len(account.Cookies) != 1 || (i == 0 && account.Credentials.Password != "") {
			t.Fatalf("legacy account unreadable: %v %+v", err, account)
		}
		account.Credentials = domain.Credentials{Username: "u", Password: "p"}
		if err := st.SaveAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		if latest, err := st.LatestOrders(ctx, "s1"); err != nil || latest[ProductKey{"s1", "p1"}].Last.ID != "o1" {
			t.Fatalf("legacy orders not backfilled: %v %+v", err, latest)
		}
		again, _ := st.GetAccount(ctx, "a1")
		if again.Credentials != (domain.Credentials{Username: "u", Password: "p"}) {
			t.Fatalf("credentials not persisted: %+v", again)
		}
		st.Close()
	}
}

func TestMigrationVersioned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("user_version %d %v", version, err)
	}
	st.Close()
	if st, err = Open(path); err != nil { // 已是最新版本：重开不再迁移
		t.Fatal(err)
	}
	st.Close()
}

func TestCountProducts(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	in := func(id string, status domain.StockStatus) domain.Product {
		return domain.Product{ID: id, Stock: domain.Stock{Status: status}}
	}
	_ = st.ReplaceProducts(ctx, "a", []domain.Product{in("1", domain.StockIn), in("2", domain.StockPreorder), in("3", domain.StockOut), in("4", domain.StockUnknown)})
	_ = st.ReplaceProducts(ctx, "b", []domain.Product{in("1", domain.StockIn)})
	if counts, err := st.CountProducts(ctx, "a", nil); err != nil || counts != (ProductCounts{Visible: 4, InStock: 2}) {
		t.Fatalf("site a: %+v %v", counts, err)
	}
	// 隐藏的商品不计入可见与有货；不存在的隐藏 ID 不计数。
	if counts, err := st.CountProducts(ctx, "a", []string{"1", "3", "missing"}); err != nil || counts != (ProductCounts{Visible: 2, InStock: 1, Hidden: 2}) {
		t.Fatalf("site a hidden: %+v %v", counts, err)
	}
	if counts, err := st.CountProducts(ctx, "none", nil); err != nil || counts != (ProductCounts{}) {
		t.Fatalf("empty site: %+v %v", counts, err)
	}
}

func TestPruneOrders(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_ = st.SaveOrder(ctx, domain.Order{ID: "new", SiteID: "s", CreatedAt: now})
	_ = st.SaveOrder(ctx, domain.Order{ID: "old", SiteID: "s", CreatedAt: now.Add(-100 * 24 * time.Hour)})
	if err := st.PruneOrders(ctx, now.Add(-90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if orders, _ := st.ListOrders(ctx, 10); len(orders) != 1 || orders[0].ID != "new" {
		t.Fatalf("prune orders: %+v", orders)
	}
}

func TestLatestOrdersPerProduct(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	save := func(id, site, product string, status domain.OrderStatus, age time.Duration) {
		if err := st.SaveOrder(ctx, domain.Order{ID: id, SiteID: site, ProductID: product, AccountID: "a", Status: status, CreatedAt: now.Add(-age)}); err != nil {
			t.Fatal(err)
		}
	}
	save("o1", "s", "1", domain.OrderPlaced, 3*time.Hour)
	save("o2", "s", "1", domain.OrderDryRun, time.Hour)
	save("o3", "s", "2", domain.OrderFailed, time.Hour)
	save("o4", "t", "1", domain.OrderPlaced, 30*time.Hour)
	latest, err := st.LatestOrders(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 2 || latest[ProductKey{"s", "1"}].Last.ID != "o2" || !latest[ProductKey{"s", "1"}].LastPlacedAt.Equal(now.Add(-3*time.Hour)) {
		t.Fatalf("site s: %+v", latest)
	}
	if state := latest[ProductKey{"s", "2"}]; state.Last.ID != "o3" || !state.LastPlacedAt.IsZero() {
		t.Fatalf("never placed product: %+v", state)
	}
	if all, _ := st.LatestOrders(ctx, ""); len(all) != 3 {
		t.Fatalf("all sites: %+v", all)
	}
	if order, err := st.LatestPlacedOrder(ctx, "s", "1", "a", now.Add(-24*time.Hour)); err != nil || order.ID != "o1" {
		t.Fatalf("latest placed: %v %+v", err, order)
	}
	if _, err := st.LatestPlacedOrder(ctx, "t", "1", "a", now.Add(-24*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("placed outside window must not count: %v", err)
	}
	if _, err := st.LatestPlacedOrder(ctx, "s", "1", "other", now.Add(-24*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other account must not count: %v", err)
	}
}
