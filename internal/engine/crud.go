package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/store"
)

// SaveSite 校验并保存站点配置，保留运行时状态字段，随后重启调度。
func (e *Engine) SaveSite(ctx context.Context, site domain.Site) (domain.Site, error) {
	meta, ok := provider.Lookup(site.Kind)
	if !ok {
		return domain.Site{}, fmt.Errorf("unknown provider kind %q", site.Kind)
	}
	parsed, err := url.Parse(site.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return domain.Site{}, fmt.Errorf("invalid base_url %q", site.BaseURL)
	}
	site.BaseURL = strings.TrimRight(site.BaseURL, "/")
	for _, field := range meta.ConfigFields {
		if field.Required && strings.TrimSpace(site.Config[field.Key]) == "" {
			return domain.Site{}, fmt.Errorf("config %s (%s) is required", field.Key, field.Label)
		}
	}
	if site.FetchMode == "" {
		site.FetchMode = meta.DefaultFetch
	}
	if site.ID == "" {
		site.ID = newID()
	}
	if _, err := provider.New(site); err != nil {
		return domain.Site{}, err
	}
	e.docMu.Lock()
	if existing, err := e.store.GetSite(ctx, site.ID); err == nil {
		site.LastPollAt, site.LastError, site.NextPollAt, site.ConsecutiveFail = existing.LastPollAt, existing.LastError, existing.NextPollAt, existing.ConsecutiveFail
	} else if !errors.Is(err, store.ErrNotFound) {
		e.docMu.Unlock()
		return domain.Site{}, err
	}
	err = e.store.SaveSite(ctx, site)
	e.docMu.Unlock()
	if err != nil {
		return domain.Site{}, err
	}
	return site, e.ReloadSite(site)
}

// DeleteSite 停止调度并级联删除。
func (e *Engine) DeleteSite(ctx context.Context, siteID string) error {
	e.StopSite(siteID)
	return e.store.DeleteSite(ctx, siteID)
}

// SaveWatch 校验并保存关注规则，保留计数类运行时字段。
func (e *Engine) SaveWatch(ctx context.Context, watch domain.Watch) (domain.Watch, error) {
	if _, err := e.store.GetSite(ctx, watch.SiteID); err != nil {
		return domain.Watch{}, fmt.Errorf("site %s: %w", watch.SiteID, err)
	}
	if watch.ProductID == "" {
		return domain.Watch{}, errors.New("product_id is required")
	}
	if watch.AccountID != "" {
		account, err := e.store.GetAccount(ctx, watch.AccountID)
		if err != nil {
			return domain.Watch{}, fmt.Errorf("account %s: %w", watch.AccountID, err)
		}
		if account.SiteID != watch.SiteID {
			return domain.Watch{}, errors.New("account belongs to another site")
		}
	}
	if watch.AutoOrder && watch.AccountID == "" {
		return domain.Watch{}, errors.New("auto_order requires an account")
	}
	if watch.Qty <= 0 {
		watch.Qty = 1
	}
	if watch.Cycle == "" {
		watch.Cycle = domain.CycleMonthly
	}
	if product, err := e.store.GetProduct(ctx, watch.SiteID, watch.ProductID); err == nil {
		watch.ProductName = product.Name
	}
	if watch.ID == "" {
		watch.ID = newID()
		watch.CreatedAt = time.Now().UTC()
	}
	e.docMu.Lock()
	defer e.docMu.Unlock()
	if existing, err := e.store.GetWatch(ctx, watch.ID); err == nil {
		watch.CreatedAt, watch.OrdersPlaced, watch.LastTriggered = existing.CreatedAt, existing.OrdersPlaced, existing.LastTriggered
	}
	if err := e.store.SaveWatch(ctx, watch); err != nil {
		return watch, err
	}
	e.RescheduleSite(watch.SiteID)
	return watch, nil
}

// DeleteWatch 删除关注规则并让站点按新间隔重排（可能降频）。
func (e *Engine) DeleteWatch(ctx context.Context, watchID string) error {
	watch, err := e.store.GetWatch(ctx, watchID)
	if err != nil {
		return err
	}
	if err := e.store.DeleteWatch(ctx, watchID); err != nil {
		return err
	}
	e.RescheduleSite(watch.SiteID)
	return nil
}

// SaveAccount 新建或更新账号基础信息（不改 Cookie）。
func (e *Engine) SaveAccount(ctx context.Context, account domain.Account) (domain.Account, error) {
	if _, err := e.store.GetSite(ctx, account.SiteID); err != nil {
		return domain.Account{}, fmt.Errorf("site %s: %w", account.SiteID, err)
	}
	if account.ID == "" {
		account.ID = newID()
		account.CreatedAt = time.Now().UTC()
		account.Status = domain.AccountUnknown
		account.Cookies = nil
		account.SyncCookieNames()
		e.docMu.Lock()
		defer e.docMu.Unlock()
		return account, e.store.SaveAccount(ctx, account)
	}
	updated, err := e.updateAccount(ctx, account.ID, func(stored *domain.Account) {
		stored.Label = account.Label
		stored.UserAgent = account.UserAgent
		stored.Defaults = account.Defaults
	})
	if err == nil {
		e.ResetSession(account.ID)
	}
	return updated, err
}

// SetCookies 替换账号 Cookie（手动粘贴），并重置会话缓存。
func (e *Engine) SetCookies(ctx context.Context, accountID string, cookies []*http.Cookie, userAgent string) (domain.Account, error) {
	account, err := e.updateAccount(ctx, accountID, func(stored *domain.Account) {
		stored.Cookies = cookies
		if userAgent != "" {
			stored.UserAgent = userAgent
		}
		stored.Status = domain.AccountUnknown
		stored.LastError = ""
	})
	if err == nil {
		e.ResetSession(accountID)
	}
	return account, err
}

// DeleteAccount 删除账号与会话缓存。
func (e *Engine) DeleteAccount(ctx context.Context, accountID string) error {
	e.ResetSession(accountID)
	return e.store.DeleteAccount(ctx, accountID)
}
