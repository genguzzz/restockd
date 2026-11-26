package whmcs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/provider/htmlutil"
	"github.com/genguzzz/restockd/internal/solver"
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
	client, _ := fetch.NewHTTPClient(5*time.Second, "")
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

// SixtyNet 登录页：WHMCS 自带隐形 reCAPTCHA（var recaptcha 全站配置 + btn-recaptcha 按钮），没有 data-sitekey。
func TestDetectNativeRecaptcha(t *testing.T) {
	body, err := os.ReadFile("../../../testdata/whmcs/sixtynet_login.html")
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := htmlutil.Parse(body)
	captcha, ok := detectCaptcha(doc, body, captchaLogin)
	if !ok || captcha.kind != solver.KindRecaptchaV2 || captcha.siteKey != "6LdLT5UqAAAAAGOMrS3BdN1ryxq-BO9uo74QKIT4" || captcha.field != "g-recaptcha-response" {
		t.Fatalf("native recaptcha: %v %+v", ok, captcha)
	}
	// btn-recaptcha-invisible：隐形 v2，必须告诉 Solver 按 invisible 求解（否则 token 被拒：Captcha verification incorrect）。
	if challenge := captcha.challenge("https://example.com/login", "login"); !challenge.Invisible || challenge.Kind != solver.KindRecaptchaV2 {
		t.Fatalf("SixtyNet login is invisible reCAPTCHA v2: %+v", challenge)
	}
	// 同一份全站配置，但页面没有带 btn-recaptcha 的按钮：这个表单不要求验证码。
	plain := []byte(strings.ReplaceAll(string(body), "btn-recaptcha", "btn-plain"))
	doc, _ = htmlutil.Parse(plain)
	if _, ok := detectCaptcha(doc, plain, captchaLogin); ok {
		t.Fatal("config without a captcha button must not require a captcha")
	}
}

// 站点回「请完成验证码」：不是密码错误，不能返回 ErrLoginFailed（否则账号被标记凭据被拒）。
func TestLoginCaptchaRejectionIsNotCredentialFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body><div class="alert alert-danger">Complete the captcha and try again.</div>
<form method="post" action="/login"><input type="hidden" name="token" value="t"/><input name="username"/><input type="password" name="password"/></form></body></html>`)
	}))
	defer server.Close()
	p, env := newLoginEnv(t, server)
	_, err := p.Login(context.Background(), env, domain.Credentials{Username: "me@example.com", Password: "right"})
	if err == nil || errors.Is(err, provider.ErrLoginFailed) || !strings.Contains(err.Error(), "captcha") {
		t.Fatalf("captcha rejection must not be a credential failure: %v", err)
	}
}

// 站点没开短网址、重启后还没轮询过目录：会话校验遇到 /store/... 404 要切到 index.php?rp= 重试，不能误判失效。
func TestCheckSessionFallsBackToRouteParam(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/store/srv" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `<html><body><a href="/logout.php">Logout</a></body></html>`)
	}))
	defer server.Close()
	p, env := newLoginEnv(t, server)
	if _, err := p.CheckSession(context.Background(), env); err != nil {
		t.Fatalf("404 on the short URL must fall back to index.php?rp=: %v", err)
	}
	if got := p.GroupURL("srv"); got != server.URL+"/index.php?rp=/store/srv" {
		t.Fatalf("fallback must be remembered, got %s", got)
	}
}

// 商品组页面不放退出链接的主题（SixtyNet Lagom2）：以客户中心为准；客户中心是登录表单则仍判失效。
func TestCheckSessionFallsBackToClientArea(t *testing.T) {
	loggedIn := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/clientarea.php" && loggedIn:
			fmt.Fprint(w, `<html><body><a href="/logout.php">Logout</a><span class="client-name">Me</span></body></html>`)
		case r.URL.Path == "/clientarea.php":
			fmt.Fprint(w, `<html><body><a href="/logout.php">x</a><form><input name="username"/><input type="password" name="password"/></form></body></html>`)
		default:
			fmt.Fprint(w, `<html><body><a href="/clientarea.php">Client Area</a></body></html>`)
		}
	}))
	defer server.Close()
	p, env := newLoginEnv(t, server)
	info, err := p.CheckSession(context.Background(), env)
	if err != nil || info.Identity != "Me" {
		t.Fatalf("logged-in client area must count as a session: %v %+v", err, info)
	}
	loggedIn = false
	if _, err := p.CheckSession(context.Background(), env); !errors.Is(err, provider.ErrSessionExpired) {
		t.Fatalf("client area showing a login form means expired, got %v", err)
	}
}
