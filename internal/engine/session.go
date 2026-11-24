package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/notify"
	"github.com/genguzzz/restockd/internal/provider"
)

// 自动登录：账号托管了用户名密码且 Provider 实现 Authenticator 时，会话失效（或还没有 Cookie）
// 由引擎自动登录并重试，调用方只需把用到账号会话的调用包进 withSession。

// ErrNoAuthenticator 站点类型不支持账号密码登录。
var ErrNoAuthenticator = errors.New("provider does not support password login")

// ErrNoCredentials 账号没有托管完整的用户名与密码。
var ErrNoCredentials = errors.New("account has no username/password")

func (e *Engine) loginMutex(accountID string) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.loginLocks == nil {
		e.loginLocks = map[string]*sync.Mutex{}
	}
	lock, ok := e.loginLocks[accountID]
	if !ok {
		lock = &sync.Mutex{}
		e.loginLocks[accountID] = lock
	}
	return lock
}

// authenticator 返回可用于该账号的登录实现；凭据不全或 Provider 不支持时 ok=false。
func authenticator(instance provider.Provider, account *domain.Account) (provider.Authenticator, bool) {
	if account == nil || !account.Credentials.Usable() {
		return nil, false
	}
	auth, ok := instance.(provider.Authenticator)
	return auth, ok
}

// autoLoginAllowed 自动登录的前提：可登录，且站点没有拒绝过当前密码（拒绝后反复重试只会触发封禁）。
func autoLoginAllowed(instance provider.Provider, account *domain.Account) bool {
	_, ok := authenticator(instance, account)
	return ok && !account.CredentialsRejected
}

// login 用托管凭据登录并更新账号状态。并发调用时只登录一次：等锁期间别人已登录成功则直接复用。
// 站点拒绝密码（ErrLoginFailed）时标记 CredentialsRejected，自动登录随之暂停。
func (e *Engine) login(ctx context.Context, instance provider.Provider, account *domain.Account, env provider.Env) error {
	if !account.Credentials.Usable() {
		return ErrNoCredentials
	}
	auth, ok := authenticator(instance, account)
	if !ok {
		return ErrNoAuthenticator
	}
	requested := time.Now()
	lock := e.loginMutex(account.ID)
	lock.Lock()
	defer lock.Unlock()
	if stored, err := e.store.GetAccount(ctx, account.ID); err == nil && stored.LoginAt.After(requested) && stored.Status == domain.AccountActive {
		*account = stored
		return nil
	}
	started := time.Now()
	info, loginErr := auth.Login(ctx, env, account.Credentials)
	e.persistSession(ctx, account, env.Session)
	updated, err := e.updateAccount(ctx, account.ID, func(stored *domain.Account) {
		stored.CheckedAt = time.Now().UTC()
		if loginErr != nil {
			stored.Status = domain.AccountExpired
			stored.LastError = "自动登录失败: " + loginErr.Error()
			stored.CredentialsRejected = errors.Is(loginErr, provider.ErrLoginFailed)
			return
		}
		stored.Status = domain.AccountActive
		stored.LastError = ""
		stored.CredentialsRejected = false
		stored.LoginAt = time.Now().UTC()
		if info.Identity != "" {
			stored.Identity = info.Identity
		}
		if info.Balance != nil {
			stored.Balance = info.Balance
		}
	})
	if err == nil {
		*account = updated
		e.bus.Publish("account", updated)
	}
	if loginErr != nil {
		e.log.Warn("auto login failed", "account", account.ID, "site", account.SiteID, "err", loginErr)
		body := "站点 " + account.SiteID + "：" + loginErr.Error()
		if errors.Is(loginErr, provider.ErrLoginFailed) {
			body += "\n站点拒绝了当前密码，已暂停自动登录；修改密码或手动点「登录」后恢复"
		}
		e.recordEvent(ctx, domain.Event{Type: domain.EventAccount, SiteID: account.SiteID, Title: "账号自动登录失败: " + account.Label, Detail: loginErr.Error()},
			&notify.Message{Title: "账号自动登录失败: " + account.Label, Body: body})
		return fmt.Errorf("auto login: %w", loginErr)
	}
	e.log.Info("auto login ok", "account", account.ID, "site", account.SiteID, "elapsed", time.Since(started))
	return nil
}

// withSession 执行一次用到账号会话的调用：
//   - eager（调用必然需要登录态，如下单 / 报价 / 校验）且有凭据但还没有 Cookie：先登录；
//   - 调用返回 ErrSessionExpired 且可自动登录：登录后重试一次。
//
// 无账号、不可自动登录或站点已拒绝当前密码时原样执行 fn。轮询公开库存应传 eager=false，
// 避免为不需要登录的请求去过登录验证码。
func (e *Engine) withSession(ctx context.Context, instance provider.Provider, account *domain.Account, env provider.Env, eager bool, fn func() error) error {
	if !autoLoginAllowed(instance, account) {
		return fn()
	}
	if eager && len(account.Cookies) == 0 {
		if err := e.login(ctx, instance, account, env); err != nil {
			return err
		}
	}
	err := fn()
	if !errors.Is(err, provider.ErrSessionExpired) {
		return err
	}
	if loginErr := e.login(ctx, instance, account, env); loginErr != nil {
		return loginErr
	}
	return fn()
}

// LoginAccount 立即用托管凭据登录（API「登录」按钮）。
func (e *Engine) LoginAccount(ctx context.Context, accountID string) (domain.Account, error) {
	account, err := e.store.GetAccount(ctx, accountID)
	if err != nil {
		return domain.Account{}, err
	}
	if !account.Credentials.Usable() {
		return account, ErrNoCredentials
	}
	site, err := e.store.GetSite(ctx, account.SiteID)
	if err != nil {
		return domain.Account{}, err
	}
	instance, err := e.providerFor(site)
	if err != nil {
		return domain.Account{}, err
	}
	env, err := e.buildEnv(site, &account)
	if err != nil {
		return domain.Account{}, err
	}
	err = e.login(ctx, instance, &account, env)
	return account, err
}

// SetCredentials 更新托管凭据。password 为 nil 表示不修改密码，空串表示清除。
// 凭据变了就解除「密码被拒」；换了用户名则丢弃旧 Cookie，避免继续用旧用户的会话下单。
func (e *Engine) SetCredentials(ctx context.Context, accountID, username string, password *string) (domain.Account, error) {
	userChanged := false
	account, err := e.updateAccount(ctx, accountID, func(stored *domain.Account) {
		previous := stored.Credentials
		stored.Credentials.Username = username
		if password != nil {
			stored.Credentials.Password = *password
		}
		if stored.Credentials == previous {
			return
		}
		stored.CredentialsRejected = false
		if previous.Username != "" && previous.Username != username {
			userChanged = true
			stored.Cookies = nil
			stored.Status = domain.AccountUnknown
			stored.Identity = ""
			stored.Balance = nil
		}
	})
	if err == nil && userChanged {
		e.ResetSession(accountID)
	}
	return account, err
}
