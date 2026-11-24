package usvps24

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/provider"
)

var _ provider.Canceler = (*Provider)(nil)

var invoiceURLPattern = regexp.MustCompile(`/invoices/(\d+)`)

// invoiceProps 账单页（Invoices/Show）里取消要看的字段；结构以站点为准，只取 status 与提示。
type invoiceProps struct {
	Errors  map[string]any `json:"errors"`
	Invoice struct {
		Status string `json:"status"`
	} `json:"invoice"`
	Flash struct {
		Success string `json:"success"`
		Error   string `json:"error"`
	} `json:"flash"`
}

// CancelOrder 实现 provider.Canceler：POST /invoices/{id}/cancel（与站点前端一致的 Inertia 提交），再读账单确认已作废。
// 只访问账单页（不受库存 / 下单页的频率限制）。
func (p *Provider) CancelOrder(ctx context.Context, env provider.Env, result domain.OrderResult) error {
	invoiceID := result.InvoiceID
	if match := invoiceURLPattern.FindStringSubmatch(result.PayURL); invoiceID == "" && match != nil {
		invoiceID = match[1]
	}
	if invoiceID == "" {
		return errors.New("order has no invoice id")
	}
	path := "/invoices/" + invoiceID
	before, err := p.invoice(ctx, env, path)
	if err != nil {
		return err
	}
	if cancelledStatus(before.props.Invoice.Status) {
		return nil
	}
	resp, err := p.postJSON(ctx, env, path+"/cancel", map[string]any{}, before.version)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return fmt.Errorf("%w: invoice %s cannot be cancelled (http %d)", provider.ErrNotSupported, invoiceID, resp.StatusCode)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("cancel invoice %s: http %d", invoiceID, resp.StatusCode)
	}
	if page, err := ParsePage(resp.Body); err == nil {
		var props invoiceProps
		_ = json.Unmarshal(page.Props, &props)
		if message := cmp.Or(first(laravelErrors(props.Errors)), props.Flash.Error); message != "" {
			return fmt.Errorf("%w: %s", provider.ErrNotSupported, message)
		}
	}
	after, err := p.invoice(ctx, env, path)
	if err != nil {
		return err
	}
	if !cancelledStatus(after.props.Invoice.Status) {
		return fmt.Errorf("invoice %s still %q after cancel", invoiceID, after.props.Invoice.Status)
	}
	return nil
}

type invoicePage struct {
	version string
	props   invoiceProps
}

func (p *Provider) invoice(ctx context.Context, env provider.Env, path string) (invoicePage, error) {
	page, err := p.getPage(ctx, env, path)
	if err != nil {
		return invoicePage{}, err
	}
	if page.Component == "Auth/Login" {
		return invoicePage{}, provider.ErrSessionExpired
	}
	var props invoiceProps
	if err := json.Unmarshal(page.Props, &props); err != nil {
		return invoicePage{}, fmt.Errorf("decode invoice: %w", err)
	}
	return invoicePage{version: page.Version, props: props}, nil
}

// cancelledStatus 站点账单状态里表示已作废的取值（cancelled / canceled / void）。
func cancelledStatus(status string) bool {
	status = strings.ToLower(status)
	return strings.Contains(status, "cancel") || strings.Contains(status, "void")
}
