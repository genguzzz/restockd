package whmcs

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/provider/htmlutil"
)

// 部分商家装了「同意条款」插件（index.php?m=agreeterms）：结算页先给出 agreetermsform，
// 提交后进入条款页，要求停留若干秒（服务端也校验）并勾选同意，才放行结算。

const (
	maxTermsRounds      = 3 // 购物车里每个需要同意条款的商品各一轮
	defaultTermsWaitSec = 3
)

// termsSleep 条款页要求的阅读等待（测试替换为不等待）。
var termsSleep = func(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

var termsWaitPattern = regexp.MustCompile(`waitSeconds\s*=\s*(\d+)`)

func termsGate(doc *goquery.Document) *goquery.Selection {
	form := doc.Find(`#agreetermsform, form[action*="m=agreeterms"]`).First()
	if form.Length() == 0 {
		return nil
	}
	return form
}

// acceptTerms 结算页被条款插件拦截时逐个同意，返回放行后的结算页。
func (p *Provider) acceptTerms(ctx context.Context, env provider.Env, checkout *page) (*page, error) {
	for round := 0; round < maxTermsRounds; round++ {
		gate := termsGate(checkout.doc)
		if gate == nil {
			return checkout, nil
		}
		action := htmlutil.Resolve(checkout.resp.URL, gate.AttrOr("action", "index.php?m=agreeterms"))
		terms, err := p.do(ctx, env, &fetch.Request{Method: http.MethodPost, URL: action, Form: htmlutil.FormValues(gate)})
		if err != nil {
			return nil, fmt.Errorf("open terms: %w", err)
		}
		agree := terms.doc.Find(`#agreetermssuccess`).First()
		if agree.Length() == 0 {
			agree = terms.doc.Find(`input[type="checkbox"][name="agreeterms"]`).First().Closest("form")
		}
		if agree.Length() == 0 {
			return nil, fmt.Errorf("terms page without an agree form at %s", terms.resp.URL)
		}
		wait := defaultTermsWaitSec
		if match := termsWaitPattern.FindSubmatch(terms.resp.Body); match != nil {
			wait, _ = strconv.Atoi(string(match[1]))
		}
		if err := termsSleep(ctx, time.Duration(wait+1)*time.Second); err != nil {
			return nil, err
		}
		values := url.Values{}
		agree.Find(`input[type="hidden"][name]`).Each(func(_ int, input *goquery.Selection) {
			if name := input.AttrOr("name", ""); name != "readconfirmed" {
				values.Add(name, input.AttrOr("value", ""))
			}
		})
		values.Set("readconfirmed", "1")
		values.Add("agreeterms", "1") // 勾选框；与隐藏的 agreeterms=true 同名，PHP 取最后一个
		target := htmlutil.Resolve(terms.resp.URL, agree.AttrOr("action", ""))
		if _, err := p.do(ctx, env, &fetch.Request{Method: http.MethodPost, URL: target, Form: values}); err != nil {
			return nil, fmt.Errorf("agree terms: %w", err)
		}
		if checkout, err = p.get(ctx, env, "/cart.php?a=checkout"); err != nil {
			return nil, fmt.Errorf("checkout page: %w", err)
		}
	}
	if termsGate(checkout.doc) != nil {
		return nil, fmt.Errorf("terms agreement not accepted after %d rounds", maxTermsRounds)
	}
	return checkout, nil
}
