package whmcs

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"

	"github.com/PuerkitoBio/goquery"

	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/provider/htmlutil"
	"github.com/genguzzz/restockd/internal/solver"
)

// 验证码场景（与 megabre_turnstile 一类插件的 contexts / action 取值一致）。
const (
	captchaCheckout = "checkoutCompletion"
	captchaLogin    = "login"
)

// pageCaptcha 页面要求的验证码：类型、sitekey、Turnstile action、是否隐形 reCAPTCHA，以及 token 与附加字段的表单名。
type pageCaptcha struct {
	kind      solver.Kind
	siteKey   string
	invisible bool
	action    string
	field     string
	extra     url.Values
}

// captchaAddonConfig JS 注入式验证码插件的配置（如 WHMCS 的 megabre_turnstile：window.novixCaptcha = {...}），
// 页面上没有 data-sitekey 控件，只能读这份配置。
type captchaAddonConfig struct {
	Contexts        []string `json:"contexts"`
	SiteKey         string   `json:"siteKey"`
	ResponseField   string   `json:"responseField"`
	ProviderField   string   `json:"providerField"`
	InitialProvider string   `json:"initialProvider"`
}

var windowObjectPattern = regexp.MustCompile(`window\.[A-Za-z_$][\w$]*\s*=\s*\{`)

// detectCaptcha 先找插件配置（仅当 context 在其保护范围内），再退回常规 data-sitekey 控件。
func detectCaptcha(doc *goquery.Document, body []byte, context string) (pageCaptcha, bool) {
	if captcha, configured, ok := detectAddonCaptcha(body, context); configured {
		return captcha, ok
	}
	if kind, siteKey, found := htmlutil.DetectCaptcha(doc); found {
		return pageCaptcha{kind: kind, siteKey: siteKey, invisible: kind == solver.KindRecaptchaV2 && htmlutil.InvisibleRecaptcha(doc), field: htmlutil.CaptchaField(kind)}, true
	}
	return nativeCaptcha(doc, body)
}

// nativeRecaptchaPattern WHMCS 自带验证码的全站配置：var recaptcha = {"siteKey":"...","apiObject":"grecaptcha",...}。
var nativeRecaptchaPattern = regexp.MustCompile(`var\s+recaptcha\s*=\s*\{`)

// nativeCaptcha WHMCS 自带的（隐形）reCAPTCHA / hCaptcha：页面上没有 data-sitekey 控件，sitekey 在全站配置里；
// 只有当前页的提交按钮带 btn-recaptcha 时这个表单才要求验证码。
func nativeCaptcha(doc *goquery.Document, body []byte) (pageCaptcha, bool) {
	if doc.Find(".btn-recaptcha").Length() == 0 {
		return pageCaptcha{}, false
	}
	loc := nativeRecaptchaPattern.FindIndex(body)
	if loc == nil {
		return pageCaptcha{}, false
	}
	var config struct {
		SiteKey   string `json:"siteKey"`
		APIObject string `json:"apiObject"`
	}
	if json.NewDecoder(bytes.NewReader(body[loc[1]-1:])).Decode(&config) != nil || config.SiteKey == "" {
		return pageCaptcha{}, false
	}
	kind := solver.KindRecaptchaV2
	if config.APIObject == "hcaptcha" {
		kind = solver.KindHCaptcha
	}
	return pageCaptcha{kind: kind, siteKey: config.SiteKey, invisible: kind == solver.KindRecaptchaV2 && htmlutil.InvisibleRecaptcha(doc), field: htmlutil.CaptchaField(kind)}, true
}

// detectAddonCaptcha 只看 JS 注入的插件配置（全站页面都带，商品页也能读到）。configured 表示页面有插件配置；
// ok 表示 context 在其保护范围内。
func detectAddonCaptcha(body []byte, context string) (captcha pageCaptcha, configured, ok bool) {
	for _, loc := range windowObjectPattern.FindAllIndex(body, -1) {
		var config captchaAddonConfig
		if json.NewDecoder(bytes.NewReader(body[loc[1]-1:])).Decode(&config) != nil || config.SiteKey == "" || config.ResponseField == "" {
			continue
		}
		if !slices.Contains(config.Contexts, context) {
			return pageCaptcha{}, true, false
		}
		captcha = pageCaptcha{kind: solver.KindTurnstile, siteKey: config.SiteKey, action: context, field: config.ResponseField, extra: url.Values{}}
		if config.ProviderField != "" {
			captcha.extra.Set(config.ProviderField, cmp.Or(config.InitialProvider, "turnstile"))
		}
		return captcha, true, true
	}
	return pageCaptcha{}, false, false
}

// rememberCheckoutCaptcha 记下结算页会要求的验证码，供下次锁单提前求解。
func (p *Provider) rememberCheckoutCaptcha(captcha pageCaptcha) {
	p.mu.Lock()
	p.checkoutCaptcha = &captcha
	p.mu.Unlock()
}

func (p *Provider) knownCheckoutCaptcha() (pageCaptcha, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.checkoutCaptcha == nil {
		return pageCaptcha{}, false
	}
	return *p.checkoutCaptcha, true
}

func (c pageCaptcha) challenge(pageURL, purpose string) solver.Challenge {
	return solver.Challenge{Kind: c.kind, SiteKey: c.siteKey, Action: c.action, Invisible: c.invisible, PageURL: pageURL, Purpose: purpose}
}

// solveCaptcha 页面带验证码时交给 Solver（有匹配的预取结果则直接用），把 token 与附加字段写入表单，
// 返回求解者名称（供站点拒绝 token 时定位）；没有验证码直接返回空串。
func solveCaptcha(ctx context.Context, env provider.Env, page *page, values url.Values, context, pageURL, purpose string, pre *provider.Prefetch) (string, error) {
	captcha, found := detectCaptcha(page.doc, page.resp.Body, context)
	if !found {
		return "", nil
	}
	solution, err := pre.Solve(ctx, env, captcha.challenge(pageURL, purpose))
	if err != nil {
		return "", err
	}
	values.Set(captcha.field, solution.Token)
	for key := range captcha.extra {
		values.Set(key, captcha.extra.Get(key))
	}
	return solution.SolvedBy, nil
}
