package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/store"
)

// 站点与关注规则的增删改。读改写都持有 docMu，与调度 / 锁单的写入互斥。

// SaveSite 校验并保存站点配置，保留运行时状态字段，随后重启调度。
func (e *Engine) SaveSite(ctx context.Context, site domain.Site) (domain.Site, error) {
	meta, ok := provider.Lookup(site.Kind)
	if !ok {
		return domain.Site{}, invalidf("unknown provider kind %q", site.Kind)
	}
	parsed, err := url.Parse(site.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return domain.Site{}, invalidf("invalid base_url %q", site.BaseURL)
	}
	site.BaseURL = strings.TrimRight(site.BaseURL, "/")
	for _, field := range meta.ConfigFields {
		if field.Required && strings.TrimSpace(site.Config[field.Key]) == "" {
			return domain.Site{}, invalidf("config %s (%s) is required", field.Key, field.Label)
		}
	}
	if site.FetchMode == "" {
		site.FetchMode = meta.DefaultFetch
	}
	if site.ID == "" {
		site.ID = newID()
	}
	if _, err := provider.New(site); err != nil {
		return domain.Site{}, tagged{err: err, tag: ErrInvalid}
	}
	if err := e.saveSite(ctx, &site); err != nil {
		return domain.Site{}, err
	}
	return site, e.ReloadSite(site)
}

func (e *Engine) saveSite(ctx context.Context, site *domain.Site) error {
	e.docMu.Lock()
	defer e.docMu.Unlock()
	if site.MonitorAccount != "" {
		account, err := e.store.GetAccount(ctx, site.MonitorAccount)
		if err != nil {
			return tagged{err: fmt.Errorf("monitor account %s: %w", site.MonitorAccount, err), tag: ErrInvalid}
		}
		if account.SiteID != site.ID {
			return invalidf("monitor account belongs to another site")
		}
	}
	site.HiddenProducts = nil // 只经 SetProductHidden 修改
	if existing, err := e.store.GetSite(ctx, site.ID); err == nil {
		site.LastPollAt, site.LastError, site.NextPollAt, site.ConsecutiveFail = existing.LastPollAt, existing.LastError, existing.NextPollAt, existing.ConsecutiveFail
		site.HiddenProducts = existing.HiddenProducts
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return e.store.SaveSite(ctx, *site)
}

// SetProductHidden 隐藏 / 取消隐藏站点下的商品，返回更新后的站点。
func (e *Engine) SetProductHidden(ctx context.Context, siteID, productID string, hidden bool) (domain.Site, error) {
	e.docMu.Lock()
	defer e.docMu.Unlock()
	site, err := e.store.GetSite(ctx, siteID)
	if err != nil {
		return domain.Site{}, err
	}
	site.HiddenProducts = slices.DeleteFunc(slices.Clone(site.HiddenProducts), func(id string) bool { return id == productID })
	if hidden {
		site.HiddenProducts = append(site.HiddenProducts, productID)
	}
	if err := e.store.SaveSite(ctx, site); err != nil {
		return domain.Site{}, err
	}
	e.bus.Publish("site", site)
	return site, nil
}

// DeleteSite 停止调度，级联删除站点下的账号、关注与商品，并清理它们的缓存会话与锁。
func (e *Engine) DeleteSite(ctx context.Context, siteID string) error {
	e.StopSite(siteID)
	e.docMu.Lock()
	defer e.docMu.Unlock()
	accounts, err := e.store.ListAccounts(ctx, siteID)
	if err != nil {
		return err
	}
	watches, err := e.store.ListWatches(ctx, siteID)
	if err != nil {
		return err
	}
	if err := e.store.DeleteSite(ctx, siteID); err != nil {
		return err
	}
	for _, account := range accounts {
		e.forgetAccount(account.ID)
	}
	for _, watch := range watches {
		e.orderLocks.Delete(watchLockKey(watch.ID))
	}
	e.mu.Lock()
	delete(e.sessions, "anon:"+siteID)
	e.mu.Unlock()
	return nil
}

// SaveWatch 校验并保存关注规则，保留计数类运行时字段。
func (e *Engine) SaveWatch(ctx context.Context, watch domain.Watch) (domain.Watch, error) {
	if watch.ProductID == "" {
		return domain.Watch{}, invalidf("product_id is required")
	}
	if watch.AutoOrder && watch.AccountID == "" {
		return domain.Watch{}, invalidf("auto_order requires an account")
	}
	if watch.Qty <= 0 {
		watch.Qty = 1
	}
	if watch.Cycle == "" {
		watch.Cycle = domain.CycleMonthly
	}
	e.docMu.Lock()
	defer e.docMu.Unlock()
	if _, err := e.store.GetSite(ctx, watch.SiteID); err != nil {
		return domain.Watch{}, tagged{err: fmt.Errorf("site %s: %w", watch.SiteID, err), tag: ErrInvalid}
	}
	if watch.AccountID != "" {
		account, err := e.store.GetAccount(ctx, watch.AccountID)
		if err != nil {
			return domain.Watch{}, tagged{err: fmt.Errorf("account %s: %w", watch.AccountID, err), tag: ErrInvalid}
		}
		if account.SiteID != watch.SiteID {
			return domain.Watch{}, invalidf("account belongs to another site")
		}
	}
	if product, err := e.store.GetProduct(ctx, watch.SiteID, watch.ProductID); err == nil {
		watch.ProductName = product.Name
	}
	if watch.ID == "" {
		watch.ID = newID()
		watch.CreatedAt = time.Now().UTC()
	} else if existing, err := e.store.GetWatch(ctx, watch.ID); err == nil {
		watch.CreatedAt, watch.OrdersPlaced, watch.LastTriggered = existing.CreatedAt, existing.OrdersPlaced, existing.LastTriggered
	}
	if err := e.store.SaveWatch(ctx, watch); err != nil {
		return domain.Watch{}, err
	}
	e.RescheduleSite(watch.SiteID)
	return watch, nil
}

// DeleteWatch 删除关注规则并让站点按新间隔重排（可能降频）。
func (e *Engine) DeleteWatch(ctx context.Context, watchID string) error {
	e.docMu.Lock()
	defer e.docMu.Unlock()
	watch, err := e.store.GetWatch(ctx, watchID)
	if err != nil {
		return err
	}
	if err := e.store.DeleteWatch(ctx, watchID); err != nil {
		return err
	}
	e.orderLocks.Delete(watchLockKey(watchID))
	e.RescheduleSite(watch.SiteID)
	return nil
}

// touchWatch 更新触发时间；placed 时累加成功次数。
func (e *Engine) touchWatch(ctx context.Context, watchID string, placed bool) error {
	e.docMu.Lock()
	defer e.docMu.Unlock()
	watch, err := e.store.GetWatch(ctx, watchID)
	if err != nil {
		return err
	}
	watch.LastTriggered = time.Now().UTC()
	if placed {
		watch.OrdersPlaced++
	}
	return e.store.SaveWatch(ctx, watch)
}

func newID() string {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
