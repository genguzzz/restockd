// Package egress 出站线路抽象：站点按优先级使用一组出口（直连 / 代理），出错时自动回退。
//
//   - Pool：出口来源，按优先级给出站点可用的出口。Registry（设置里登记的静态代理）是第一种实现，
//     将来的代理池只需实现同一接口。
//   - Client：实现 fetch.Client，按顺序尝试出口；连接失败、站点限流时换下一个，并把出了问题的出口
//     暂时冷却（Health）。非幂等请求（POST 等）只在连接都没建立时回退，避免重复下单。
//     配置了 Browser 时，遇到 Cloudflare 质询在同一出口上用真实浏览器过质询（cf_clearance 绑定出口 IP）。
package egress

import (
	"cmp"
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
	// connectCooldown 连不上时的冷却时间：代理本身连不上对所有站点生效，目标站点连不上只冷却该 host。
	connectCooldown = 2 * time.Minute
	// defaultRateLimitCooldown 站点 429 未带 Retry-After、且 Client 未指定时的冷却时间。
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
	// Proxy 代理地址，直连为空；浏览器回落经同一代理，连接失败按它区分是代理还是目标站点的问题。
	Proxy string
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
	health   *Health
	proxies  map[string]Proxy
	defaults []string
	clients  map[string]fetch.Client
}

// NewRegistry direct 为服务器默认出口客户端，timeout 用于新建代理客户端；
// health 为 Client 共用的冷却表，代理地址变更时清掉该出口的冷却（可为 nil）。
func NewRegistry(direct fetch.Client, timeout time.Duration, health *Health) *Registry {
	return &Registry{direct: direct, timeout: timeout, health: health, proxies: map[string]Proxy{}, clients: map[string]fetch.Client{}}
}

// Apply 热更新登记表：释放不再登记的代理客户端；地址变了的代理视为新出口，清除其冷却。
func (r *Registry) Apply(settings Settings) {
	proxies := make(map[string]Proxy, len(settings.Proxies))
	urls := map[string]bool{}
	for _, proxy := range settings.Proxies {
		proxies[proxy.ID] = proxy
		urls[proxy.URL] = true
	}
	r.mu.Lock()
	var changed []string
	for id, old := range r.proxies {
		if proxy, ok := proxies[id]; !ok || proxy.URL != old.URL {
			changed = append(changed, id)
		}
	}
	for proxyURL := range r.clients {
		if !urls[proxyURL] {
			delete(r.clients, proxyURL)
		}
	}
	r.proxies = proxies
	r.defaults = append([]string(nil), settings.Default...)
	r.mu.Unlock()
	if r.health != nil {
		for _, id := range changed {
			r.health.reset(id)
		}
	}
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
		return []Route{r.directRoute()}, nil
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
		return []Route{r.directRoute()}, nil
	}
	return routes, nil
}

func (r *Registry) directRoute() Route {
	return Route{ID: Direct, Name: "直连", Client: r.direct}
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
		return r.directRoute(), true, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	proxy, ok := r.proxies[id]
	if !ok || !proxy.Enabled || strings.TrimSpace(proxy.URL) == "" {
		return Route{}, false, nil
	}
	client, ok := r.clients[proxy.URL]
	if !ok {
		httpClient, err := fetch.NewHTTPClient(r.timeout, proxy.URL)
		if err != nil {
			return Route{}, false, err
		}
		client = httpClient
		r.clients[proxy.URL] = client
	}
	return Route{ID: proxy.ID, Name: cmp.Or(proxy.Name, proxy.ID), Client: client, Proxy: proxy.URL}, true, nil
}

// Health 出口冷却状态，跨请求共享。连接失败：代理连不上按出口全局冷却，目标 host 连不上按「出口 + host」冷却；
// 限流按「站点 + 出口 + 路径前缀」冷却（站点往往只封部分页面，如 usvps24 封下单页但库存接口照常）。
// 过期条目在读写时顺手删除。
type Health struct {
	mu    sync.Mutex
	until map[coolKey]time.Time
	now   func() time.Time
}

// coolKey 冷却范围；site 为空表示连接冷却，scope 为空表示整个出口。
type coolKey struct {
	site  string
	route string
	scope string
}

// NewHealth 创建冷却表。
func NewHealth() *Health {
	return &Health{until: map[coolKey]time.Time{}, now: time.Now}
}

func (h *Health) cooling(keys ...coolKey) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	var wait time.Duration
	for _, key := range keys {
		until, ok := h.until[key]
		if !ok {
			continue
		}
		if !until.After(now) {
			delete(h.until, key)
			continue
		}
		wait = max(wait, until.Sub(now))
	}
	return wait
}

func (h *Health) cool(key coolKey, d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	for other, until := range h.until {
		if !until.After(now) {
			delete(h.until, other)
		}
	}
	if until := now.Add(d); until.After(h.until[key]) {
		h.until[key] = until
	}
}

// reset 清除某出口的全部冷却（代理地址变更后旧 IP 的封禁不再适用）。
func (h *Health) reset(routeID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key := range h.until {
		if key.route == routeID {
			delete(h.until, key)
		}
	}
}

// Cooling 出口对该请求剩余的冷却时间（取连接冷却与限流冷却的较大者）。
func (h *Health) Cooling(siteID, routeID, rawURL string) time.Duration {
	return h.cooling(coolKey{route: routeID}, coolKey{route: routeID, scope: hostOf(rawURL)}, limitKey(siteID, routeID, rawURL))
}

// limitKey 限流范围：站点 + 出口 + host + 路径第一段。
func limitKey(siteID, routeID, rawURL string) coolKey {
	scope := rawURL
	if parsed, err := url.Parse(rawURL); err == nil {
		segment := strings.SplitN(strings.TrimPrefix(parsed.Path, "/"), "/", 2)[0]
		scope = parsed.Host + "/" + segment
	}
	return coolKey{site: siteID, route: routeID, scope: scope}
}

func hostOf(rawURL string) string {
	if parsed, err := url.Parse(rawURL); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return rawURL
}

// Browser 真实浏览器回落（如 *fetch.FlareSolverrClient），proxyURL 为出口代理（空为直连）。
type Browser interface {
	Do(ctx context.Context, session *fetch.Session, req *fetch.Request, proxyURL string) (*fetch.Response, error)
}

// Client 按优先级依次尝试出口的 fetch.Client。
type Client struct {
	SiteID string
	Routes []Route
	Health *Health
	Log    *slog.Logger
	// Browser 非空时遇到质询在同一出口上用浏览器重试；为空则把质询错误原样返回。
	Browser Browser
	// RateLimitCooldown 429 未带 Retry-After 时的冷却，0 为 1 小时（见 provider.Meta.RateLimitCooldown）。
	RateLimitCooldown time.Duration
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
		resp, err = c.attempt(ctx, session, req, route)
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

// ProxyFor 对 rawURL 的请求接下来会用的出口代理（第一个未冷却的出口；直连为空串）。
// 验证码求解服务据此走同一出口：有的站点只认生成 token 与提交表单来自同一 IP。
func (c *Client) ProxyFor(rawURL string) string {
	for _, route := range c.Routes {
		if c.Health.Cooling(c.SiteID, route.ID, rawURL) <= 0 {
			return route.Proxy
		}
	}
	return ""
}

// cfClearance Cloudflare 过质询后下发的放行 Cookie（绑定出口 IP 与 UA）。
const cfClearance = "cf_clearance"

// attempt 用一个出口发请求；遇到质询且配置了浏览器时在同一出口上过质询。
// 浏览器能原样表达的请求（GET / 表单 POST、无自定义头）直接用浏览器的响应；其余请求先让浏览器 GET 页面
// 拿到 cf_clearance 与 UA（写回会话），再用原客户端重发，保留 JSON 与请求头。
func (c *Client) attempt(ctx context.Context, session *fetch.Session, req *fetch.Request, route Route) (*fetch.Response, error) {
	resp, err := route.Client.Do(ctx, session, req)
	if fetch.IsChallenge(err) && session != nil && session.DropCookie(cfClearance) {
		// 失效的 cf_clearance（站点改了 Cloudflare 规则、或是另一个出口 IP 拿到的）会被直接拦截，
		// 带着它连浏览器也过不去（FlareSolverr 会误报 IP 被封）：去掉后重试一次。
		resp, err = route.Client.Do(ctx, session, req)
	}
	if c.Browser == nil || !fetch.IsChallenge(err) {
		return resp, err
	}
	if req.JSON == nil && len(req.Header) == 0 && (req.Method == "" || req.Method == http.MethodGet || req.Method == http.MethodPost) {
		return c.Browser.Do(ctx, session, req, route.Proxy)
	}
	if _, err := c.Browser.Do(ctx, session, &fetch.Request{Method: http.MethodGet, URL: req.URL}, route.Proxy); err != nil {
		return nil, err
	}
	return route.Client.Do(ctx, session, req)
}

// fallbackReason 判断能否换下一个出口，并按原因冷却当前出口。
func (c *Client) fallbackReason(ctx context.Context, req *fetch.Request, route Route, err error) (string, bool) {
	if ctx.Err() != nil {
		return "", false
	}
	var rateLimit *fetch.RateLimitError
	if errors.As(err, &rateLimit) {
		// 站点拒绝了请求、未处理：任何方法都可换线。
		wait := cmp.Or(rateLimit.RetryAfter, c.RateLimitCooldown, defaultRateLimitCooldown)
		c.Health.cool(limitKey(c.SiteID, route.ID, req.URL), wait)
		return "rate limited", true
	}
	if isConnectError(err) {
		key := coolKey{route: route.ID, scope: hostOf(req.URL)}
		if proxyFault(route, err) {
			key.scope = ""
		}
		c.Health.cool(key, connectCooldown)
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

// proxyFault 连接失败是否出在代理本身（拨号 / 握手代理失败），而非目标站点不可达。
// 直连出口连不上只说明目标站点挂了，不能让其它站点也绕开直连。
func proxyFault(route Route, err error) bool {
	if route.Proxy == "" {
		return false
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && (opErr.Op == "proxyconnect" || strings.HasPrefix(opErr.Op, "socks"))
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
