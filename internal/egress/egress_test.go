package egress

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
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
		client := &Client{SiteID: site, Routes: []Route{{ID: "a", Client: down}, {ID: "b", Client: backup}}, Health: health}
		if _, err := client.Do(context.Background(), session, &fetch.Request{URL: "https://shop.test/"}); err != nil {
			t.Fatal(err)
		}
	}
	if down.calls != 1 {
		t.Fatalf("a dead proxy must be skipped by every site while cooling, got %d calls", down.calls)
	}
}

func TestRegistryRoutes(t *testing.T) {
	direct := &stubClient{result: func(*fetch.Request) (*fetch.Response, error) { return ok() }}
	registry := NewRegistry(direct, time.Second)
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
