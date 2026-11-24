package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseCookiesFormats(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		want  map[string]string
		count int
	}{
		{"header", "laravel_session=abc; XSRF-TOKEN=x%3Dy; cf_clearance=zz", map[string]string{"laravel_session": "abc", "XSRF-TOKEN": "x%3Dy", "cf_clearance": "zz"}, 3},
		{"header with prefix", "Cookie: a=1; b=2", map[string]string{"a": "1", "b": "2"}, 2},
		{"json array", `[{"name":"WHMCSy","value":"s1","domain":".novixlink.com","path":"/","httpOnly":true,"expirationDate":1900000000},{"name":"cf_clearance","value":"c1"}]`, map[string]string{"WHMCSy": "s1", "cf_clearance": "c1"}, 2},
		{"netscape", "# Netscape HTTP Cookie File\n.usvps24.com\tTRUE\t/\tTRUE\t1900000000\tlaravel_session\tv1\n#HttpOnly_.usvps24.com\tTRUE\t/\tTRUE\t0\tremember_web\tv2\n", map[string]string{"laravel_session": "v1", "remember_web": "v2"}, 2},
		{"duplicate keeps last", "a=1; a=2", map[string]string{"a": "2"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cookies, err := ParseCookies(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if len(cookies) != tc.count {
				t.Fatalf("want %d cookies, got %d", tc.count, len(cookies))
			}
			for _, cookie := range cookies {
				if tc.want[cookie.Name] != cookie.Value {
					t.Fatalf("cookie %s=%q, want %q", cookie.Name, cookie.Value, tc.want[cookie.Name])
				}
			}
		})
	}
}

func TestParseCookiesEmptyAndInvalid(t *testing.T) {
	if _, err := ParseCookies("   "); !errors.Is(err, ErrNoCookies) {
		t.Fatalf("want ErrNoCookies, got %v", err)
	}
	if _, err := ParseCookies("novalue"); !errors.Is(err, ErrNoCookies) {
		t.Fatalf("want ErrNoCookies for garbage, got %v", err)
	}
	if _, err := ParseCookies("[not json"); err == nil {
		t.Fatal("want json error")
	}
}

func TestSessionScopesPastedDomainCookies(t *testing.T) {
	cookies, _ := ParseCookies(`[{"name":"s","value":"1","domain":".example.com"}]`)
	session, err := NewSession("https://shop.example.com", cookies, "")
	if err != nil {
		t.Fatal(err)
	}
	if session.Cookie("s") != "1" {
		t.Fatal("pasted cookie not visible in session")
	}
	if session.UserAgent() != DefaultUserAgent {
		t.Fatal("default UA not applied")
	}
}

func TestHTTPClientClassifiesChallengeAndRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cf":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("<html><head><title>Just a moment...</title></head><script src=/cdn-cgi/challenge-platform/x></script></html>"))
		case "/limited":
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
		case "/limited-silent":
			w.WriteHeader(http.StatusTooManyRequests)
		case "/echo":
			if r.Header.Get("User-Agent") != "UA-1" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "fresh", Value: "yes", Path: "/"})
			_, _ = w.Write([]byte("ok:" + r.Header.Get("Cookie")))
		}
	}))
	defer server.Close()
	client, err := NewHTTPClient(5*time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	session, _ := NewSession(server.URL, []*http.Cookie{{Name: "sid", Value: "42"}}, "UA-1")
	ctx := context.Background()
	if _, err := client.Do(ctx, session, &Request{URL: server.URL + "/cf"}); !IsChallenge(err) {
		t.Fatalf("want challenge error, got %v", err)
	}
	_, err = client.Do(ctx, session, &Request{URL: server.URL + "/limited"})
	var rateLimit *RateLimitError
	if !errors.As(err, &rateLimit) || rateLimit.RetryAfter != 7*time.Second {
		t.Fatalf("want rate limit 7s, got %v", err)
	}
	// 未带 Retry-After 时为 0，由出口层套用自己的默认冷却（不能悄悄变成 1 分钟）。
	_, err = client.Do(ctx, session, &Request{URL: server.URL + "/limited-silent"})
	if !errors.As(err, &rateLimit) || rateLimit.RetryAfter != 0 {
		t.Fatalf("want rate limit without duration, got %v", err)
	}
	resp, err := client.Do(ctx, session, &Request{URL: server.URL + "/echo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resp.Body), "sid=42") {
		t.Fatalf("session cookie not sent: %s", resp.Body)
	}
	if session.Cookie("fresh") != "yes" {
		t.Fatal("response cookie not stored in session jar")
	}
}

func TestParseRetryAfter(t *testing.T) {
	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	for value, want := range map[string]time.Duration{"": 0, "abc": 0, "-5": 0, "120": 2 * time.Minute} {
		if got := parseRetryAfter(value); got != want {
			t.Fatalf("%q: got %s want %s", value, got, want)
		}
	}
	if got := parseRetryAfter(future); got < 59*time.Minute || got > time.Hour {
		t.Fatalf("http date: got %s", got)
	}
}

// 空代理即直连：不能因为进程环境里有 HTTPS_PROXY 就悄悄换出口。
func TestHTTPClientIgnoresEnvProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	client, err := NewHTTPClient(time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	if client.transport.(*http.Transport).Proxy != nil {
		t.Fatal("empty proxy url must not read environment proxies")
	}
}

func TestFlareSolverrUsesRouteProxy(t *testing.T) {
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"status":"ok","solution":{"url":"https://shop.test/","status":200,"response":"ok","userAgent":"UA-B","cookies":[{"name":"cf_clearance","value":"cc"}]}}`))
	}))
	defer server.Close()
	client := NewFlareSolverrClient(server.URL, time.Second)
	session, _ := NewSession("https://shop.test", nil, "")
	if _, err := client.Do(context.Background(), session, &Request{URL: "https://shop.test/"}, "socks5://u:p@127.0.0.1:1080"); err != nil {
		t.Fatal(err)
	}
	proxy, _ := got["proxy"].(map[string]any)
	if proxy["url"] != "socks5://127.0.0.1:1080" || proxy["username"] != "u" || proxy["password"] != "p" {
		t.Fatalf("proxy not forwarded: %v", got["proxy"])
	}
	if session.Cookie("cf_clearance") != "cc" || session.UserAgent() != "UA-B" {
		t.Fatal("browser cookies / UA not written back to session")
	}
	if _, err := client.Do(context.Background(), session, &Request{Method: http.MethodPost, URL: "https://shop.test/", JSON: []byte(`{}`)}, ""); err == nil {
		t.Fatal("json body must be rejected")
	}
}
