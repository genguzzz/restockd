package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
)

func stocked(id string, status domain.StockStatus) domain.Product {
	return domain.Product{ID: id, Name: id, Stock: domain.Stock{Status: status}}
}

func TestTrackStockWindows(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

	// 首次快照：a 在售但不是观测到的补货 → Baseline；b 无货不开。
	if _, err := st.TrackStock(ctx, "s", []domain.Product{stocked("a", domain.StockIn), stocked("b", domain.StockOut)}, nil, t0); err != nil {
		t.Fatal(err)
	}
	// b 补货，a 仍在售（更新 last_seen）。
	if _, err := st.TrackStock(ctx, "s", []domain.Product{stocked("a", domain.StockIn), stocked("b", domain.StockIn)}, map[string]bool{"b": true}, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// a 售罄，b 下架：两段都在此刻结束。
	changes, err := st.TrackStock(ctx, "s", []domain.Product{stocked("a", domain.StockOut)}, nil, t0.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	a, b := changes.Closed["a"], changes.Closed["b"]
	if !a.Baseline || a.Duration(time.Time{}) != 3*time.Minute || !a.LastSeenAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("window a: %+v", a)
	}
	if b.Baseline || b.Duration(time.Time{}) != 2*time.Minute {
		t.Fatalf("window b: %+v", b)
	}
	// b 再次补货：Previous 给出上一段。
	changes, err = st.TrackStock(ctx, "s", []domain.Product{stocked("b", domain.StockIn)}, map[string]bool{"b": true}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if previous := changes.Previous["b"]; previous.ID != b.ID {
		t.Fatalf("previous window of b: %+v", previous)
	}
	windows, err := st.StockWindows(ctx, "s", "b", 0)
	if err != nil || len(windows) != 2 || windows[0].EndedAt != nil || windows[1].EndedAt == nil {
		t.Fatalf("history of b: %v %+v", err, windows)
	}
	histories, err := st.StockHistories(ctx, "", t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	hb := histories[ProductKey{"s", "b"}]
	if hb.Current == nil || hb.Last == nil || hb.Last.ID != b.ID || hb.Restocks30d != 2 {
		t.Fatalf("summary of b: %+v", hb)
	}
	if ha := histories[ProductKey{"s", "a"}]; ha.Current != nil || ha.Last == nil || ha.Restocks30d != 0 {
		t.Fatalf("summary of a (baseline only): %+v", ha)
	}
}

func TestMigrateBackfillsStockWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// 回到 v2：有补货 / 售罄事件但没有在售期表。p1 售罄过一次后又补货且仍在售；p2 最后一次补货没有售罄事件但已无货。
	if _, err := db.Exec(`DROP TABLE stock_windows; PRAGMA user_version=2;
INSERT INTO products(site_id, id, data) VALUES('s', 'p1', '{"stock":{"status":"in_stock"}}'), ('s', 'p2', '{"stock":{"status":"out_of_stock"}}');
INSERT INTO events(type, site_id, product_id, title, created_at) VALUES
 ('restock', 's', 'p1', '', 1000), ('sold_out', 's', 'p1', '', 7000), ('restock', 's', 'p1', '', 9000),
 ('restock', 's', 'p2', '', 2000), ('price_change', 's', 'p2', '', 3000);`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if st, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	p1, err := st.StockWindows(ctx, "s", "p1", 0)
	if err != nil || len(p1) != 2 || p1[0].EndedAt != nil || p1[1].Duration(time.Time{}) != 6*time.Second {
		t.Fatalf("p1 backfill: %v %+v", err, p1)
	}
	if p2, _ := st.StockWindows(ctx, "s", "p2", 0); len(p2) != 0 {
		t.Fatalf("p2 unpaired restock of a sold-out product should be dropped: %+v", p2)
	}
}
