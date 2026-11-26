// Package domain 定义与主机商无关的统一领域模型：商品、价格、库存、账号、关注规则与订单。
//
// 所有 Provider 实现都把各自站点的数据映射为这里的结构，引擎、存储与 API 只依赖本包。
package domain

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"time"
)

// BillingCycle 统一的计费周期标识。
type BillingCycle string

const (
	CycleMonthly      BillingCycle = "monthly"
	CycleQuarterly    BillingCycle = "quarterly"
	CycleSemiannually BillingCycle = "semiannually"
	CycleAnnually     BillingCycle = "annually"
	CycleBiennially   BillingCycle = "biennially"
	CycleTriennially  BillingCycle = "triennially"
	CycleOneTime      BillingCycle = "onetime"
)

// Money 金额，以最小货币单位（分）存储，避免浮点误差。
type Money struct {
	Cents    int64  `json:"cents"`
	Currency string `json:"currency"`
}

// String 统一金额展示，如 "188.00 CNY"。
func (m Money) String() string {
	cents := m.Cents % 100
	if cents < 0 {
		cents = -cents
	}
	return fmt.Sprintf("%d.%02d %s", m.Cents/100, cents, m.Currency)
}

// Price 某个计费周期下的售价。
type Price struct {
	Cycle  BillingCycle `json:"cycle"`
	Amount Money        `json:"amount"`
	Setup  Money        `json:"setup"`
	// RefID 站点内部的周期 ID（如 usvps24 billing_cycles[].id），下单时可能需要。
	RefID string `json:"ref_id,omitempty"`
}

// Choice 一个可选值。
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
	// DeltaMonthly 选中后每月增加的费用，可为零。
	DeltaMonthly Money `json:"delta_monthly"`
}

// ConfigOption 商品的可配置项（带宽、IP 数量等）。
type ConfigOption struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Choices []Choice `json:"choices"`
}

// Specs 规格信息；结构化字段尽量填写，站点特有的放 Extra。
type Specs struct {
	CPU           string            `json:"cpu,omitempty"`
	RAMMB         int               `json:"ram_mb,omitempty"`
	DiskGB        int               `json:"disk_gb,omitempty"`
	BandwidthMbps int               `json:"bandwidth_mbps,omitempty"`
	TrafficGB     int               `json:"traffic_gb,omitempty"`
	Location      string            `json:"location,omitempty"`
	IPType        string            `json:"ip_type,omitempty"`
	Extra         map[string]string `json:"extra,omitempty"`
}

// StockStatus 库存状态。
type StockStatus string

const (
	StockIn       StockStatus = "in_stock"
	StockOut      StockStatus = "out_of_stock"
	StockPreorder StockStatus = "preorder"
	StockClosed   StockStatus = "closed"
	StockUnknown  StockStatus = "unknown"
)

// Orderable 当前状态是否可下单。
func (s StockStatus) Orderable() bool {
	return s == StockIn || s == StockPreorder
}

// Stock 库存信息；Qty 为 nil 表示站点未披露数量。
type Stock struct {
	Status StockStatus `json:"status"`
	Qty    *int        `json:"qty,omitempty"`
	Reason string      `json:"reason,omitempty"`
}

// Product 统一商品模型。
type Product struct {
	SiteID      string         `json:"site_id"`
	ID          string         `json:"id"`
	Group       string         `json:"group,omitempty"`
	Name        string         `json:"name"`
	URL         string         `json:"url,omitempty"`
	Specs       Specs          `json:"specs"`
	Prices      []Price        `json:"prices"`
	Options     []ConfigOption `json:"options,omitempty"`
	OSTemplates []Choice       `json:"os_templates,omitempty"`
	Stock       Stock          `json:"stock"`
	NoDiscount  bool           `json:"no_discount,omitempty"`
	FetchedAt   time.Time      `json:"fetched_at"`
}

// PriceFor 返回指定周期的价格。
func (p Product) PriceFor(cycle BillingCycle) (Price, bool) {
	for _, price := range p.Prices {
		if price.Cycle == cycle {
			return price, true
		}
	}
	return Price{}, false
}

// PriceSummary 价格摘要（优先月付，否则第一个周期），如 "CAD 19.99 monthly"；无价格为空。
func (p Product) PriceSummary() string {
	if len(p.Prices) == 0 {
		return ""
	}
	price, ok := p.PriceFor(CycleMonthly)
	if !ok {
		price = p.Prices[0]
	}
	return fmt.Sprintf("%s %s", price.Amount, price.Cycle)
}

// Promo 站点公开的促销信息。
type Promo struct {
	Code     string    `json:"code"`
	Percent  int       `json:"percent"`
	StartsAt time.Time `json:"starts_at,omitempty"`
	EndsAt   time.Time `json:"ends_at,omitempty"`
}

// Catalog 一次抓取得到的完整目录。
type Catalog struct {
	Products []Product `json:"products"`
	Promo    *Promo    `json:"promo,omitempty"`
	Gateways []string  `json:"gateways,omitempty"`
}

// Site 一个被监控的站点实例：Kind 指向 Provider 实现，Config 为该实现的参数。
// 同一 Kind（如 whmcs）可对应多个站点，新增 WHMCS 商家只需加配置。
type Site struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Name        string            `json:"name"`
	BaseURL     string            `json:"base_url"`
	Config      map[string]string `json:"config"`
	IntervalSec int               `json:"interval_sec"`
	FetchMode   FetchMode         `json:"fetch_mode"`
	// Egress 出口优先级（egress 登记的出口 ID，"direct" 为直连）；空表示用全局默认。出错按顺序回退。
	Egress         []string `json:"egress,omitempty"`
	MonitorAccount string   `json:"monitor_account_id,omitempty"`
	// HiddenProducts 用户隐藏的商品 ID：不列出、不通知、不自动锁单（只经隐藏 / 取消隐藏接口修改）。
	HiddenProducts  []string  `json:"hidden_products,omitempty"`
	Enabled         bool      `json:"enabled"`
	LastPollAt      time.Time `json:"last_poll_at,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	NextPollAt      time.Time `json:"next_poll_at,omitempty"`
	ConsecutiveFail int       `json:"consecutive_fail"`
}

// Hidden 商品是否被用户隐藏。
func (s Site) Hidden(productID string) bool { return slices.Contains(s.HiddenProducts, productID) }

// FetchMode 抓取方式。
type FetchMode string

const (
	// FetchDirect 直接用 Go HTTP 客户端访问。
	FetchDirect FetchMode = "direct"
	// FetchFlareSolverr 经由 FlareSolverr 浏览器代理访问（Cloudflare 保护站点）。
	FetchFlareSolverr FetchMode = "flaresolverr"
)

// AccountStatus 账号会话状态。
type AccountStatus string

const (
	AccountUnknown AccountStatus = "unknown"
	AccountActive  AccountStatus = "active"
	AccountExpired AccountStatus = "expired"
)

// Credentials 托管的登录凭据（密钥）：单独存储，API 永不回传密码。
type Credentials struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// Usable 用户名与密码齐全才能自动登录。
func (c Credentials) Usable() bool { return c.Username != "" && c.Password != "" }

// Account 托管的站点账号：Cookie 会话 + 可选登录凭据。有凭据且站点支持时，会话失效会自动重新登录。
type Account struct {
	ID          string         `json:"id"`
	SiteID      string         `json:"site_id"`
	Label       string         `json:"label"`
	Cookies     []*http.Cookie `json:"-"`
	Credentials Credentials    `json:"-"`
	LoginAt     time.Time      `json:"login_at,omitempty"`
	// CredentialsRejected 站点明确拒绝了当前密码：暂停自动登录（避免反复失败触发封禁），
	// 直到用户修改凭据或手动点「登录」。
	CredentialsRejected bool          `json:"credentials_rejected,omitempty"`
	UserAgent           string        `json:"user_agent,omitempty"`
	Status              AccountStatus `json:"status"`
	Identity            string        `json:"identity,omitempty"`
	Balance             *Money        `json:"balance,omitempty"`
	CheckedAt           time.Time     `json:"checked_at,omitempty"`
	LastError           string        `json:"last_error,omitempty"`
	Defaults            OrderDefaults `json:"defaults"`
	CreatedAt           time.Time     `json:"created_at"`
}

// OrderDefaults 账号级默认下单参数，Watch 未填写时回落到这里。
type OrderDefaults struct {
	Gateway  string `json:"gateway,omitempty"`
	OS       string `json:"os,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	SSHKey   string `json:"ssh_key,omitempty"`
	// Coupon 默认优惠码：关注规则 / 手动下单未填写时自动使用。
	Coupon string `json:"coupon,omitempty"`
}

// Watch 关注规则：关注某站点某商品，可选择有货时自动锁单。
type Watch struct {
	ID            string            `json:"id"`
	SiteID        string            `json:"site_id"`
	ProductID     string            `json:"product_id"`
	ProductName   string            `json:"product_name,omitempty"`
	Cycle         BillingCycle      `json:"cycle"`
	Options       map[string]string `json:"options,omitempty"`
	OS            string            `json:"os,omitempty"`
	Hostname      string            `json:"hostname,omitempty"`
	Coupon        string            `json:"coupon,omitempty"`
	Gateway       string            `json:"gateway,omitempty"`
	Qty           int               `json:"qty"`
	AccountID     string            `json:"account_id,omitempty"`
	AutoOrder     bool              `json:"auto_order"`
	MaxPriceCents int64             `json:"max_price_cents,omitempty"`
	MaxOrders     int               `json:"max_orders"`
	OrdersPlaced  int               `json:"orders_placed"`
	Notify        bool              `json:"notify"`
	Enabled       bool              `json:"enabled"`
	CreatedAt     time.Time         `json:"created_at"`
	LastTriggered time.Time         `json:"last_triggered,omitempty"`
}

// OrderSpec 下单参数，由 Watch + 账号默认值合成。
type OrderSpec struct {
	ProductID string            `json:"product_id"`
	Cycle     BillingCycle      `json:"cycle"`
	Options   map[string]string `json:"options,omitempty"`
	OS        string            `json:"os,omitempty"`
	Hostname  string            `json:"hostname,omitempty"`
	SSHKey    string            `json:"ssh_key,omitempty"`
	Coupon    string            `json:"coupon,omitempty"`
	Gateway   string            `json:"gateway,omitempty"`
	Qty       int               `json:"qty"`
	// DryRun 为 true 时只做到提交前一步（加购物车、校验优惠码、算价），不真正下单。
	DryRun bool `json:"dry_run"`
}

// Quote 算价结果。
type Quote struct {
	Subtotal    Money  `json:"subtotal"`
	Discount    Money  `json:"discount"`
	Total       Money  `json:"total"`
	CouponValid bool   `json:"coupon_valid"`
	Message     string `json:"message,omitempty"`
}

// OrderStatus 订单记录状态。
type OrderStatus string

const (
	OrderPending OrderStatus = "pending"
	OrderPlaced  OrderStatus = "placed"
	OrderDryRun  OrderStatus = "dry_run"
	OrderFailed  OrderStatus = "failed"
	// OrderCancelled 已在站点取消（未付款订单 / 账单作废）。
	OrderCancelled OrderStatus = "cancelled"
)

// OrderResult Provider 下单返回。
type OrderResult struct {
	OrderID   string `json:"order_id,omitempty"`
	InvoiceID string `json:"invoice_id,omitempty"`
	PayURL    string `json:"pay_url,omitempty"`
	Total     Money  `json:"total"`
	Message   string `json:"message,omitempty"`
}

// Order 本地订单记录。
type Order struct {
	ID          string      `json:"id"`
	WatchID     string      `json:"watch_id,omitempty"`
	SiteID      string      `json:"site_id"`
	AccountID   string      `json:"account_id"`
	ProductID   string      `json:"product_id"`
	ProductName string      `json:"product_name"`
	Spec        OrderSpec   `json:"spec"`
	Status      OrderStatus `json:"status"`
	Result      OrderResult `json:"result"`
	Error       string      `json:"error,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	DurationMs  int64       `json:"duration_ms"`
	// CancelledAt 在站点取消的时间（未取消为零值）。
	CancelledAt time.Time `json:"cancelled_at,omitempty"`
}

// EventType 库存事件类型。
type EventType string

const (
	EventRestock     EventType = "restock"
	EventSoldOut     EventType = "sold_out"
	EventPriceChange EventType = "price_change"
	EventNewProduct  EventType = "new_product"
	EventOrder       EventType = "order"
	EventChallenge   EventType = "challenge"
	EventPollError   EventType = "poll_error"
	// EventPollRecovered 之前失败的站点轮询恢复正常。
	EventPollRecovered EventType = "poll_recovered"
	// EventAccount 账号异常：会话失效且无法自动恢复、自动登录被拒。
	EventAccount EventType = "account"
)

// Event 事件流记录。
type Event struct {
	ID        int64     `json:"id"`
	Type      EventType `json:"type"`
	SiteID    string    `json:"site_id"`
	ProductID string    `json:"product_id,omitempty"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

var cycleOrder = map[BillingCycle]int{CycleMonthly: 1, CycleQuarterly: 2, CycleSemiannually: 3, CycleAnnually: 4, CycleBiennially: 5, CycleTriennially: 6, CycleOneTime: 7}

// SortPrices 按 月 → 季 → 半年 → 年 → 两年 → 三年 → 一次性 排序，未知周期排最后。
func (p *Product) SortPrices() {
	rank := func(cycle BillingCycle) int {
		if order, ok := cycleOrder[cycle]; ok {
			return order
		}
		return len(cycleOrder) + 1
	}
	sort.SliceStable(p.Prices, func(i, j int) bool { return rank(p.Prices[i].Cycle) < rank(p.Prices[j].Cycle) })
}

// StockWindow 商品的一段在售期（补货 → 售罄），按轮询观测：
// StartedAt 首次看到可下单，LastSeenAt 最后一次看到可下单，EndedAt 首次看到不可下单或下架（在售中为 nil）。
// 真实起止时间与观测值相差不超过一个轮询间隔。
type StockWindow struct {
	ID         int64      `json:"id"`
	SiteID     string     `json:"site_id"`
	ProductID  string     `json:"product_id"`
	StartedAt  time.Time  `json:"started_at"`
	LastSeenAt time.Time  `json:"last_seen_at"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	// Baseline 开始于首次抓取（或本功能上线时已在售），真实补货时间更早。
	Baseline bool `json:"baseline,omitempty"`
	// Qty / Price 补货时的库存数（站点披露时）与价格摘要。
	Qty   *int   `json:"qty,omitempty"`
	Price string `json:"price,omitempty"`
}

// Duration 在售时长：已结束按 EndedAt，在售中按 now。
func (w StockWindow) Duration(now time.Time) time.Duration {
	if w.EndedAt != nil {
		return w.EndedAt.Sub(w.StartedAt)
	}
	return now.Sub(w.StartedAt)
}

// StockHistory 商品补货概况（商品列表用）。
type StockHistory struct {
	// Current 当前在售期（未在售为 nil）；Last 最近一段已结束的在售期。
	Current *StockWindow `json:"current,omitempty"`
	Last    *StockWindow `json:"last,omitempty"`
	// Restocks30d 近 30 天补货次数（不含 Baseline）。
	Restocks30d int `json:"restocks_30d"`
}
