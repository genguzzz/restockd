package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
)

// 在售期（商品补货历史）。一行是一段「补货 → 售罄」，ended_at 为空表示在售中；
// 由轮询的 TrackStock 维护，比事件表保留更久（事件 30 天就清理）。

const stockWindowColumns = `id, site_id, product_id, started_at, last_seen_at, ended_at, baseline, qty, price`

func migrateStockWindows(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS stock_windows (
	id INTEGER PRIMARY KEY AUTOINCREMENT, site_id TEXT NOT NULL, product_id TEXT NOT NULL,
	started_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL, ended_at INTEGER,
	baseline INTEGER NOT NULL DEFAULT 0, qty INTEGER, price TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS idx_stock_windows_product ON stock_windows(site_id, product_id, started_at);
CREATE INDEX IF NOT EXISTS idx_stock_windows_open ON stock_windows(site_id) WHERE ended_at IS NULL`); err != nil {
		return err
	}
	// 回填：按商品把补货与其后第一条售罄配对；最后一条未配对的补货只在商品当前仍可下单时保留为在售中。
	rows, err := tx.Query(`SELECT e.site_id, e.product_id, e.type, e.created_at, json_extract(p.data, '$.stock.status')
FROM events e LEFT JOIN products p ON p.site_id=e.site_id AND p.id=e.product_id
WHERE e.type IN (?, ?) AND e.product_id!='' ORDER BY e.site_id, e.product_id, e.created_at, e.id`, string(domain.EventRestock), string(domain.EventSoldOut))
	if err != nil {
		return err
	}
	type pending struct {
		siteID, productID string
		started           int64
		orderable         bool
	}
	var windows [][4]any // site, product, started, ended(nil = 在售中)
	var open *pending
	flush := func() {
		if open != nil && open.orderable {
			windows = append(windows, [4]any{open.siteID, open.productID, open.started, nil})
		}
		open = nil
	}
	for rows.Next() {
		var siteID, productID, kind string
		var at int64
		var status sql.NullString
		if err := rows.Scan(&siteID, &productID, &kind, &at, &status); err != nil {
			rows.Close()
			return err
		}
		if open != nil && (open.siteID != siteID || open.productID != productID) {
			flush()
		}
		switch domain.EventType(kind) {
		case domain.EventRestock:
			if open == nil {
				open = &pending{siteID: siteID, productID: productID, started: at, orderable: domain.StockStatus(status.String).Orderable()}
			}
		case domain.EventSoldOut:
			if open != nil {
				windows = append(windows, [4]any{siteID, productID, open.started, at})
				open = nil
			}
		}
	}
	flush()
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, w := range windows {
		lastSeen := w[2]
		if w[3] != nil {
			lastSeen = w[3]
		}
		if _, err := tx.Exec(`INSERT INTO stock_windows(site_id, product_id, started_at, last_seen_at, ended_at) VALUES(?, ?, ?, ?, ?)`, w[0], w[1], w[2], lastSeen, w[3]); err != nil {
			return err
		}
	}
	return nil
}

// StockChanges TrackStock 的结果，按商品 ID：Closed 本次结束的在售期；Previous 本次补货的商品上一段已结束的在售期。
type StockChanges struct {
	Closed   map[string]domain.StockWindow
	Previous map[string]domain.StockWindow
}

// TrackStock 用一次完整目录快照维护站点的在售期：可下单且没有在售期的开一段（restocked 里没有的记为 Baseline，
// 即不是本次观测到的补货），仍可下单的更新 last_seen，不再可下单或已下架的在 now 结束。
func (s *Store) TrackStock(ctx context.Context, siteID string, products []domain.Product, restocked map[string]bool, now time.Time) (StockChanges, error) {
	changes := StockChanges{Closed: map[string]domain.StockWindow{}, Previous: map[string]domain.StockWindow{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return changes, err
	}
	defer tx.Rollback()
	openWindows, err := queryStockWindows(ctx, tx, `SELECT `+stockWindowColumns+` FROM stock_windows WHERE site_id=? AND ended_at IS NULL`, siteID)
	if err != nil {
		return changes, err
	}
	open := make(map[string]domain.StockWindow, len(openWindows))
	for _, window := range openWindows {
		if _, dup := open[window.ProductID]; dup {
			// 不应出现；保险起见只保留最早一段，其余直接结束。
			if _, err := tx.ExecContext(ctx, `UPDATE stock_windows SET ended_at=last_seen_at WHERE id=?`, window.ID); err != nil {
				return changes, err
			}
			continue
		}
		open[window.ProductID] = window
	}
	at := now.UnixMilli()
	orderable := map[string]bool{}
	for _, product := range products {
		if !product.Stock.Status.Orderable() {
			continue
		}
		orderable[product.ID] = true
		if window, ok := open[product.ID]; ok {
			if _, err := tx.ExecContext(ctx, `UPDATE stock_windows SET last_seen_at=? WHERE id=?`, at, window.ID); err != nil {
				return changes, err
			}
			continue
		}
		if restocked[product.ID] {
			previous, err := queryStockWindows(ctx, tx, `SELECT `+stockWindowColumns+` FROM stock_windows WHERE site_id=? AND product_id=? AND ended_at IS NOT NULL ORDER BY started_at DESC LIMIT 1`, siteID, product.ID)
			if err != nil {
				return changes, err
			}
			if len(previous) > 0 {
				changes.Previous[product.ID] = previous[0]
			}
		}
		var qty any
		if product.Stock.Qty != nil {
			qty = *product.Stock.Qty
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO stock_windows(site_id, product_id, started_at, last_seen_at, baseline, qty, price) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			siteID, product.ID, at, at, !restocked[product.ID], qty, product.PriceSummary()); err != nil {
			return changes, err
		}
	}
	for productID, window := range open {
		if orderable[productID] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE stock_windows SET ended_at=? WHERE id=?`, at, window.ID); err != nil {
			return changes, err
		}
		ended := now.UTC()
		window.EndedAt = &ended
		changes.Closed[productID] = window
	}
	return changes, tx.Commit()
}

// StockWindows 商品的在售期，最新在前。
func (s *Store) StockWindows(ctx context.Context, siteID, productID string, limit int) ([]domain.StockWindow, error) {
	return queryStockWindows(ctx, s.db, `SELECT `+stockWindowColumns+` FROM stock_windows WHERE site_id=? AND product_id=? ORDER BY started_at DESC, id DESC LIMIT ?`, siteID, productID, clampLimit(limit))
}

// StockHistories 一次查询得到各商品的补货概况；siteID 为空时覆盖全部站点。
func (s *Store) StockHistories(ctx context.Context, siteID string, now time.Time) (map[ProductKey]domain.StockHistory, error) {
	// 每个商品取在售中的一段与最近一段已结束的，外加 30 天内的非 Baseline 补货数。
	windows, err := queryStockWindows(ctx, s.db, `SELECT `+stockWindowColumns+` FROM (
	SELECT *, ROW_NUMBER() OVER (PARTITION BY site_id, product_id, ended_at IS NULL ORDER BY started_at DESC, id DESC) AS rn
	FROM stock_windows WHERE ?='' OR site_id=?
) WHERE rn=1`, siteID, siteID)
	if err != nil {
		return nil, err
	}
	out := map[ProductKey]domain.StockHistory{}
	for _, window := range windows {
		key := ProductKey{SiteID: window.SiteID, ProductID: window.ProductID}
		history := out[key]
		if window.EndedAt == nil {
			history.Current = &window
		} else {
			history.Last = &window
		}
		out[key] = history
	}
	rows, err := s.db.QueryContext(ctx, `SELECT site_id, product_id, COUNT(*) FROM stock_windows WHERE NOT baseline AND started_at>=? AND (?='' OR site_id=?) GROUP BY site_id, product_id`,
		now.Add(-30*24*time.Hour).UnixMilli(), siteID, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key ProductKey
		var count int
		if err := rows.Scan(&key.SiteID, &key.ProductID, &count); err != nil {
			return nil, err
		}
		history := out[key]
		history.Restocks30d = count
		out[key] = history
	}
	return out, rows.Err()
}

// PruneStockWindows 删除早于 before 已结束的在售期。
func (s *Store) PruneStockWindows(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM stock_windows WHERE ended_at IS NOT NULL AND ended_at<?`, before.UnixMilli())
	return err
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func queryStockWindows(ctx context.Context, db querier, query string, args ...any) ([]domain.StockWindow, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.StockWindow
	for rows.Next() {
		var window domain.StockWindow
		var started, lastSeen int64
		var ended, qty sql.NullInt64
		if err := rows.Scan(&window.ID, &window.SiteID, &window.ProductID, &started, &lastSeen, &ended, &window.Baseline, &qty, &window.Price); err != nil {
			return nil, err
		}
		window.StartedAt = time.UnixMilli(started).UTC()
		window.LastSeenAt = time.UnixMilli(lastSeen).UTC()
		if ended.Valid {
			at := time.UnixMilli(ended.Int64).UTC()
			window.EndedAt = &at
		}
		if qty.Valid {
			value := int(qty.Int64)
			window.Qty = &value
		}
		out = append(out, window)
	}
	return out, rows.Err()
}
