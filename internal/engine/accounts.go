package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/store"
)

// 账号：增删改、会话缓存与状态。所有账号写入都经 updateAccount（持有 docMu），
// 替换 Cookie / 凭据时在同一临界区内丢弃缓存会话，在途调用持有的旧会话因此不会再写回。

// errSkip mutate 返回它表示放弃本次写入。
var errSkip = errors.New("skip account update")

// AccountInput 新建 / 更新账号（API 请求体）。
//   - Username 为 nil 不修改；Password 为 nil 或 SecretMask 不修改，空串清除；
//   - Cookies 非空时替换 Cookie。
type AccountInput struct {
	ID        string
	SiteID    string
	Label     string
	UserAgent string
	Defaults  domain.OrderDefaults
	Username  *string
	Password  *string
	Cookies   []*http.Cookie
}

// SaveAccount 一次原子写入完成新建或更新：基础信息、凭据、Cookie。
// 凭据变了就解除「密码被拒」；换了用户名则丢弃旧 Cookie 与身份，避免继续用旧用户的会话下单。
func (e *Engine) SaveAccount(ctx context.Context, input AccountInput) (domain.Account, error) {
	if input.ID != "" {
		return e.updateAccount(ctx, input.ID, func(stored *domain.Account) error {
			e.applyAccountInput(stored, input)
			return nil
		})
	}
	e.docMu.Lock()
	defer e.docMu.Unlock()
	if _, err := e.store.GetSite(ctx, input.SiteID); err != nil {
		return domain.Account{}, tagged{err: fmt.Errorf("site %s: %w", input.SiteID, err), tag: ErrInvalid}
	}
	account := domain.Account{ID: newID(), SiteID: input.SiteID, Status: domain.AccountUnknown, CreatedAt: time.Now().UTC()}
	e.applyAccountInput(&account, input)
	if err := e.store.SaveAccount(ctx, account); err != nil {
		return domain.Account{}, err
	}
	e.bus.Publish("account", account)
	return account, nil
}

// applyAccountInput 调用方持有 docMu。
func (e *Engine) applyAccountInput(account *domain.Account, input AccountInput) {
	account.Label = input.Label
	account.UserAgent = input.UserAgent
	account.Defaults = input.Defaults
	previous := account.Credentials
	if input.Username != nil {
		account.Credentials.Username = strings.TrimSpace(*input.Username)
	}
	if input.Password != nil && *input.Password != SecretMask {
		account.Credentials.Password = *input.Password
	}
	if account.Credentials != previous {
		account.CredentialsRejected = false
		e.pauseLogin(account.ID, time.Time{})
		if previous.Username != "" && previous.Username != account.Credentials.Username {
			account.Cookies = nil
			account.Status = domain.AccountUnknown
			account.Identity = ""
			account.Balance = nil
		}
	}
	if len(input.Cookies) > 0 {
		setCookies(account, input.Cookies, input.UserAgent)
	}
	e.ResetSession(account.ID) // UA / Cookie / 用户名都可能变了
}

// SetCookies 替换账号 Cookie（手动粘贴），并重置会话缓存。
func (e *Engine) SetCookies(ctx context.Context, accountID string, cookies []*http.Cookie, userAgent string) (domain.Account, error) {
	return e.updateAccount(ctx, accountID, func(stored *domain.Account) error {
		setCookies(stored, cookies, userAgent)
		e.ResetSession(accountID)
		return nil
	})
}

func setCookies(account *domain.Account, cookies []*http.Cookie, userAgent string) {
	account.Cookies = cookies
	if userAgent != "" {
		account.UserAgent = userAgent
	}
	account.Status = domain.AccountUnknown
	account.LastError = ""
}

// DeleteAccount 删除账号与会话缓存；仍被关注规则或站点监控引用时拒绝。
func (e *Engine) DeleteAccount(ctx context.Context, accountID string) error {
	e.docMu.Lock()
	defer e.docMu.Unlock()
	var refs []string
	watches, err := e.store.ListWatches(ctx, "")
	if err != nil {
		return err
	}
	for _, watch := range watches {
		if watch.AccountID == accountID {
			refs = append(refs, "watch "+watch.ID+" ("+watch.ProductName+")")
		}
	}
	sites, err := e.store.ListSites(ctx)
	if err != nil {
		return err
	}
	for _, site := range sites {
		if site.MonitorAccount == accountID {
			refs = append(refs, "site "+site.ID+" monitor account")
		}
	}
	if len(refs) > 0 {
		return invalidf("account is still used by: %s", strings.Join(refs, ", "))
	}
	if err := e.store.DeleteAccount(ctx, accountID); err != nil {
		return err
	}
	e.forgetAccount(accountID)
	return nil
}

// forgetAccount 账号删除后清理内存状态。
func (e *Engine) forgetAccount(accountID string) {
	e.ResetSession(accountID)
	e.pauseLogin(accountID, time.Time{})
	e.loginLocks.Delete(accountID)
}

// updateAccount 账号读改写的唯一入口：串行化，写入后广播 "account"。
func (e *Engine) updateAccount(ctx context.Context, id string, mutate func(*domain.Account) error) (domain.Account, error) {
	e.docMu.Lock()
	defer e.docMu.Unlock()
	account, err := e.store.GetAccount(ctx, id)
	if err != nil {
		return domain.Account{}, err
	}
	if err := mutate(&account); err != nil {
		return domain.Account{}, err
	}
	if err := e.store.SaveAccount(ctx, account); err != nil {
		return domain.Account{}, err
	}
	e.bus.Publish("account", account)
	return account, nil
}

// markAccountStatus 记录会话状态；只在变为失效时记事件并通知一次。
func (e *Engine) markAccountStatus(ctx context.Context, accountID string, status domain.AccountStatus, message string) {
	var previous domain.AccountStatus
	account, err := e.updateAccount(ctx, accountID, func(stored *domain.Account) error {
		previous = stored.Status
		stored.Status = status
		stored.LastError = message
		stored.CheckedAt = time.Now().UTC()
		return nil
	})
	if err == nil && status == domain.AccountExpired && previous != domain.AccountExpired {
		event, notice := sessionExpiredNotice(account)
		e.recordEvent(ctx, event, notice)
	}
}

// ---- 会话缓存 ----

func sessionKey(accountID string) string { return "acct:" + accountID }

// sessionFor 会话缓存：首次从账号 Cookie 构建，之后复用以保留站点刷新的 Cookie。
// account 为 nil 时返回站点级匿名会话（保留 cf_clearance 等）。账号会话须在持有 docMu 时获取（见 accountSession）。
func (e *Engine) sessionFor(site domain.Site, account *domain.Account) (*fetch.Session, error) {
	key := "anon:" + site.ID
	if account != nil {
		key = sessionKey(account.ID)
	}
	e.mu.RLock()
	session, ok := e.sessions[key]
	e.mu.RUnlock()
	if ok {
		return session, nil
	}
	var err error
	if account != nil {
		session, err = fetch.NewSession(site.BaseURL, account.Cookies, account.UserAgent)
	} else {
		session, err = fetch.NewSession(site.BaseURL, nil, "")
	}
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	if existing, ok := e.sessions[key]; ok {
		session = existing
	} else {
		e.sessions[key] = session
	}
	e.mu.Unlock()
	return session, nil
}

// accountSession 读取账号并取其会话；与 Cookie 替换同在 docMu 下，保证缓存会话与库中 Cookie 一致。
func (e *Engine) accountSession(ctx context.Context, site domain.Site, accountID string) (*domain.Account, *fetch.Session, error) {
	e.docMu.Lock()
	defer e.docMu.Unlock()
	account, err := e.store.GetAccount(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	if account.SiteID != site.ID {
		return nil, nil, invalidf("account %s belongs to site %s", accountID, account.SiteID)
	}
	session, err := e.sessionFor(site, &account)
	if err != nil {
		return nil, nil, err
	}
	return &account, session, nil
}

// ResetSession 丢弃账号的缓存会话；替换 Cookie / 凭据时须在 updateAccount 的 mutate 内调用。
func (e *Engine) ResetSession(accountID string) {
	e.mu.Lock()
	delete(e.sessions, sessionKey(accountID))
	e.mu.Unlock()
}

// sessionCurrent 会话是否仍是该账号的缓存会话（被 ResetSession 丢弃后返回 false）。
func (e *Engine) sessionCurrent(accountID string, session *fetch.Session) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.sessions[sessionKey(accountID)] == session
}

// persistSession 把会话中最新 Cookie 写回账号（站点会滚动刷新 laravel_session / cf_clearance）；
// 会话已被替换（用户刚粘贴了新 Cookie）时放弃，避免旧 Cookie 覆盖新的。
func (e *Engine) persistSession(ctx context.Context, account *domain.Account, session *fetch.Session) {
	if account == nil || session == nil {
		return
	}
	_, err := e.updateAccount(ctx, account.ID, func(stored *domain.Account) error {
		return e.applySession(stored, session)
	})
	if err != nil && !errors.Is(err, errSkip) && !errors.Is(err, store.ErrNotFound) {
		e.log.Warn("persist session", "account", account.ID, "err", err)
	}
}

// applySession 在 updateAccount 的 mutate 内调用：会话仍有效时把 Cookie / UA 写入账号，否则返回 errSkip。
func (e *Engine) applySession(stored *domain.Account, session *fetch.Session) error {
	if !e.sessionCurrent(stored.ID, session) {
		return errSkip
	}
	stored.Cookies = session.Cookies()
	if userAgent := session.UserAgent(); userAgent != "" && userAgent != fetch.DefaultUserAgent {
		stored.UserAgent = userAgent
	}
	return nil
}
