package usvps24

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
)

// fakeLogin 模拟 Laravel + Inertia 登录：GET /login 下发 XSRF 与 Auth/Login 页；POST /login 校验 XSRF、Turnstile、密码。
func fakeLogin(t *testing.T, ordersHit *int) *httptest.Server {
	t.Helper()
	const version = "v1"
	write := func(w http.ResponseWriter, r *http.Request, component string, props map[string]any) {
		page, _ := json.Marshal(map[string]any{"component": component, "url": r.URL.Path, "version": version, "props": props})
		if r.Header.Get("X-Inertia") == "true" {
			w.Header().Set("X-Inertia", "true")
			_, _ = w.Write(page)
			return
		}
		fmt.Fprintf(w, `<div id="app" data-page="%s"></div>`, html.EscapeString(string(page)))
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "XSRF-TOKEN", Value: "x%3D", Path: "/"})
		switch {
		case r.URL.Path == "/login" && r.Method == http.MethodGet:
			props := map[string]any{"errors": map[string]any{}, "auth": map[string]any{"user": nil}, "turnstileSiteKey": "0xKEY"}
			if c, err := r.Cookie("cloud_session"); err == nil && c.Value == "in" {
				write(w, r, "Auth/Login", map[string]any{"errors": map[string]any{"email": "这些凭据与我们的记录不符。"}, "auth": map[string]any{"user": nil}, "turnstileSiteKey": "0xKEY"})
				return
			}
			write(w, r, "Auth/Login", props)
		case r.URL.Path == "/login" && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.Header.Get("X-XSRF-TOKEN") != "x=" || body["cf_turnstile_response"] != "TURNSTILE-OK" || body["remember"] != true {
				w.WriteHeader(419)
				return
			}
			if body["email"] != "me@example.com" || body["password"] != "right" {
				http.SetCookie(w, &http.Cookie{Name: "cloud_session", Value: "in", Path: "/"}) // 带回错误
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "cloud_session", Value: "authed", Path: "/"})
			http.Redirect(w, r, "/dashboard", http.StatusFound)
		case r.URL.Path == "/dashboard":
			write(w, r, "Dashboard", map[string]any{"errors": map[string]any{}, "auth": map[string]any{"user": map[string]any{"email": "me@example.com", "name": "me"}}})
		case r.URL.Path == "/orders/create":
			*ordersHit++
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			http.NotFound(w, r)
		}
	}))
}

func newLoginEnv(t *testing.T, server *httptest.Server) (*Provider, provider.Env, *tokenSolver) {
	t.Helper()
	site := domain.Site{ID: "usvps24", Name: "usvps24", Kind: Kind, BaseURL: server.URL}
	instance, _ := New(site)
	client, _ := fetch.NewHTTPClient(5*time.Second, "")
	session, _ := fetch.NewSession(server.URL, nil, "")
	slv := &tokenSolver{}
	return instance.(*Provider), provider.Env{Site: site, Client: client, Session: session, Solver: slv, Account: &domain.Account{ID: "a1"}}, slv
}

func TestUsvps24Login(t *testing.T) {
	ordersHit := 0
	server := fakeLogin(t, &ordersHit)
	defer server.Close()
	p, env, slv := newLoginEnv(t, server)
	info, err := p.Login(context.Background(), env, domain.Credentials{Username: "me@example.com", Password: "right"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Identity != "me@example.com" || env.Session.Cookie("cloud_session") != "authed" {
		t.Fatalf("unexpected login result %+v cookie=%q", info, env.Session.Cookie("cloud_session"))
	}
	if slv.challenge.SiteKey != "0xKEY" || !strings.HasSuffix(slv.challenge.PageURL, "/login") {
		t.Fatalf("turnstile not solved for login page: %+v", slv.challenge)
	}
	if ordersHit != 0 {
		t.Fatal("login must not touch the rate-limited order page")
	}
}

func TestUsvps24LoginWrongPassword(t *testing.T) {
	ordersHit := 0
	server := fakeLogin(t, &ordersHit)
	defer server.Close()
	p, env, _ := newLoginEnv(t, server)
	_, err := p.Login(context.Background(), env, domain.Credentials{Username: "me@example.com", Password: "nope"})
	if !errors.Is(err, provider.ErrLoginFailed) || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("want ErrLoginFailed with site message, got %v", err)
	}
}
