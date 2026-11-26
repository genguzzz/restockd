// Package htmlutil 汇集各 Provider 共用的 HTML 解析工具：表单序列化、金额 / 周期解析、验证码探测。
package htmlutil

import (
	"bytes"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/solver"
)

// Parse 解析 HTML 文档。
func Parse(body []byte) (*goquery.Document, error) {
	return goquery.NewDocumentFromReader(bytes.NewReader(body))
}

// CleanText 折叠空白。
func CleanText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// FormValues 按浏览器提交语义序列化表单：hidden/text 等取 value，checkbox/radio 仅取 checked，
// select 取 selected（无则第一项），textarea 取文本；disabled 与无 name 的控件忽略。
func FormValues(form *goquery.Selection) url.Values {
	values := url.Values{}
	form.Find("input, select, textarea").Each(func(_ int, field *goquery.Selection) {
		name, _ := field.Attr("name")
		if name == "" {
			return
		}
		if _, disabled := field.Attr("disabled"); disabled {
			return
		}
		switch goquery.NodeName(field) {
		case "select":
			option := field.Find("option[selected]").First()
			if option.Length() == 0 {
				option = field.Find("option").First()
			}
			if option.Length() > 0 {
				values.Add(name, optionValue(option))
			}
		case "textarea":
			values.Add(name, field.Text())
		default:
			inputType := strings.ToLower(field.AttrOr("type", "text"))
			switch inputType {
			case "submit", "button", "image", "file", "reset":
				return
			case "checkbox", "radio":
				if _, checked := field.Attr("checked"); checked {
					values.Add(name, field.AttrOr("value", "on"))
				}
			default:
				values.Add(name, field.AttrOr("value", ""))
			}
		}
	})
	return values
}

func optionValue(option *goquery.Selection) string {
	if value, ok := option.Attr("value"); ok {
		return value
	}
	return strings.TrimSpace(option.Text())
}

var csrfPattern = regexp.MustCompile(`csrfToken\s*=\s*['"]([a-f0-9]{20,})['"]`)

// CSRFToken 读取 WHMCS 风格的 token：优先隐藏域 name=token，其次脚本中的 csrfToken。
func CSRFToken(doc *goquery.Document, body []byte) string {
	if token := doc.Find(`input[name="token"]`).First().AttrOr("value", ""); token != "" {
		return token
	}
	if match := csrfPattern.FindSubmatch(body); match != nil {
		return string(match[1])
	}
	return ""
}

var (
	// 千分位分支至少要有一组分隔符：否则 "1000.00" 会被第一个分支截成 "100"。
	amountPattern = regexp.MustCompile(`(-?\d{1,3}(?:[,\s]\d{3})+(?:\.\d+)?|-?\d+(?:\.\d+)?)`)
	// 已知币种代码，前后不能紧挨字母（"22.36USD" 也要识别，"VPS" 之类不能误认）。
	currencyPattern = regexp.MustCompile(`(?:^|[^A-Za-z])(USD|CNY|RMB|CAD|EUR|GBP|HKD|JPY|AUD|SGD|TWD|KRW|RUB|CHF|NZD)(?:$|[^A-Za-z])`)
)

var symbolCurrency = map[string]string{"C$": "CAD", "CA$": "CAD", "A$": "AUD", "HK$": "HKD", "US$": "USD", "$": "USD", "¥": "CNY", "￥": "CNY", "€": "EUR", "£": "GBP"}

// ParseMoney 解析 "$19.99 CAD"、"¥188.00"、"19.99 USD" 等；fallbackCurrency 用于无法识别货币时。
func ParseMoney(text, fallbackCurrency string) (domain.Money, bool) {
	text = strings.TrimSpace(text)
	match := amountPattern.FindString(text)
	if match == "" {
		return domain.Money{}, false
	}
	number, err := strconv.ParseFloat(strings.NewReplacer(",", "", " ", "").Replace(match), 64)
	if err != nil {
		return domain.Money{}, false
	}
	return domain.Money{Cents: int64(math.Round(number * 100)), Currency: detectCurrency(text, fallbackCurrency)}, true
}

// detectCurrency 优先三位货币代码；裸 "$" 有歧义（USD/CAD/AUD…），有站点默认货币时以默认为准。
func detectCurrency(text, fallbackCurrency string) string {
	if code := currencyPattern.FindStringSubmatch(text); code != nil {
		if code[1] == "RMB" {
			return "CNY"
		}
		return code[1]
	}
	for _, symbol := range []string{"CA$", "C$", "A$", "HK$", "US$", "$", "¥", "￥", "€", "£"} {
		if !strings.Contains(text, symbol) {
			continue
		}
		if symbol == "$" && fallbackCurrency != "" {
			return fallbackCurrency
		}
		return symbolCurrency[symbol]
	}
	return fallbackCurrency
}

var cycleWords = []struct {
	words []string
	cycle domain.BillingCycle
}{
	{[]string{"semi-annually", "semiannually", "semi annually", "半年"}, domain.CycleSemiannually},
	{[]string{"biennially", "两年"}, domain.CycleBiennially},
	{[]string{"triennially", "三年"}, domain.CycleTriennially},
	{[]string{"annually", "yearly", "年付", "年缴", "每年", "包年", "/yr", "/year"}, domain.CycleAnnually},
	{[]string{"quarterly", "季付", "季缴", "每季", "季度"}, domain.CycleQuarterly},
	{[]string{"monthly", "月付", "月缴", "每月", "包月", "/mo", "/month"}, domain.CycleMonthly},
	{[]string{"one time", "one-time", "onetime", "free", "一次性"}, domain.CycleOneTime},
}

// ParseCycle 从描述文本识别周期；识别不到返回空串。
func ParseCycle(text string) domain.BillingCycle {
	lower := strings.ToLower(text)
	for _, entry := range cycleWords {
		for _, word := range entry.words {
			if strings.Contains(lower, word) {
				return entry.cycle
			}
		}
	}
	return ""
}

// DetectCaptcha 探测页面中的验证码控件，返回类型与 sitekey。
// 先按各家控件 class 精确识别；只剩裸 [data-sitekey]（主题自定义容器）时按最常见的 reCAPTCHA v2 处理。
func DetectCaptcha(doc *goquery.Document) (solver.Kind, string, bool) {
	if node := doc.Find(".cf-turnstile[data-sitekey]").First(); node.Length() > 0 {
		return solver.KindTurnstile, node.AttrOr("data-sitekey", ""), true
	}
	if node := doc.Find(".h-captcha[data-sitekey]").First(); node.Length() > 0 {
		return solver.KindHCaptcha, node.AttrOr("data-sitekey", ""), true
	}
	if node := doc.Find(".g-recaptcha[data-sitekey]").First(); node.Length() > 0 {
		return solver.KindRecaptchaV2, node.AttrOr("data-sitekey", ""), true
	}
	if node := doc.Find("[data-sitekey]").First(); node.Length() > 0 {
		return solver.KindRecaptchaV2, node.AttrOr("data-sitekey", ""), true
	}
	return "", "", false
}

// InvisibleRecaptcha 页面是否为隐形 reCAPTCHA v2：控件 data-size="invisible"，或 WHMCS 的 btn-recaptcha-invisible 提交按钮。
func InvisibleRecaptcha(doc *goquery.Document) bool {
	return doc.Find(`.g-recaptcha[data-size="invisible"], .btn-recaptcha-invisible`).Length() > 0
}

// CaptchaField 各验证码提交时的表单字段名。
func CaptchaField(kind solver.Kind) string {
	switch kind {
	case solver.KindTurnstile:
		return "cf-turnstile-response"
	case solver.KindHCaptcha:
		return "h-captcha-response"
	default:
		return "g-recaptcha-response"
	}
}

// Resolve 把相对链接解析为绝对 URL。
func Resolve(base, ref string) string {
	baseURL, err := url.Parse(base)
	if err != nil {
		return ref
	}
	refURL, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return baseURL.ResolveReference(refURL).String()
}

// Title 页面 <title> 文本（解析失败或没有为空），用于错误信息。
func Title(body []byte) string {
	doc, err := Parse(body)
	if err != nil {
		return ""
	}
	return CleanText(doc.Find("title").First().Text())
}
