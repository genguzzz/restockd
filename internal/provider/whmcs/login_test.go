package whmcs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
)

// fakeLoginSite 最小 WHMCS 登录流程：/login 表单（带 token），正确密码种 WHMCSsession 并跳客户中心。
func fakeLoginSite(t *testing.T, twoFactor bool) *httptest.Server {
	t.Helper()
	const token = "tok123"
	loggedIn := func(r *http.Request) bool {
		cookie, err := r.Cookie("WHMCSsession")
		return err == nil && cookie.Value == "ok"
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch {
		case r.URL.Path == "/login" && r.Method == http.MethodGet:
			fmt.Fprint(w, `<html><body><div class="alert alert-danger hidden">template</div>
<form method="post" action="/login" class="login-form"><input type="hidden" name="token" value="`+token+`"/>
<input type="email" name="username"/><input type="password" name="password"/><input type="checkbox" name="rememberme"/></form></body></html>`)
		case r.URL.Path == "/login" && r.Method == http.MethodPost:
			if r.PostForm.Get("token") != token || r.PostForm.Get("username") != "me@example.com" || r.PostForm.Get("password") != "right" || r.PostForm.Get("rememberme") != "on" {
				fmt.Fprint(w, `<html><body><div class="alert alert-danger">登录信息有误，请重试</div><form method="post" action="/login"><input type="hidden" name="token" value="`+token+`"/><input name="username"/><input type="password" name="password"/></form></body></html>`)
				return
			}
			if twoFactor {
				fmt.Fprint(w, `<html><body><h2>Two-Factor Authentication</h2><form method="post"><input name="key"/></form></body></html>`)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "WHMCSsession", Value: "ok", Path: "/"})
			http.Redirect(w, r, "/clientarea.php", http.StatusFound)
		case r.URL.Path == "/clientarea.php":
			if !loggedIn(r) {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			fmt.Fprint(w, `<html><body><a href="/logout.php">Logout</a><span class="client-name">Me</span><div class="client-credit">¥12.00</div></body></html>`)
		case strings.HasPrefix(r.URL.Path, "/store/"):
			if loggedIn(r) {
				fmt.Fprint(w, `<html><body><a href="/logout.php">Logout</a></body></html>`)
				return
			}
			fmt.Fprint(w, `<html><body><a href="/login">Login</a></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func newLoginEnv(t *testing.T, server *httptest.Server) (*Provider, provider.Env) {
	t.Helper()
	instance, err := New(domain.Site{ID: "aa", Name: "AA", BaseURL: server.URL, Config: map[string]string{"groups": "srv", "currency": "CNY"}})
	if err != nil {
		t.Fatal(err)
	}
	client, _ := fetch.NewDirectClient(5*time.Second, "")
	session, _ := fetch.NewSession(server.URL, nil, "")
	return instance.(*Provider), provider.Env{Site: domain.Site{ID: "aa", Name: "AA", BaseURL: server.URL}, Client: client, Session: session}
}

func TestLoginSuccess(t *testing.T) {
	server := fakeLoginSite(t, false)
	defer server.Close()
	p, env := newLoginEnv(t, server)
	info, err := p.Login(context.Background(), env, domain.Credentials{Username: "me@example.com", Password: "right"})
	if err != nil {
		t.Fatal(err)
	}
	if env.Session.Cookie("WHMCSsession") != "ok" || info.Identity != "Me" || info.Balance == nil || info.Balance.Cents != 1200 {
		t.Fatalf("unexpected login result %+v cookie=%q", info, env.Session.Cookie("WHMCSsession"))
	}
	// 已登录再调用：没有登录表单但有退出链接，直接视为成功。
	if _, err := p.Login(context.Background(), env, domain.Credentials{Username: "me@example.com", Password: "right"}); err != nil {
		t.Fatalf("login when already logged in: %v", err)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	server := fakeLoginSite(t, false)
	defer server.Close()
	p, env := newLoginEnv(t, server)
	_, err := p.Login(context.Background(), env, domain.Credentials{Username: "me@example.com", Password: "nope"})
	if !errors.Is(err, provider.ErrLoginFailed) || !strings.Contains(err.Error(), "登录信息有误") {
		t.Fatalf("want ErrLoginFailed with site message, got %v", err)
	}
}

func TestLoginTwoFactorUnsupported(t *testing.T) {
	server := fakeLoginSite(t, true)
	defer server.Close()
	p, env := newLoginEnv(t, server)
	_, err := p.Login(context.Background(), env, domain.Credentials{Username: "me@example.com", Password: "right"})
	if !errors.Is(err, provider.ErrLoginFailed) || !strings.Contains(err.Error(), "two-factor") {
		t.Fatalf("want two-factor error, got %v", err)
	}
}
