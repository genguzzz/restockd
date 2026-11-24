// Package egress 出站线路抽象：站点按优先级使用一组出口（直连 / 代理），出错时自动回退。
//
//   - Pool：出口来源，按优先级给出站点可用的出口。Registry（设置里登记的静态代理）是第一种实现，
//     将来的代理池只需实现同一接口。
//   - Client：实现 fetch.Client，按顺序尝试出口；连接失败、站点限流时换下一个，并把出了问题的出口
//     暂时冷却（Health）。非幂等请求（POST 等）只在连接都没建立时回退，避免重复下单。
package egress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/genguzzz/restockd/internal/fetch"
)

// Direct 内置的直连出口 ID（服务器默认出口）。
const Direct = "direct"

const (
	// connectCooldown 出口连不上时的冷却时间（对所有站点生效）。
	connectCooldown = 2 * time.Minute
	// defaultRateLimitCooldown 站点 429 未带 Retry-After 时的冷却时间。
	defaultRateLimitCooldown = time.Hour
)

// ErrNoEgress 站点指定的出口都不可用（未登记或已停用）。
var ErrNoEgress = errors.New("no usable egress for this site")

// Proxy 设置里登记的一个代理出口。
type Proxy struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"` // http://user:pass@host:port 或 socks5://host:port
	Enabled bool   `json:"enabled"`
}

// Settings 出口设置：代理登记表 + 未单独指定出口的站点使用的默认优先级。
type Settings struct {
	Proxies []Proxy  `json:"proxies"`
	Default []string `json:"default"`
}

// Route 一个可用出口。
type Route struct {
	ID     string
	Name   string
	Client fetch.Client
}

// Pool 出口来源。preferred 为站点指定的出口 ID 优先级（空表示用默认）；返回按优先级排列的可用出口。
type Pool interface {
	Routes(siteID string, preferred []string) ([]Route, error)
}

// Registry 静态登记表实现的 Pool：直连 + 设置里登记的代理；代理客户端按 URL 复用。
type Registry struct {
	mu       sync.RWMutex
	direct   fetch.Client
	timeout  time.Duration
	proxies  map[string]Proxy
	defaults []string
	clients  map[string]fetch.Client
}

// NewRegistry direct 为服务器默认出口客户端，timeout 用于新建代理客户端。
func NewRegistry(direct fetch.Client, timeout time.Duration) *Registry {
	return &Registry{direct: direct, timeout: timeout, proxies: map[string]Proxy{}, clients: map[string]fetch.Client{}}
}

// Apply 热更新登记表；URL 变化的代理在下次使用时重建客户端。
func (r *Registry) Apply(settings Settings) {
	proxies := make(map[string]Proxy, len(settings.Proxies))
	for _, proxy := range settings.Proxies {
		proxies[proxy.ID] = proxy
	}
	r.mu.Lock()
	r.proxies = proxies
	r.defaults = append([]string(nil), settings.Default...)
	r.mu.Unlock()
}

// Routes 实现 Pool：站点指定了出口则严格按其列表（都不可用时报错，不偷偷回落直连——
// 站点往往正是因为直连被封才指定代理）；未指定时按默认列表，默认列表为空即直连。
func (r *Registry) Routes(siteID string, preferred []string) ([]Route, error) {
	r.mu.RLock()
	chain := preferred
	explicit := len(chain) > 0
	if !explicit {
		chain = r.defaults
	}
	r.mu.RUnlock()
	if len(chain) == 0 {
		return []Route{{ID: Direct, Name: "直连", Client: r.direct}}, nil
	}
	var routes []Route
	seen := map[string]bool{}
	for _, id := range chain {
		if seen[id] {
			continue
		}
		seen[id] = true
		route, ok, err := r.route(id)
		if err != nil {
			return nil, fmt.Errorf("egress %s: %w", id, err)
		}
		if ok {
			routes = append(routes, route)
		}
	}
	if len(routes) == 0 {
		if explicit {
			return nil, fmt.Errorf("%w (site %s: %s)", ErrNoEgress, siteID, strings.Join(chain, ","))
		}
		return []Route{{ID: Direct, Name: "直连", Client: r.direct}}, nil
	}
	return routes, nil
}

// Route 单个出口（测试连通性用）。
func (r *Registry) Route(id string) (Route, error) {
	route, ok, err := r.route(id)
	if err != nil {
		return Route{}, err
	}
	if !ok {
		return Route{}, fmt.Errorf("egress %s not found or disabled", id)
	}
	return route, nil
}

func (r *Registry) route(id string) (Route, bool, error) {
	if id == Direct {
		return Route{ID: Direct, Name: "直连", Client: r.direct}, true, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	proxy, ok := r.proxies[id]
	if !ok || !proxy.Enabled || strings.TrimSpace(proxy.URL) == "" {
		return Route{}, false, nil
	}
	client, ok := r.clients[proxy.URL]
	if !ok {
		direct, err := fetch.NewDirectClient(r.timeout, proxy.URL)
		if err != nil {
			return Route{}, false, err
		}
		client = direct
		r.clients[proxy.URL] = client
	}
	return Route{ID: proxy.ID, Name: firstNonEmpty(proxy.Name, proxy.ID), Client: client}, true, nil
}

// Health 出口冷却状态，跨请求共享。连接失败按出口全局冷却；限流按「站点 + 出口 + 路径前缀」冷却
// （站点往往只封部分页面，如 usvps24 封下单页但库存接口照常）。
type Health struct {
	mu    sync.Mutex
	until map[string]time.Time
	now   func() time.Time
}

// NewHealth 创建冷却表。
func NewHealth() *Health {
	return &Health{until: map[string]time.Time{}, now: time.Now}
}

func (h *Health) cooling(key string) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.until[key].Sub(h.now())
}

func (h *Health) cool(key string, d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if until := h.now().Add(d); until.After(h.until[key]) {
		h.until[key] = until
	}
}

// Cooling 出口对该请求剩余的冷却时间（取连接冷却与限流冷却的较大者）。
func (h *Health) Cooling(siteID, routeID, rawURL string) time.Duration {
	return max(h.cooling(connectKey(routeID)), h.cooling(limitKey(siteID, routeID, rawURL)))
}

func connectKey(routeID string) string { return "connect|" + routeID }

// limitKey 限流范围：站点 + 出口 + host + 路径第一段。
func limitKey(siteID, routeID, rawURL string) string {
	scope := rawURL
	if parsed, err := url.Parse(rawURL); err == nil {
		segment := strings.SplitN(strings.TrimPrefix(parsed.Path, "/"), "/", 2)[0]
		scope = parsed.Host + "/" + segment
	}
	return "limit|" + siteID + "|" + routeID + "|" + scope
}

// Client 按优先级依次尝试出口的 fetch.Client。
type Client struct {
	SiteID string
	Routes []Route
	Health *Health
	Log    *slog.Logger
}

// Do 实现 fetch.Client。
func (c *Client) Do(ctx context.Context, session *fetch.Session, req *fetch.Request) (*fetch.Response, error) {
	if len(c.Routes) == 0 {
		return nil, ErrNoEgress
	}
	candidates := make([]Route, 0, len(c.Routes))
	var soonest time.Duration
	for _, route := range c.Routes {
		wait := c.Health.Cooling(c.SiteID, route.ID, req.URL)
		if wait <= 0 {
			candidates = append(candidates, route)
			continue
		}
		if soonest == 0 || wait < soonest {
			soonest = wait
		}
	}
	if len(candidates) == 0 {
		// 全部冷却：不发请求。被限流的页面再访问只会让站点重新计时。
		return nil, fmt.Errorf("all egress routes cooling down: %w", &fetch.RateLimitError{RetryAfter: soonest})
	}
	var resp *fetch.Response
	var err error
	for i, route := range candidates {
		resp, err = route.Client.Do(ctx, session, req)
		if err == nil {
			return resp, nil
		}
		reason, ok := c.fallbackReason(ctx, req, route, err)
		if !ok {
			return resp, err
		}
		if i < len(candidates)-1 && c.Log != nil {
			c.Log.Info("egress fallback", "site", c.SiteID, "route", route.ID, "next", candidates[i+1].ID, "reason", reason, "err", err)
		}
	}
	return resp, err
}

// fallbackReason 判断能否换下一个出口，并按原因冷却当前出口。
func (c *Client) fallbackReason(ctx context.Context, req *fetch.Request, route Route, err error) (string, bool) {
	if ctx.Err() != nil {
		return "", false
	}
	var rateLimit *fetch.RateLimitError
	if errors.As(err, &rateLimit) {
		// 站点拒绝了请求、未处理：任何方法都可换线。
		wait := rateLimit.RetryAfter
		if wait <= 0 {
			wait = defaultRateLimitCooldown
		}
		c.Health.cool(limitKey(c.SiteID, route.ID, req.URL), wait)
		return "rate limited", true
	}
	if isConnectError(err) {
		c.Health.cool(connectKey(route.ID), connectCooldown)
		return "connect failed", true
	}
	if idempotent(req.Method) && isNetworkError(err) {
		// 请求可能已到达站点；只有幂等请求可以安全重发。
		return "network error", true
	}
	return "", false
}

func idempotent(method string) bool {
	switch strings.ToUpper(method) {
	case "", http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// isConnectError 连接阶段失败（拨号、代理握手、DNS）：请求尚未发出，任何方法都可安全重试。
func isConnectError(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Op == "dial" || opErr.Op == "proxyconnect" || strings.HasPrefix(opErr.Op, "socks") {
			return true
		}
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

// isNetworkError 传输层错误（含超时、连接被重置），不含 HTTP 状态与质询。
func isNetworkError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
