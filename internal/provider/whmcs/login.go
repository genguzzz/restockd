package whmcs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/PuerkitoBio/goquery"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/provider/htmlutil"
	"github.com/genguzzz/restockd/internal/solver"
)

var _ provider.Authenticator = (*Provider)(nil)

// loginPaths WHMCS 8（/login 与 rp 路由）与旧版（clientarea.php 内嵌表单）的登录入口，依次尝试。
var loginPaths = []string{"/login", "/index.php?rp=/login", "/clientarea.php"}

// twoFactorPattern 登录后要求二次验证（暂不支持自动完成）。
var twoFactorPattern = regexp.MustCompile(`(?i)two[- ]factor|2fa|二次验证|双重验证|两步验证`)

// Login 实现 provider.Authenticator：提交 WHMCS 登录表单，验证码交给 Solver，成功后以 CheckSession 确认。
func (p *Provider) Login(ctx context.Context, env provider.Env, creds domain.Credentials) (provider.SessionInfo, error) {
	form, formURL, doc, err := p.findLoginForm(ctx, env)
	if err != nil {
		return provider.SessionInfo{}, err
	}
	if form == nil {
		// 没有登录表单且已带退出链接：会话本来就有效。
		if doc != nil && doc.Find(`a[href*="logout"]`).Length() > 0 {
			return p.CheckSession(ctx, env)
		}
		return provider.SessionInfo{}, errors.New("login form not found")
	}
	values := htmlutil.FormValues(form)
	userField := "username"
	if form.Find(`input[name="username"]`).Length() == 0 && form.Find(`input[name="email"]`).Length() > 0 {
		userField = "email"
	}
	values.Set(userField, creds.Username)
	values.Set(form.Find(`input[type="password"]`).First().AttrOr("name", "password"), creds.Password)
	values.Set("rememberme", "on")
	if values.Get("token") == "" {
		values.Set("token", htmlutil.CSRFToken(doc, nil))
	}
	if kind, siteKey, found := htmlutil.DetectCaptcha(doc); found {
		if env.Solver == nil {
			return provider.SessionInfo{}, fmt.Errorf("login requires %s but no solver configured", kind)
		}
		solution, err := env.Solver.Solve(ctx, solver.Challenge{Kind: kind, SiteKey: siteKey, PageURL: formURL, SiteID: env.Site.ID, AccountID: accountID(env), Purpose: env.Site.Name + " 登录"})
		if err != nil {
			return provider.SessionInfo{}, fmt.Errorf("solve %s: %w", kind, err)
		}
		values.Set(htmlutil.CaptchaField(kind), solution.Token)
	}
	action := htmlutil.Resolve(formURL, form.AttrOr("action", formURL))
	resp, err := env.Client.Do(ctx, env.Session, &fetch.Request{Method: http.MethodPost, URL: action, Form: values})
	if err != nil {
		return provider.SessionInfo{}, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return provider.SessionInfo{}, &fetch.HTTPError{StatusCode: resp.StatusCode, URL: resp.URL}
	}
	result, err := htmlutil.Parse(resp.Body)
	if err != nil {
		return provider.SessionInfo{}, err
	}
	if passwordForm(result) != nil {
		message := errorMessage(result)
		if message == "" {
			message = "still on login page"
		}
		return provider.SessionInfo{}, fmt.Errorf("%w: %s", provider.ErrLoginFailed, message)
	}
	if result.Find(`a[href*="logout"]`).Length() == 0 && twoFactorPattern.Match(resp.Body) {
		return provider.SessionInfo{}, fmt.Errorf("%w: two-factor authentication is enabled; disable it or paste cookies", provider.ErrLoginFailed)
	}
	info, err := p.CheckSession(ctx, env)
	if errors.Is(err, provider.ErrSessionExpired) {
		return info, fmt.Errorf("%w: site accepted the form but the session is not logged in", provider.ErrLoginFailed)
	}
	return info, err
}

// findLoginForm 依次打开登录入口，返回第一个带密码框的表单。
func (p *Provider) findLoginForm(ctx context.Context, env provider.Env) (*goquery.Selection, string, *goquery.Document, error) {
	var lastDoc *goquery.Document
	var lastErr error
	for _, path := range loginPaths {
		resp, err := env.Client.Do(ctx, env.Session, &fetch.Request{Method: http.MethodGet, URL: p.baseURL + path})
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= http.StatusBadRequest {
			lastErr = &fetch.HTTPError{StatusCode: resp.StatusCode, URL: resp.URL}
			continue
		}
		doc, err := htmlutil.Parse(resp.Body)
		if err != nil {
			lastErr = err
			continue
		}
		lastDoc, lastErr = doc, nil
		if form := passwordForm(doc); form != nil {
			return form, resp.URL, doc, nil
		}
		if doc.Find(`a[href*="logout"]`).Length() > 0 {
			return nil, resp.URL, doc, nil
		}
	}
	return nil, "", lastDoc, lastErr
}

// passwordForm 页面上带密码框与用户名 / 邮箱框的表单（排除注册、改密等多密码框表单）。
func passwordForm(doc *goquery.Document) *goquery.Selection {
	var found *goquery.Selection
	doc.Find("form").EachWithBreak(func(_ int, form *goquery.Selection) bool {
		if form.Find(`input[type="password"]`).Length() == 1 && form.Find(`input[name="username"], input[name="email"]`).Length() > 0 {
			found = form
			return false
		}
		return true
	})
	return found
}
