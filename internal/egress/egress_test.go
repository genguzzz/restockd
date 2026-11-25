package egress

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/genguzzz/restockd/internal/fetch"
)

// stubClient 按 URL 返回预设结果并计数。
type stubClient struct {
	calls  int
	result func(req *fetch.Request) (*fetch.Response, error)
}

func (s *stubClient) Do(_ context.Context, _ *fetch.Session, req *fetch.Request) (*fetch.Response, error) {
	s.calls++
	return s.result(req)
}

func ok() (*fetch.Response, error) { return &fetch.Response{StatusCode: 200}, nil }

func limited() (*fetch.Response, error) {
	return &fetch.Response{StatusCode: 429}, &fetch.RateLimitError{RetryAfter: time.Hour}
}

var connectRefused = &url.Error{Op: "Get", URL: "https://x", Err: &net.OpError{Op: "proxyconnect", Net: "tcp", Err: errors.New("connection refused")}}
var midStreamReset = &url.Error{Op: "Post", URL: "https://x", Err: io.ErrUnexpectedEOF}

func newClient(routes ...Route) *Client {
	return &Client{SiteID: "s", Routes: routes, Health: NewHealth()}
}

func TestPriorityAndRateLimitFallback(t *testing.T) {
	primary := &stubClient{result: func(req *fetch.Request) (*fetch.Response, error) {
		if req.URL == "https://shop.test/orders/create" {
			return limited()
		}
		return ok()
	}}
	backup := &stubClient{result: func(*fetch.Request) (*fetch.Response, error) { return ok() }}
	client := newClient(Route{ID: "a", Client: primary}, Route{ID: "b", Client: backup})
	session, _ := fetch.NewSession("https://shop.test", nil, "")

	if _, err := client.Do(context.Background(), session, &fetch.Request{Method: "GET", URL: "https://shop.test/api/v1/stock"}); err != nil || primary.calls != 1 || backup.calls != 0 {
		t.Fatalf("highest priority route must serve first: err=%v a=%d b=%d", err, primary.calls, backup.calls)
	}
	if _, err := client.Do(context.Background(), session, &fetch.Request{Method: "GET", URL: "https://shop.test/orders/create"}); err != nil || backup.calls != 1 {
		t.Fatalf("429 must fall back to the next route: err=%v b=%d", err, backup.calls)
	}
	// 冷却只针对被限流的路径：/orders 走备用，/api 仍走主线。
	_, _ = client.Do(context.Background(), session, &fetch.Request{Method: "GET", URL: "https://shop.test/orders/create"})
	if primary.calls != 2 || backup.calls != 2 {
		t.Fatalf("rate-limited route must be skipped for that path: a=%d b=%d", primary.calls, backup.calls)
	}
	_, _ = client.Do(context.Background(), session, &fetch.Request{Method: "GET", URL: "https://shop.test/api/v1/stock"})
	if primary.calls != 3 {
		t.Fatalf("other paths must keep using the primary route: a=%d", primary.calls)
	}
}

func TestAllRoutesCoolingSendsNothing(t *testing.T) {
	only := &stubClient{result: func(*fetch.Request) (*fetch.Response, error) { return limited() }}
	client := newClient(Route{ID: "a", Client: only})
	session, _ := fetch.NewSession("https://shop.test", nil, "")
	req := &fetch.Request{Method: "GET", URL: "https://shop.test/orders/create"}
	for i := 0; i < 3; i++ {
		_, err := client.Do(context.Background(), session, req)
		var rateLimit *fetch.RateLimitError
		if !errors.As(err, &rateLimit) {
			t.Fatalf("want rate limit error, got %v", err)
		}
	}
	if only.calls != 1 {
		t.Fatalf("a cooling route must not be requested again, got %d calls", only.calls)
	}
}

func TestPostFallbackOnlyBeforeConnecting(t *testing.T) {
	session, _ := fetch.NewSession("https://shop.test", nil, "")
	post := &fetch.Request{Method: "POST", URL: "https://shop.test/cart/checkout", JSON: []byte(`{}`)}

	down := &stubClient{result: func(*fetch.Request) (*fetch.Response, error) { return nil, connectRefused }}
	backup := &stubClient{result: func(*fetch.Request) (*fetch.Response, error) { return ok() }}
	if _, err := newClient(Route{ID: "a", Client: down}, Route{ID: "b", Client: backup}).Do(context.Background(), session, post); err != nil || backup.calls != 1 {
		t.Fatalf("connect failure must fall back even for POST: err=%v b=%d", err, backup.calls)
	}

	reset := &stubClient{result: func(*fetch.Request) (*fetch.Response, error) { return nil, midStreamReset }}
	backup2 := &stubClient{result: func(*fetch.Request) (*fetch.Response, error) { return ok() }}
	if _, err := newClient(Route{ID: "a", Client: reset}, Route{ID: "b", Client: backup2}).Do(context.Background(), session, post); err == nil || backup2.calls != 0 {
		t.Fatalf("POST that may have reached the site must not be resent: err=%v b=%d", err, backup2.calls)
	}

	get := &fetch.Request{Method: "GET", URL: "https://shop.test/x"}
	reset2 := &stubClient{result: func(*fetch.Request) (*fetch.Response, error) { return nil, midStreamReset }}
	backup3 := &stubClient{result: func(*fetch.Request) (*fetch.Response, error) { return ok() }}
	if _, err := newClient(Route{ID: "a", Client: reset2}, Route{ID: "b", Client: backup3}).Do(context.Background(), session, get); err != nil || backup3.calls != 1 {
		t.Fatalf("idempotent GET may be retried on another route: err=%v", err)
	}
}

func TestConnectFailureCoolsRouteForAllSites(t *testing.T) {
	health := NewHealth()
	down := &stubClient{result: func(*fetch.Request) (*fetch.Response, error) { return nil, connectRefused }}
	backup := &stubClient{result: func(*fetch.Request) (*fetch.Response, error) { return ok() }}
	session, _ := fetch.NewSession("https://shop.test", nil, "")
	for _, site := range []string{"s1", "s2"} {
		client := &Client{SiteID: site, Routes: []Route{{ID: "a", Client: down, Proxy: "http://127.0.0.1:1"}, {ID: "b", Client: backup}}, Health: health}
		if _, err := client.Do(context.Background(), session, &fetch.Request{URL: "https://" + site + ".test/"}); err != nil {
			t.Fatal(err)
		}
	}
	if down.calls != 1 {
		t.Fatalf("a dead proxy must be skipped by every site while cooling, got %d calls", down.calls)
	}
}

// 直连拨号失败说明目标站点挂了：只冷却该 host，其它站点照常直连。
func TestDirectDialFailureCoolsOnlyThatHost(t *testing.T) {
	health := NewHealth()
	dialFailed := &url.Error{Op: "Get", URL: "https://down.test/", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}
	direct := &stubClient{result: func(req *fetch.Request) (*fetch.Response, error) {
		if strings.Contains(req.URL, "down.test") {
			return nil, dialFailed
		}
		return ok()
	}}
	session, _ := fetch.NewSession("https://down.test", nil, "")
	client := &Client{SiteID: "s1", Routes: []Route{{ID: Direct, Client: direct}}, Health: health}
	if _, err := client.Do(context.Background(), session, &fetch.Request{URL: "https://down.test/"}); err == nil {
		t.Fatal("want dial error")
	}
	if wait := health.Cooling("s1", Direct, "https://down.test/x"); wait <= 0 {
		t.Fatal("the unreachable host should cool down")
	}
	other := &Client{SiteID: "s2", Routes: []Route{{ID: Direct, Client: direct}}, Health: health}
	if _, err := other.Do(context.Background(), session, &fetch.Request{URL: "https://up.test/"}); err != nil {
		t.Fatalf("other sites must keep using direct: %v", err)
	}
}

func TestRateLimitWithoutRetryAfterUsesDefaultCooldown(t *testing.T) {
	health := NewHealth()
	only := &stubClient{result: func(*fetch.Request) (*fetch.Response, error) {
		return &fetch.Response{StatusCode: 429}, &fetch.RateLimitError{}
	}}
	session, _ := fetch.NewSession("https://shop.test", nil, "")
	client := &Client{SiteID: "s", Routes: []Route{{ID: "a", Client: only}}, Health: health}
	_, _ = client.Do(context.Background(), session, &fetch.Request{URL: "https://shop.test/orders/create"})
	if wait := health.Cooling("s", "a", "https://shop.test/orders/create"); wait < 59*time.Minute {
		t.Fatalf("missing Retry-After must cool for the default hour, got %s", wait)
	}
	client.RateLimitCooldown = 6 * time.Hour
	client.Health = NewHealth()
	_, _ = client.Do(context.Background(), session, &fetch.Request{URL: "https://shop.test/orders/create"})
	if wait := client.Health.Cooling("s", "a", "https://shop.test/orders/create"); wait < 5*time.Hour {
		t.Fatalf("site-specific cooldown not applied, got %s", wait)
	}
}

func TestHealthPrunesExpiredKeys(t *testing.T) {
	health := NewHealth()
	now := time.Now()
	health.now = func() time.Time { return now }
	health.cool(coolKey{route: "a"}, time.Minute)
	health.cool(coolKey{route: "b"}, time.Hour)
	now = now.Add(2 * time.Minute)
	health.cool(coolKey{route: "c"}, time.Minute)
	if len(health.until) != 2 {
		t.Fatalf("expired keys must be dropped, got %v", health.until)
	}
	if health.Cooling("s", "b", "https://x/") <= 0 || health.Cooling("s", "a", "https://x/") != 0 {
		t.Fatal("unexpected cooling state")
	}
}

type stubBrowser struct {
	proxies []string
	reqs    []*fetch.Request
}

func (b *stubBrowser) Do(_ context.Context, session *fetch.Session, req *fetch.Request, proxyURL string) (*fetch.Response, error) {
	b.proxies = append(b.proxies, proxyURL)
	b.reqs = append(b.reqs, req)
	session.SetCookies([]*http.Cookie{{Name: "cf_clearance", Value: "ok"}})
	return &fetch.Response{StatusCode: 200, Body: []byte("browser")}, nil
}

func TestBrowserSolvesChallengeOnSameRoute(t *testing.T) {
	route := &stubClient{result: func(req *fetch.Request) (*fetch.Response, error) {
		if req.JSON != nil {
			return &fetch.Response{StatusCode: 200, Body: []byte("http")}, nil
		}
		return &fetch.Response{StatusCode: 403}, &fetch.ChallengeError{Kind: "cloudflare", URL: req.URL}
	}}
	browser := &stubBrowser{}
	client := &Client{SiteID: "s", Routes: []Route{{ID: "p", Client: route, Proxy: "socks5://127.0.0.1:1080"}}, Health: NewHealth(), Browser: browser}
	session, _ := fetch.NewSession("https://shop.test", nil, "")
	resp, err := client.Do(context.Background(), session, &fetch.Request{URL: "https://shop.test/store"})
	if err != nil || string(resp.Body) != "browser" || browser.proxies[0] != "socks5://127.0.0.1:1080" {
		t.Fatalf("plain GET must be served by the browser through the route proxy: %v %v", err, browser.proxies)
	}
	// JSON 请求浏览器发不了：浏览器只过质询，再用原客户端重发。
	route.result = func(req *fetch.Request) (*fetch.Response, error) {
		if session.Cookie("cf_clearance") == "ok" {
			return &fetch.Response{StatusCode: 200, Body: []byte("http")}, nil
		}
		return &fetch.Response{StatusCode: 403}, &fetch.ChallengeError{Kind: "cloudflare", URL: req.URL}
	}
	session, _ = fetch.NewSession("https://shop.test", nil, "")
	resp, err = client.Do(context.Background(), session, &fetch.Request{Method: "POST", URL: "https://shop.test/api", JSON: []byte(`{}`)})
	if err != nil || string(resp.Body) != "http" || browser.reqs[1].Method != "GET" || browser.reqs[1].JSON != nil {
		t.Fatalf("json request must be resent by the route client after clearance: %v %+v", err, browser.reqs[1])
	}
}

func TestRegistryApplyEvictsClientsAndResetsCooling(t *testing.T) {
	health := NewHealth()
	registry := NewRegistry(&stubClient{}, time.Second, health)
	registry.Apply(Settings{Proxies: []Proxy{{ID: "p", URL: "http://127.0.0.1:1", Enabled: true}, {ID: "q", URL: "http://127.0.0.1:2", Enabled: true}}})
	_, _ = registry.Route("p")
	_, _ = registry.Route("q")
	health.cool(coolKey{route: "p"}, time.Hour)
	health.cool(coolKey{route: "q"}, time.Hour)
	registry.Apply(Settings{Proxies: []Proxy{{ID: "p", URL: "http://127.0.0.1:3", Enabled: true}, {ID: "q", URL: "http://127.0.0.1:2", Enabled: true}}})
	if len(registry.clients) != 1 {
		t.Fatalf("clients for unregistered urls must be dropped, got %d", len(registry.clients))
	}
	if health.Cooling("s", "p", "https://x/") != 0 {
		t.Fatal("a proxy whose url changed is a new egress and must not stay cooling")
	}
	if health.Cooling("s", "q", "https://x/") <= 0 {
		t.Fatal("unchanged proxy must keep its cooldown")
	}
}

func TestRegistryRoutes(t *testing.T) {
	direct := &stubClient{result: func(*fetch.Request) (*fetch.Response, error) { return ok() }}
	registry := NewRegistry(direct, time.Second, NewHealth())
	registry.Apply(Settings{Proxies: []Proxy{
		{ID: "dmit", Name: "dmit LA", URL: "socks5://127.0.0.1:10820", Enabled: true},
		{ID: "off", URL: "http://127.0.0.1:1", Enabled: false},
	}})
	ids := func(routes []Route) (out []string) {
		for _, route := range routes {
			out = append(out, route.ID)
		}
		return
	}
	routes, err := registry.Routes("s", nil)
	if err != nil || len(routes) != 1 || routes[0].ID != Direct {
		t.Fatalf("no preference and no default means direct: %v %v", ids(routes), err)
	}
	routes, _ = registry.Routes("s", []string{"dmit", "off", Direct, "dmit"})
	if got := ids(routes); len(got) != 2 || got[0] != "dmit" || got[1] != Direct {
		t.Fatalf("explicit chain must keep order, drop disabled and duplicates: %v", got)
	}
	if _, err := registry.Routes("s", []string{"off", "missing"}); !errors.Is(err, ErrNoEgress) {
		t.Fatalf("explicit chain with nothing usable must fail rather than leak to direct: %v", err)
	}
	registry.Apply(Settings{Proxies: []Proxy{{ID: "dmit", URL: "socks5://127.0.0.1:10820", Enabled: true}}, Default: []string{"dmit", Direct}})
	routes, _ = registry.Routes("s", nil)
	if got := ids(routes); len(got) != 2 || got[0] != "dmit" {
		t.Fatalf("default chain must apply to sites without their own: %v", got)
	}
	a, _ := registry.Routes("x", []string{"dmit"})
	b, _ := registry.Routes("y", []string{"dmit"})
	if a[0].Client != b[0].Client {
		t.Fatal("proxy clients must be shared per URL")
	}
}

func TestStaleClearanceDroppedBeforeBrowser(t *testing.T) {
	session, _ := fetch.NewSession("https://www.shop.test", nil, "")
	// 粘贴导入的主机 Cookie 与服务端下发的父域 Cookie 都要删掉。
	session.SetCookies([]*http.Cookie{{Name: "cf_clearance", Value: "stale"}, {Name: "sid", Value: "1"}})
	shop, _ := url.Parse("https://www.shop.test/")
	session.Jar().SetCookies(shop, []*http.Cookie{{Name: "cf_clearance", Value: "stale2", Domain: ".shop.test", Path: "/"}})
	route := &stubClient{result: func(req *fetch.Request) (*fetch.Response, error) {
		if session.Cookie("cf_clearance") != "" {
			return &fetch.Response{StatusCode: 403}, &fetch.ChallengeError{Kind: "cloudflare", URL: req.URL}
		}
		return &fetch.Response{StatusCode: 200, Body: []byte("http")}, nil
	}}
	browser := &stubBrowser{}
	client := &Client{SiteID: "s", Routes: []Route{{ID: "p", Client: route}}, Health: NewHealth(), Browser: browser}
	resp, err := client.Do(context.Background(), session, &fetch.Request{URL: "https://www.shop.test/store"})
	if err != nil || string(resp.Body) != "http" || len(browser.reqs) != 0 || route.calls != 2 {
		t.Fatalf("stale clearance must be dropped and the request retried without the browser: %v calls=%d browser=%d", err, route.calls, len(browser.reqs))
	}
	if session.Cookie("sid") != "1" {
		t.Fatal("other cookies must be kept")
	}
	// 没有 cf_clearance 时照常交给浏览器，不额外重试。
	route.calls = 0
	route.result = func(req *fetch.Request) (*fetch.Response, error) {
		return &fetch.Response{StatusCode: 403}, &fetch.ChallengeError{Kind: "cloudflare", URL: req.URL}
	}
	if _, err := client.Do(context.Background(), session, &fetch.Request{URL: "https://www.shop.test/store"}); err != nil || route.calls != 1 || len(browser.reqs) != 1 {
		t.Fatalf("challenge without clearance goes straight to the browser: %v calls=%d", err, route.calls)
	}
}
