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

// pageCaptcha 页面要求的验证码：类型、sitekey、Turnstile action，以及 token 与附加字段的表单名。
type pageCaptcha struct {
	kind    solver.Kind
	siteKey string
	action  string
	field   string
	extra   url.Values
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
	kind, siteKey, found := htmlutil.DetectCaptcha(doc)
	if !found {
		return pageCaptcha{}, false
	}
	return pageCaptcha{kind: kind, siteKey: siteKey, field: htmlutil.CaptchaField(kind)}, true
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
	return solver.Challenge{Kind: c.kind, SiteKey: c.siteKey, Action: c.action, PageURL: pageURL, Purpose: purpose}
}

// solveCaptcha 页面带验证码时交给 Solver（有匹配的预取结果则直接用），把 token 与附加字段写入表单；
// 没有验证码直接返回。
func solveCaptcha(ctx context.Context, env provider.Env, page *page, values url.Values, context, pageURL, purpose string, pre *provider.Prefetch) error {
	captcha, found := detectCaptcha(page.doc, page.resp.Body, context)
	if !found {
		return nil
	}
	solution, err := pre.Solve(ctx, env, captcha.challenge(pageURL, purpose))
	if err != nil {
		return err
	}
	values.Set(captcha.field, solution.Token)
	for key := range captcha.extra {
		values.Set(key, captcha.extra.Get(key))
	}
	return nil
}
