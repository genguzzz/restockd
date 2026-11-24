package fetch

import (
	"context"
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

func TestDirectClientClassifiesChallengeAndRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cf":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("<html><head><title>Just a moment...</title></head><script src=/cdn-cgi/challenge-platform/x></script></html>"))
		case "/limited":
			w.Header().Set("Retry-After", "7")
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
	client, err := NewDirectClient(5*time.Second, "")
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

type stubClient struct {
	calls int
	err   error
}

func (s *stubClient) Do(context.Context, *Session, *Request) (*Response, error) {
	s.calls++
	return &Response{StatusCode: 200}, s.err
}

func TestHybridFallsBackOnlyOnChallenge(t *testing.T) {
	session, _ := NewSession("https://x.test", nil, "")
	primary := &stubClient{err: &ChallengeError{Kind: "cloudflare"}}
	fallback := &stubClient{}
	hybrid := &HybridClient{Primary: primary, Fallback: fallback}
	if _, err := hybrid.Do(context.Background(), session, &Request{}); err != nil {
		t.Fatalf("fallback should succeed: %v", err)
	}
	if fallback.calls != 1 {
		t.Fatalf("fallback calls %d", fallback.calls)
	}
	primary.err = &HTTPError{StatusCode: 500}
	if _, err := hybrid.Do(context.Background(), session, &Request{}); err == nil {
		t.Fatal("non-challenge error must not fall back")
	}
	if fallback.calls != 1 {
		t.Fatalf("fallback called for non-challenge error")
	}
}
