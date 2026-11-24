package whmcs

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"

	"github.com/genguzzz/restockd/internal/solver"
)

func TestDetectCaptchaAddonConfig(t *testing.T) {
	// NovixLink（megabre_turnstile）：页面无 data-sitekey，配置注入在 window.novixCaptcha。
	body := `<html><head><script>window.dataLayer = window.dataLayer || [];</script>
<script>window.novixCaptcha = {"contexts":["login","checkoutCompletion"],"template":"lagom2","mode":"managed","siteKey":"0xSITE","responseField":"megabre_turnstile_response","providerField":"megabre_captcha_provider","hcaptchaEnabled":true,"initialProvider":"turnstile","strings":{"pending":"Verifying; please wait."}};</script>
</head><body><form id="frmCheckout"></form></body></html>`
	doc, _ := goquery.NewDocumentFromReader(strings.NewReader(body))
	captcha, ok := detectCaptcha(doc, []byte(body), captchaCheckout)
	if !ok || captcha.kind != solver.KindTurnstile || captcha.siteKey != "0xSITE" || captcha.action != captchaCheckout || captcha.field != "megabre_turnstile_response" || captcha.extra.Get("megabre_captcha_provider") != "turnstile" {
		t.Fatalf("addon config not detected: %+v ok=%v", captcha, ok)
	}
	if _, ok := detectCaptcha(doc, []byte(body), "submitTicket"); ok {
		t.Fatal("contexts outside the addon's list need no captcha")
	}
	plain := `<form><div class="cf-turnstile" data-sitekey="0xPLAIN"></div></form>`
	plainDoc, _ := goquery.NewDocumentFromReader(strings.NewReader(plain))
	if captcha, ok := detectCaptcha(plainDoc, []byte(plain), captchaLogin); !ok || captcha.siteKey != "0xPLAIN" || captcha.field != "cf-turnstile-response" {
		t.Fatalf("plain widget not detected: %+v", captcha)
	}
	none := `<form id="frmCheckout"></form><script>window.cfg = {"a":1};</script>`
	noneDoc, _ := goquery.NewDocumentFromReader(strings.NewReader(none))
	if _, ok := detectCaptcha(noneDoc, []byte(none), captchaCheckout); ok {
		t.Fatal("no captcha expected")
	}
}
