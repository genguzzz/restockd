// Package store SQLite 持久化（纯 Go 驱动，WAL 模式）。实体以 JSON 文档存储，按主键与常用过滤列建索引。
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/genguzzz/restockd/internal/domain"
)

// ErrNotFound 记录不存在。
var ErrNotFound = errors.New("not found")

const schema = `
CREATE TABLE IF NOT EXISTS sites (id TEXT PRIMARY KEY, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS accounts (id TEXT PRIMARY KEY, site_id TEXT NOT NULL, data TEXT NOT NULL, cookies TEXT NOT NULL DEFAULT '[]', secrets TEXT NOT NULL DEFAULT '{}');
CREATE INDEX IF NOT EXISTS idx_accounts_site ON accounts(site_id);
CREATE TABLE IF NOT EXISTS watches (id TEXT PRIMARY KEY, site_id TEXT NOT NULL, data TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_watches_site ON watches(site_id);
CREATE TABLE IF NOT EXISTS products (site_id TEXT NOT NULL, id TEXT NOT NULL, data TEXT NOT NULL, PRIMARY KEY(site_id, id));
CREATE TABLE IF NOT EXISTS events (id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT NOT NULL, site_id TEXT NOT NULL, product_id TEXT NOT NULL DEFAULT '', title TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS idx_events_created ON events(created_at);
CREATE TABLE IF NOT EXISTS orders (id TEXT PRIMARY KEY, watch_id TEXT NOT NULL DEFAULT '', site_id TEXT NOT NULL, product_id TEXT NOT NULL DEFAULT '', account_id TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, data TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS idx_orders_created ON orders(created_at);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

// migrations 按 PRAGMA user_version 依次执行：migrations[i] 把库从 v(i) 升到 v(i+1)。
// 新库由 schema 建表后同样从 v0 跑完全部迁移，因此每步都须幂等；依赖新列的索引放在迁移里建。
var migrations = []func(*sql.Tx) error{
	// v0 → v1：旧库 accounts 缺 secrets 列（账号凭据）。
	func(tx *sql.Tx) error {
		return addColumn(tx, "accounts", "secrets", `TEXT NOT NULL DEFAULT '{}'`)
	},
	// v1 → v2：orders 增加 product_id / account_id 列（按商品查最近订单、防重复下单）。
	func(tx *sql.Tx) error {
		for _, column := range []string{"product_id", "account_id"} {
			if err := addColumn(tx, "orders", column, `TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`UPDATE orders SET product_id=COALESCE(json_extract(data, '$.product_id'), ''), account_id=COALESCE(json_extract(data, '$.account_id'), '') WHERE product_id='';
CREATE INDEX IF NOT EXISTS idx_orders_product ON orders(site_id, product_id, created_at)`)
		return err
	},
}

// addColumn 列不存在时补列。
func addColumn(tx *sql.Tx, table, column, definition string) error {
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&count); err != nil || count > 0 {
		return err
	}
	_, err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, definition))
	return err
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for ; version < len(migrations); version++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if err := migrations[version](tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("v%d: %w", version+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, version+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Store 数据访问。
type Store struct {
	db *sql.DB
}

// Open 打开（或创建）数据库文件，文件权限 0600（含会话 Cookie 与通知密钥）。
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	return &Store{db: db}, nil
}

// Close 关闭连接。
func (s *Store) Close() error { return s.db.Close() }

func marshal(value any) (string, error) {
	raw, err := json.Marshal(value)
	return string(raw), err
}

// ---- sites ----

// SaveSite 新增或覆盖。
func (s *Store) SaveSite(ctx context.Context, site domain.Site) error {
	data, err := marshal(site)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO sites(id, data) VALUES(?, ?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, site.ID, data)
	return err
}

// GetSite 按 ID 读取。
func (s *Store) GetSite(ctx context.Context, id string) (domain.Site, error) {
	var site domain.Site
	err := s.getJSON(ctx, `SELECT data FROM sites WHERE id=?`, &site, id)
	return site, err
}

// ListSites 全部站点。
func (s *Store) ListSites(ctx context.Context) ([]domain.Site, error) {
	return listJSON[domain.Site](ctx, s.db, `SELECT data FROM sites ORDER BY id`)
}

// DeleteSite 级联删除站点下的账号、关注与商品快照。
func (s *Store) DeleteSite(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{`DELETE FROM sites WHERE id=?`, `DELETE FROM accounts WHERE site_id=?`, `DELETE FROM watches WHERE site_id=?`, `DELETE FROM products WHERE site_id=?`} {
		if _, err := tx.ExecContext(ctx, stmt, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- accounts ----

type cookieRecord struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Path     string    `json:"path,omitempty"`
	Expires  time.Time `json:"expires,omitempty"`
	HTTPOnly bool      `json:"http_only,omitempty"`
	Secure   bool      `json:"secure,omitempty"`
}

// SaveAccount 新增或覆盖（含 Cookie）。
func (s *Store) SaveAccount(ctx context.Context, account domain.Account) error {
	records := make([]cookieRecord, 0, len(account.Cookies))
	for _, cookie := range account.Cookies {
		records = append(records, cookieRecord{Name: cookie.Name, Value: cookie.Value, Path: cookie.Path, Expires: cookie.Expires, HTTPOnly: cookie.HttpOnly, Secure: cookie.Secure})
	}
	data, err := marshal(account)
	if err != nil {
		return err
	}
	cookies, err := marshal(records)
	if err != nil {
		return err
	}
	secrets, err := marshal(account.Credentials)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO accounts(id, site_id, data, cookies, secrets) VALUES(?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET site_id=excluded.site_id, data=excluded.data, cookies=excluded.cookies, secrets=excluded.secrets`, account.ID, account.SiteID, data, cookies, secrets)
	return err
}

// GetAccount 按 ID 读取（含 Cookie）。
func (s *Store) GetAccount(ctx context.Context, id string) (domain.Account, error) {
	row := s.db.QueryRowContext(ctx, `SELECT data, cookies, secrets FROM accounts WHERE id=?`, id)
	return scanAccount(row)
}

// ListAccounts siteID 为空时返回全部。
func (s *Store) ListAccounts(ctx context.Context, siteID string) ([]domain.Account, error) {
	query := `SELECT data, cookies, secrets FROM accounts ORDER BY id`
	args := []any{}
	if siteID != "" {
		query = `SELECT data, cookies, secrets FROM accounts WHERE site_id=? ORDER BY id`
		args = append(args, siteID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Account
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, account)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanAccount(row scanner) (domain.Account, error) {
	var data, cookies, secrets string
	if err := row.Scan(&data, &cookies, &secrets); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Account{}, ErrNotFound
		}
		return domain.Account{}, err
	}
	var account domain.Account
	if err := json.Unmarshal([]byte(data), &account); err != nil {
		return domain.Account{}, err
	}
	var records []cookieRecord
	if err := json.Unmarshal([]byte(cookies), &records); err != nil {
		return domain.Account{}, err
	}
	for _, record := range records {
		account.Cookies = append(account.Cookies, &http.Cookie{Name: record.Name, Value: record.Value, Path: record.Path, Expires: record.Expires, HttpOnly: record.HTTPOnly, Secure: record.Secure})
	}
	if err := json.Unmarshal([]byte(secrets), &account.Credentials); err != nil {
		return domain.Account{}, err
	}
	return account, nil
}

// DeleteAccount 删除账号。
func (s *Store) DeleteAccount(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id=?`, id)
	return err
}

// ---- watches ----

// SaveWatch 新增或覆盖。
func (s *Store) SaveWatch(ctx context.Context, watch domain.Watch) error {
	data, err := marshal(watch)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO watches(id, site_id, data) VALUES(?, ?, ?) ON CONFLICT(id) DO UPDATE SET site_id=excluded.site_id, data=excluded.data`, watch.ID, watch.SiteID, data)
	return err
}

// GetWatch 按 ID 读取。
func (s *Store) GetWatch(ctx context.Context, id string) (domain.Watch, error) {
	var watch domain.Watch
	err := s.getJSON(ctx, `SELECT data FROM watches WHERE id=?`, &watch, id)
	return watch, err
}

// ListWatches siteID 为空时返回全部。
func (s *Store) ListWatches(ctx context.Context, siteID string) ([]domain.Watch, error) {
	if siteID == "" {
		return listJSON[domain.Watch](ctx, s.db, `SELECT data FROM watches ORDER BY id`)
	}
	return listJSON[domain.Watch](ctx, s.db, `SELECT data FROM watches WHERE site_id=? ORDER BY id`, siteID)
}

// DeleteWatch 删除关注。
func (s *Store) DeleteWatch(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM watches WHERE id=?`, id)
	return err
}

// ---- products ----

// ReplaceProducts 用最新目录整体替换某站点的商品快照（下架商品随之删除）。
func (s *Store) ReplaceProducts(ctx context.Context, siteID string, products []domain.Product) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM products WHERE site_id=?`, siteID); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO products(site_id, id, data) VALUES(?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, product := range products {
		product.SiteID = siteID
		data, err := marshal(product)
		if err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx, siteID, product.ID, data); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListProducts siteID 为空时返回全部。
func (s *Store) ListProducts(ctx context.Context, siteID string) ([]domain.Product, error) {
	if siteID == "" {
		return listJSON[domain.Product](ctx, s.db, `SELECT data FROM products ORDER BY site_id, CAST(id AS INTEGER), id`)
	}
	return listJSON[domain.Product](ctx, s.db, `SELECT data FROM products WHERE site_id=? ORDER BY CAST(id AS INTEGER), id`, siteID)
}

// ProductCounts 站点商品统计；Visible / InStock 不含隐藏商品。
type ProductCounts struct {
	Visible int
	// InStock 可下单（in_stock / preorder，同 domain.StockStatus.Orderable）。
	InStock int
	Hidden  int
}

// CountProducts 一次查询统计站点商品，hidden 为该站点隐藏的商品 ID。
func (s *Store) CountProducts(ctx context.Context, siteID string, hidden []string) (ProductCounts, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(hidden)), ",")
	query := `SELECT COALESCE(SUM(NOT hidden), 0), COALESCE(SUM(NOT hidden AND status IN (?, ?)), 0), COALESCE(SUM(hidden), 0)
FROM (SELECT id IN (` + placeholders + `) AS hidden, json_extract(data, '$.stock.status') AS status FROM products WHERE site_id=?)`
	args := []any{string(domain.StockIn), string(domain.StockPreorder)}
	for _, id := range hidden {
		args = append(args, id)
	}
	args = append(args, siteID)
	var counts ProductCounts
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&counts.Visible, &counts.InStock, &counts.Hidden)
	return counts, err
}

// GetProduct 读取单个商品快照。
func (s *Store) GetProduct(ctx context.Context, siteID, id string) (domain.Product, error) {
	var product domain.Product
	err := s.getJSON(ctx, `SELECT data FROM products WHERE site_id=? AND id=?`, &product, siteID, id)
	return product, err
}

// ---- events ----

// AddEvent 写入事件并回填 ID。
func (s *Store) AddEvent(ctx context.Context, event *domain.Event) error {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO events(type, site_id, product_id, title, detail, created_at) VALUES(?, ?, ?, ?, ?, ?)`, event.Type, event.SiteID, event.ProductID, event.Title, event.Detail, event.CreatedAt.UnixMilli())
	if err != nil {
		return err
	}
	event.ID, err = result.LastInsertId()
	return err
}

// ListEvents 最新在前。
func (s *Store) ListEvents(ctx context.Context, siteID string, limit int) ([]domain.Event, error) {
	query := `SELECT id, type, site_id, product_id, title, detail, created_at FROM events`
	args := []any{}
	if siteID != "" {
		query += ` WHERE site_id=?`
		args = append(args, siteID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, clampLimit(limit))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Event
	for rows.Next() {
		var event domain.Event
		var createdAt int64
		if err := rows.Scan(&event.ID, &event.Type, &event.SiteID, &event.ProductID, &event.Title, &event.Detail, &createdAt); err != nil {
			return nil, err
		}
		event.CreatedAt = time.UnixMilli(createdAt).UTC()
		out = append(out, event)
	}
	return out, rows.Err()
}

// CountEventsSince 统计。
func (s *Store) CountEventsSince(ctx context.Context, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE created_at>=?`, since.UnixMilli()).Scan(&count)
	return count, err
}

// PruneEvents 删除早于 before 的事件，控制库体积。
func (s *Store) PruneEvents(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE created_at<?`, before.UnixMilli())
	return err
}

// ---- orders ----

// SaveOrder 新增或覆盖。
func (s *Store) SaveOrder(ctx context.Context, order domain.Order) error {
	data, err := marshal(order)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO orders(id, watch_id, site_id, product_id, account_id, created_at, data) VALUES(?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET product_id=excluded.product_id, account_id=excluded.account_id, data=excluded.data`, order.ID, order.WatchID, order.SiteID, order.ProductID, order.AccountID, order.CreatedAt.UnixMilli(), data)
	return err
}

// LatestPlacedOrder 某站点商品在 since 之后由该账号成功锁下的最近一单；没有时返回 ErrNotFound。
func (s *Store) LatestPlacedOrder(ctx context.Context, siteID, productID, accountID string, since time.Time) (domain.Order, error) {
	var order domain.Order
	err := s.getJSON(ctx, `SELECT data FROM orders WHERE site_id=? AND product_id=? AND account_id=? AND created_at>=? AND json_extract(data, '$.status')=? ORDER BY created_at DESC LIMIT 1`,
		&order, siteID, productID, accountID, since.UnixMilli(), string(domain.OrderPlaced))
	return order, err
}

// ProductKey 站点内商品的唯一键。
type ProductKey struct{ SiteID, ProductID string }

// ProductOrders 某商品的订单概况：最近一单（任意状态）与最近成功锁单时间（无则为零值）。
type ProductOrders struct {
	Last         domain.Order
	LastPlacedAt time.Time
}

// LatestOrders 一次查询得到各商品的订单概况；siteID 为空时覆盖全部站点。
func (s *Store) LatestOrders(ctx context.Context, siteID string) (map[ProductKey]ProductOrders, error) {
	query := `SELECT site_id, product_id, data, last_placed FROM (
	SELECT site_id, product_id, data,
		MAX(CASE WHEN json_extract(data, '$.status')=? THEN created_at END) OVER (PARTITION BY site_id, product_id) AS last_placed,
		ROW_NUMBER() OVER (PARTITION BY site_id, product_id ORDER BY created_at DESC, rowid DESC) AS rn
	FROM orders WHERE product_id!='' AND (?='' OR site_id=?)
) WHERE rn=1`
	rows, err := s.db.QueryContext(ctx, query, string(domain.OrderPlaced), siteID, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[ProductKey]ProductOrders{}
	for rows.Next() {
		var key ProductKey
		var data string
		var lastPlaced sql.NullInt64
		if err := rows.Scan(&key.SiteID, &key.ProductID, &data, &lastPlaced); err != nil {
			return nil, err
		}
		var state ProductOrders
		if err := json.Unmarshal([]byte(data), &state.Last); err != nil {
			return nil, err
		}
		if lastPlaced.Valid {
			state.LastPlacedAt = time.UnixMilli(lastPlaced.Int64).UTC()
		}
		out[key] = state
	}
	return out, rows.Err()
}

// ListOrders 最新在前。
func (s *Store) ListOrders(ctx context.Context, limit int) ([]domain.Order, error) {
	return listJSON[domain.Order](ctx, s.db, `SELECT data FROM orders ORDER BY created_at DESC LIMIT ?`, clampLimit(limit))
}

// CountPlacedSince 统计成功锁单数（不含试下单与失败）。
func (s *Store) CountPlacedSince(ctx context.Context, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE created_at>=? AND json_extract(data, '$.status')=?`, since.UnixMilli(), string(domain.OrderPlaced)).Scan(&count)
	return count, err
}

// PruneOrders 删除早于 before 的订单记录。
func (s *Store) PruneOrders(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM orders WHERE created_at<?`, before.UnixMilli())
	return err
}

// ---- settings ----

// GetSetting 读取 JSON 设置到 out；不存在时返回 ErrNotFound。
func (s *Store) GetSetting(ctx context.Context, key string, out any) error {
	return s.getJSON(ctx, `SELECT value FROM settings WHERE key=?`, out, key)
}

// PutSetting 写入 JSON 设置。
func (s *Store) PutSetting(ctx context.Context, key string, value any) error {
	data, err := marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, data)
	return err
}

// ---- helpers ----

func (s *Store) getJSON(ctx context.Context, query string, out any, args ...any) error {
	var data string
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	return json.Unmarshal([]byte(data), out)
}

func listJSON[T any](ctx context.Context, db *sql.DB, query string, args ...any) ([]T, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var item T
		if err := json.Unmarshal([]byte(data), &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func clampLimit(limit int) int {
	const maxLimit = 1000
	if limit <= 0 {
		return 100
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}
