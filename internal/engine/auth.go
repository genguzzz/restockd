package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/provider"
)

// 一次 Provider 调用（轮询 / 下单 / 报价 / 校验 / 登录）：prepare 组装环境，run 执行并处理自动登录与 Cookie 回写。
// 账号托管了用户名密码且 Provider 实现 Authenticator 时，会话失效（或还没有 Cookie）由引擎自动登录并重试。

// loginPause 非密码错误（验证码超时、网络等）的自动登录失败后暂停自动登录的时长，避免每轮轮询都去登录。
const loginPause = 5 * time.Minute

// errAutoLogin 自动登录失败（包装站点原因）；锁单不重试，登录自有暂停机制。
var errAutoLogin = errors.New("auto login")

// call 一次调用的上下文；account 为 nil 表示匿名（站点级会话）。
type call struct {
	site    domain.Site
	account *domain.Account
	inst    provider.Provider
	env     provider.Env
}

// prepare 组装站点（及可选账号）的调用环境。
func (e *Engine) prepare(ctx context.Context, site domain.Site, accountID string) (*call, error) {
	inst, err := e.providerFor(site)
	if err != nil {
		return nil, err
	}
	c := &call{site: site, inst: inst}
	if accountID == "" {
		c.env.Session, err = e.sessionFor(site, nil)
	} else {
		c.account, c.env.Session, err = e.accountSession(ctx, site, accountID)
	}
	if err != nil {
		return nil, err
	}
	c.env.Site, c.env.Account = site, c.account
	c.env.Client = e.clientFor(site)
	c.env.Solver = e.currentSolver()
	c.env.Log = e.log.With("site", site.ID)
	return c, nil
}

// accountCall 以账号为入口（校验 / 登录）：先按账号找站点。
func (e *Engine) accountCall(ctx context.Context, accountID string) (*call, error) {
	account, err := e.store.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	site, err := e.store.GetSite(ctx, account.SiteID)
	if err != nil {
		return nil, err
	}
	return e.prepare(ctx, site, accountID)
}

// run 执行 fn（见 withSession），随后把会话 Cookie 写回账号；失败标记为 ErrUpstream。
func (e *Engine) run(ctx context.Context, c *call, eager bool, fn func() error) error {
	err := e.withSession(ctx, c, eager, fn)
	e.persistSession(ctx, c.account, c.env.Session)
	return upstream(err)
}

// withSession 执行一次用到账号会话的调用：
//   - eager（调用必然需要登录态，如下单 / 报价 / 校验）且可自动登录但还没有 Cookie：先登录；
//   - 调用返回 ErrSessionExpired 且可自动登录：登录后重试一次。
//
// 不可自动登录时原样执行 fn。轮询公开库存应传 eager=false，避免为不需要登录的请求去过登录验证码。
func (e *Engine) withSession(ctx context.Context, c *call, eager bool, fn func() error) error {
	if !e.autoLoginAllowed(c) {
		return fn()
	}
	if eager && len(c.account.Cookies) == 0 {
		if err := e.login(ctx, c, false); err != nil {
			return err
		}
	}
	err := fn()
	if !errors.Is(err, provider.ErrSessionExpired) {
		return err
	}
	if loginErr := e.login(ctx, c, false); loginErr != nil {
		return loginErr
	}
	return fn()
}

// autoLoginAllowed 自动登录的前提：有完整凭据、站点支持登录、站点没有拒绝过当前密码（拒绝后反复重试只会触发封禁），
// 且不在失败后的暂停期内。
func (e *Engine) autoLoginAllowed(c *call) bool {
	if c.account == nil || !c.account.Credentials.Usable() || c.account.CredentialsRejected || e.loginPaused(c.account.ID) {
		return false
	}
	_, ok := c.inst.(provider.Authenticator)
	return ok
}

// pauseLogin 设置账号自动登录暂停截止时间；零值解除。
func (e *Engine) pauseLogin(accountID string, until time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if until.IsZero() {
		delete(e.loginPauses, accountID)
	} else {
		e.loginPauses[accountID] = until
	}
}

func (e *Engine) loginPaused(accountID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return time.Now().Before(e.loginPauses[accountID])
}

// login 用托管凭据登录，并在同一次写入里回写会话 Cookie 与账号状态。
// 并发调用时只登录一次：等锁期间别人已登录成功则直接复用；自动登录等锁期间别人失败进入暂停则不再尝试。
// 站点拒绝密码（ErrLoginFailed）时标记 CredentialsRejected，自动登录随之暂停；其它失败暂停 loginPause。
// manual（界面「登录」）不受暂停限制。账号状态变为失效或密码新被拒时才记事件并通知。
func (e *Engine) login(ctx context.Context, c *call, manual bool) error {
	account := c.account
	if !account.Credentials.Usable() {
		return ErrNoCredentials
	}
	auth, ok := c.inst.(provider.Authenticator)
	if !ok {
		return ErrNoAuthenticator
	}
	requested := time.Now()
	lock := e.loginLocks.Get(account.ID)
	lock.Lock()
	defer lock.Unlock()
	if stored, err := e.store.GetAccount(ctx, account.ID); err == nil && stored.LoginAt.After(requested) && stored.Status == domain.AccountActive {
		*account = stored
		return nil
	}
	if !manual && e.loginPaused(account.ID) {
		return upstream(fmt.Errorf("%w: paused after a recent failure", errAutoLogin))
	}
	started := time.Now()
	info, loginErr := auth.Login(ctx, c.env, account.Credentials)
	rejected := errors.Is(loginErr, provider.ErrLoginFailed)
	switch {
	case loginErr == nil, rejected:
		e.pauseLogin(account.ID, time.Time{})
	default:
		e.pauseLogin(account.ID, time.Now().Add(loginPause))
	}
	var previous domain.Account
	updated, err := e.updateAccount(ctx, account.ID, func(stored *domain.Account) error {
		if err := e.applySession(stored, c.env.Session); err != nil {
			return err
		}
		previous = *stored
		stored.CheckedAt = time.Now().UTC()
		if loginErr != nil {
			stored.Status = domain.AccountExpired
			stored.LastError = "自动登录失败: " + loginErr.Error()
			stored.CredentialsRejected = rejected
			return nil
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
		return nil
	})
	if err == nil {
		*account = updated
	}
	if loginErr != nil {
		e.log.Warn("auto login failed", "account", account.ID, "site", account.SiteID, "err", loginErr)
		if err == nil && (previous.Status != domain.AccountExpired || (rejected && !previous.CredentialsRejected)) {
			event, message := loginFailedNotice(updated, loginErr)
			e.recordEvent(ctx, event, message)
		}
		return upstream(fmt.Errorf("%w: %w", errAutoLogin, loginErr))
	}
	e.log.Info("auto login ok", "account", account.ID, "site", account.SiteID, "elapsed", time.Since(started))
	return nil
}

// LoginAccount 立即用托管凭据登录（界面「登录」按钮），不受自动登录暂停限制。
func (e *Engine) LoginAccount(ctx context.Context, accountID string) (domain.Account, error) {
	ctx, cancel := context.WithTimeout(ctx, e.interactiveTimeout())
	defer cancel()
	c, err := e.accountCall(ctx, accountID)
	if err != nil {
		return domain.Account{}, err
	}
	err = e.login(ctx, c, true)
	return *c.account, err
}

// CheckAccount 校验会话并更新账号状态；站点侧的校验失败记在账号状态里（不返回错误）。
// Provider 不能校验但能登录时，校验即登录。
func (e *Engine) CheckAccount(ctx context.Context, accountID string) (domain.Account, error) {
	ctx, cancel := context.WithTimeout(ctx, e.interactiveTimeout())
	defer cancel()
	c, err := e.accountCall(ctx, accountID)
	if err != nil {
		return domain.Account{}, err
	}
	checker, ok := c.inst.(provider.SessionChecker)
	if !ok {
		if _, canLogin := c.inst.(provider.Authenticator); !canLogin || !c.account.Credentials.Usable() {
			return domain.Account{}, provider.ErrNotSupported
		}
		if err := e.login(ctx, c, true); err != nil && !errors.Is(err, ErrUpstream) {
			return domain.Account{}, err
		}
		return *c.account, nil
	}
	var info provider.SessionInfo
	checkErr := e.run(ctx, c, true, func() error {
		var err error
		info, err = checker.CheckSession(ctx, c.env)
		return err
	})
	return e.updateAccount(ctx, accountID, func(stored *domain.Account) error {
		stored.CheckedAt = time.Now().UTC()
		if checkErr != nil {
			stored.Status = domain.AccountUnknown
			if errors.Is(checkErr, provider.ErrSessionExpired) || errors.Is(checkErr, provider.ErrLoginFailed) {
				stored.Status = domain.AccountExpired
			}
			stored.LastError = checkErr.Error()
			return nil
		}
		stored.Status = domain.AccountActive
		stored.LastError = ""
		stored.Identity = info.Identity
		stored.Balance = info.Balance
		return nil
	})
}
