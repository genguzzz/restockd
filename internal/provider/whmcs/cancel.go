package whmcs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/provider/htmlutil"
)

var _ provider.Canceler = (*Provider)(nil)

const (
	// maxServicesScanned 按账单反查服务时最多看最新的几个服务。
	maxServicesScanned = 10
	cancelReason       = "订单不再需要，请取消。"
)

var (
	serviceIDPattern     = regexp.MustCompile(`productdetails&(?:amp;)?id=(\d+)`)
	viewInvoiceIDPattern = regexp.MustCompile(`viewinvoice\.php\?id=(\d+)`)
)

// CancelOrder 实现 provider.Canceler：按账单找到对应服务，提交 WHMCS 标准的「请求取消」（立即取消）。
// 很多商家不允许客户取消待开通服务（取消页直接跳回服务详情），未付款订单由商家按规则自动作废——
// 这种情况返回包装了 ErrNotSupported 的错误。
func (p *Provider) CancelOrder(ctx context.Context, env provider.Env, result domain.OrderResult) error {
	invoiceID := result.InvoiceID
	if match := viewInvoiceIDPattern.FindStringSubmatch(result.PayURL); invoiceID == "" && match != nil {
		invoiceID = match[1]
	}
	if invoiceID == "" {
		return errors.New("order has no invoice id")
	}
	serviceID, err := p.serviceForInvoice(ctx, env, invoiceID)
	if err != nil {
		return err
	}
	page, err := p.get(ctx, env, "/clientarea.php?action=cancel&id="+serviceID)
	if err != nil {
		return err
	}
	form := page.doc.Find(`select[name="type"], textarea[name="cancellationreason"], textarea[name="reason"]`).First().Closest("form")
	if form.Length() == 0 {
		return fmt.Errorf("%w: 站点不允许客户自助取消服务 %s（未付款订单通常由商家按规则自动作废，或请提交工单）", provider.ErrNotSupported, serviceID)
	}
	values := htmlutil.FormValues(form)
	values.Set("type", "Immediate")
	for _, name := range []string{"cancellationreason", "reason"} {
		if form.Find(`[name="`+name+`"]`).Length() > 0 {
			values.Set(name, cancelReason)
		}
	}
	if values.Get("token") == "" {
		values.Set("token", htmlutil.CSRFToken(page.doc, page.resp.Body))
	}
	action := htmlutil.Resolve(page.resp.URL, form.AttrOr("action", page.resp.URL))
	done, err := p.do(ctx, env, &fetch.Request{Method: http.MethodPost, URL: action, Form: values})
	if err != nil {
		return err
	}
	if message := errorMessage(done.doc); message != "" {
		return fmt.Errorf("%w: cancel service %s: %s", provider.ErrOrderRejected, serviceID, message)
	}
	return nil
}

// serviceForInvoice 在「我的服务」里从最新的服务往前找，详情页引用了该账单的即为本单服务。
func (p *Provider) serviceForInvoice(ctx context.Context, env provider.Env, invoiceID string) (string, error) {
	list, err := p.get(ctx, env, "/clientarea.php?action=services")
	if err != nil {
		return "", err
	}
	seen := map[int]bool{}
	var ids []int
	for _, match := range serviceIDPattern.FindAllSubmatch(list.resp.Body, -1) {
		if id, err := strconv.Atoi(string(match[1])); err == nil && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ids)))
	for i, id := range ids {
		if i == maxServicesScanned {
			break
		}
		detail, err := p.get(ctx, env, "/clientarea.php?action=productdetails&id="+strconv.Itoa(id))
		if err != nil {
			return "", err
		}
		if strings.Contains(string(detail.resp.Body), "viewinvoice.php?id="+invoiceID+`"`) || strings.Contains(string(detail.resp.Body), "viewinvoice.php?id="+invoiceID+"&") {
			return strconv.Itoa(id), nil
		}
	}
	return "", fmt.Errorf("no service found for invoice %s among the latest %d services", invoiceID, min(len(ids), maxServicesScanned))
}
