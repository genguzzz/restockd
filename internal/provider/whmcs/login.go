package whmcs

import (
	"cmp"
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
)

var _ provider.Authenticator = (*Provider)(nil)

// loginPaths WHMCS 8（/login 与 rp 路由）与旧版（clientarea.php 内嵌表单）的登录入口，依次尝试。
var loginPaths = []string{"/login", "/index.php?rp=/login", "/clientarea.php"}

// captchaMessagePattern 登录页的验证码报错（如 WHMCS「Complete the captcha and try again.」）。
var captchaMessagePattern = regexp.MustCompile(`(?i)captcha|验证码|驗證碼|人机验证|人機驗證`)

// twoFactorPattern 登录后要求二次验证（暂不支持自动完成）。
var twoFactorPattern = regexp.MustCompile(`(?i)two[- ]factor|2fa|二次验证|二次驗證|双重验证|雙重驗證|两步验证|兩步驟驗證|兩步驗證`)

// Login 实现 provider.Authenticator：提交 WHMCS 登录表单，验证码交给 Solver，成功后以 CheckSession 确认。
func (p *Provider) Login(ctx context.Context, env provider.Env, creds domain.Credentials) (provider.SessionInfo, error) {
	form, loginPage, err := p.findLoginForm(ctx, env)
	if err != nil {
		return provider.SessionInfo{}, err
	}
	if form == nil {
		// 没有登录表单且已带退出链接：会话本来就有效。
		if loginPage != nil && loginPage.doc.Find(`a[href*="logout"]`).Length() > 0 {
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
		values.Set("token", htmlutil.CSRFToken(loginPage.doc, loginPage.resp.Body))
	}
	formURL := loginPage.resp.URL
	if captcha, _, ok := detectAddonCaptcha(loginPage.resp.Body, captchaCheckout); ok {
		p.rememberCheckoutCaptcha(captcha)
	}
	solvedBy, err := solveCaptcha(ctx, env, loginPage, values, captchaLogin, formURL, env.Site.Name+" 登录", nil)
	if err != nil {
		return provider.SessionInfo{}, err
	}
	action := htmlutil.Resolve(formURL, form.AttrOr("action", formURL))
	submitted, err := fetchPage(ctx, env, &fetch.Request{Method: http.MethodPost, URL: action, Form: values})
	if err != nil {
		return provider.SessionInfo{}, err
	}
	result := submitted.doc
	if passwordForm(result) != nil {
		message := errorMessage(result)
		if message == "" {
			message = "still on login page"
		}
		if captchaMessagePattern.MatchString(message) {
			// 验证码没通过不代表密码错：不能按 ErrLoginFailed 处理（那会标记凭据被拒、停掉自动登录）。
			return provider.SessionInfo{}, fmt.Errorf("login captcha not accepted (solved by %s): %s", cmp.Or(solvedBy, "none"), message)
		}
		return provider.SessionInfo{}, fmt.Errorf("%w: %s", provider.ErrLoginFailed, message)
	}
	if result.Find(`a[href*="logout"]`).Length() == 0 && twoFactorPattern.Match(submitted.resp.Body) {
		return provider.SessionInfo{}, fmt.Errorf("%w: two-factor authentication is enabled; disable it or paste cookies", provider.ErrLoginFailed)
	}
	info, err := p.CheckSession(ctx, env)
	if errors.Is(err, provider.ErrSessionExpired) {
		// 带上表单提交后落到的页面，便于判断是二次验证、安全提示还是会话没保持住。
		landed := cmp.Or(htmlutil.CleanText(result.Find("title").First().Text()), "untitled")
		if message := errorMessage(result); message != "" {
			landed += " / " + message
		}
		return info, fmt.Errorf("%w: site accepted the form but the session is not logged in (landed on %s: %s)", provider.ErrLoginFailed, submitted.resp.URL, landed)
	}
	return info, err
}

// findLoginForm 依次打开登录入口，返回第一个带密码框的表单及其所在页面；已登录（有退出链接）时 form 为 nil。
func (p *Provider) findLoginForm(ctx context.Context, env provider.Env) (*goquery.Selection, *page, error) {
	var last *page
	var lastErr error
	for _, path := range loginPaths {
		loaded, err := fetchPage(ctx, env, &fetch.Request{Method: http.MethodGet, URL: p.baseURL + path})
		if err != nil {
			lastErr = err
			continue
		}
		last, lastErr = loaded, nil
		if form := passwordForm(loaded.doc); form != nil {
			return form, loaded, nil
		}
		if loaded.doc.Find(`a[href*="logout"]`).Length() > 0 {
			return nil, loaded, nil
		}
	}
	return nil, last, lastErr
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
