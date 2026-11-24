package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/egress"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/notify"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/store"
)

// authProvider 只认 sid=fresh 的会话；Login 校验密码后种下 sid=fresh（密码 flaky 模拟验证码超时等非拒绝失败）。
type authProvider struct {
	mu     sync.Mutex
	logins int
	orders int
}

var authState = &authProvider{}

func init() {
	provider.Register(provider.Meta{Kind: "fakeauth", Name: "fakeauth", MinInterval: time.Hour, DefaultInterval: time.Hour, Caps: provider.Capabilities{Catalog: true, Order: true, Login: true}}, func(domain.Site) (provider.Provider, error) {
		return authState, nil
	})
}

func (f *authProvider) Meta() provider.Meta {
	meta, _ := provider.Lookup("fakeauth")
	return meta
}

func (f *authProvider) Catalog(context.Context, provider.Env) (domain.Catalog, error) {
	return domain.Catalog{Products: []domain.Product{product("1", domain.StockOut, 100)}}, nil
}

func (f *authProvider) Quote(context.Context, provider.Env, domain.OrderSpec) (domain.Quote, error) {
	return domain.Quote{}, nil
}

func (f *authProvider) PlaceOrder(_ context.Context, env provider.Env, _ domain.OrderSpec) (domain.OrderResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.orders++
	if env.Session.Cookie("sid") != "fresh" {
		return domain.OrderResult{}, provider.ErrSessionExpired
	}
	return domain.OrderResult{OrderID: "1", InvoiceID: "2"}, nil
}

func (f *authProvider) Login(_ context.Context, env provider.Env, creds domain.Credentials) (provider.SessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logins++
	if creds.Password == "flaky" {
		return provider.SessionInfo{}, errors.New("captcha timeout")
	}
	if creds.Password != "right" {
		return provider.SessionInfo{}, fmt.Errorf("%w: wrong password", provider.ErrLoginFailed)
	}
	env.Session.SetCookies([]*http.Cookie{{Name: "sid", Value: "fresh", Path: "/"}})
	return provider.SessionInfo{Identity: creds.Username}, nil
}

func (f *authProvider) reset() (logins, orders int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	logins, orders = f.logins, f.orders
	f.logins, f.orders = 0, 0
	return
}

func newAuthEngine(t *testing.T) (*Engine, *store.Store, domain.Site, *captureNotifier) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	engine, _ := New(Options{Store: st})
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Stop)
	capture := &captureNotifier{}
	engine.Hub().SetNotifiers([]notify.Notifier{capture}, nil)
	site, err := engine.SaveSite(ctx, domain.Site{ID: "fa", Kind: "fakeauth", Name: "FakeAuth", BaseURL: "https://auth.test", Enabled: true, Config: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	authState.reset()
	return engine, st, site, capture
}

func newAuthAccount(t *testing.T, engine *Engine, site domain.Site, password string) domain.Account {
	t.Helper()
	ctx := context.Background()
	username := "me@example.com"
	account, err := engine.SaveAccount(ctx, AccountInput{SiteID: site.ID, Label: "main", Username: &username, Password: &password})
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func ptr(value string) *string { return &value }

// setCredentials 只改凭据（其余字段按原值回传，同界面保存）。
func setCredentials(engine *Engine, account domain.Account, username string, password *string) (domain.Account, error) {
	return engine.SaveAccount(context.Background(), AccountInput{ID: account.ID, SiteID: account.SiteID, Label: account.Label, Username: &username, Password: password})
}

func TestAutoLoginBeforeFirstUse(t *testing.T) {
	ctx := context.Background()
	engine, st, site, _ := newAuthEngine(t)
	account := newAuthAccount(t, engine, site, "right")
	watch, _ := engine.SaveWatch(ctx, domain.Watch{SiteID: site.ID, ProductID: "1", AccountID: account.ID, Enabled: true})
	order, err := engine.OrderWatch(ctx, watch.ID, false, true)
	if err != nil || order.Status != domain.OrderPlaced {
		t.Fatalf("order should succeed after login: %v %+v", err, order)
	}
	if logins, orders := authState.reset(); logins != 1 || orders != 1 {
		t.Fatalf("want 1 login before 1 order, got logins=%d orders=%d", logins, orders)
	}
	stored, _ := st.GetAccount(ctx, account.ID)
	if stored.Status != domain.AccountActive || stored.LoginAt.IsZero() || stored.Identity != "me@example.com" {
		t.Fatalf("account not updated after login: %+v", stored)
	}
	if len(stored.Cookies) == 0 || stored.Cookies[0].Value != "fresh" {
		t.Fatalf("login cookies not persisted: %+v", stored.Cookies)
	}
}

func TestAutoLoginOnExpiredSession(t *testing.T) {
	ctx := context.Background()
	engine, _, site, _ := newAuthEngine(t)
	account := newAuthAccount(t, engine, site, "right")
	if _, err := engine.SetCookies(ctx, account.ID, []*http.Cookie{{Name: "sid", Value: "stale"}}, ""); err != nil {
		t.Fatal(err)
	}
	watch, _ := engine.SaveWatch(ctx, domain.Watch{SiteID: site.ID, ProductID: "1", AccountID: account.ID, Enabled: true})
	order, err := engine.OrderWatch(ctx, watch.ID, false, true)
	if err != nil || order.Status != domain.OrderPlaced {
		t.Fatalf("order should succeed after re-login: %v %+v", err, order)
	}
	if logins, orders := authState.reset(); logins != 1 || orders != 2 {
		t.Fatalf("want expired attempt + login + retry, got logins=%d orders=%d", logins, orders)
	}
}

func TestAutoLoginWrongPassword(t *testing.T) {
	ctx := context.Background()
	engine, st, site, capture := newAuthEngine(t)
	account := newAuthAccount(t, engine, site, "wrong")
	watch, _ := engine.SaveWatch(ctx, domain.Watch{SiteID: site.ID, ProductID: "1", AccountID: account.ID, Enabled: true})
	order, err := engine.OrderWatch(ctx, watch.ID, false, true)
	if err == nil || order.Status != domain.OrderFailed || !strings.Contains(order.Error, "auto login") {
		t.Fatalf("want login failure, got %v %+v", err, order)
	}
	logins, orders := authState.reset()
	if orders != 0 || logins != 1 {
		t.Fatalf("rejected login must not be retried nor followed by an order: logins=%d orders=%d", logins, orders)
	}
	stored, _ := st.GetAccount(ctx, account.ID)
	if stored.Status != domain.AccountExpired || !strings.Contains(stored.LastError, "自动登录失败") {
		t.Fatalf("account must be marked expired: %+v", stored)
	}
	waitFor(t, "login failure notification", func() bool {
		capture.mu.Lock()
		defer capture.mu.Unlock()
		for _, message := range capture.messages {
			if strings.Contains(message.Title, "自动登录失败") {
				return true
			}
		}
		return false
	})
}

func TestLoginAccountAndCredentialStorage(t *testing.T) {
	ctx := context.Background()
	engine, st, site, _ := newAuthEngine(t)
	account := newAuthAccount(t, engine, site, "right")
	raw, _ := json.Marshal(account)
	if strings.Contains(string(raw), "right") {
		t.Fatalf("password leaked into JSON: %s", raw)
	}
	updated, err := engine.LoginAccount(ctx, account.ID)
	if err != nil || updated.Status != domain.AccountActive {
		t.Fatalf("manual login failed: %v %+v", err, updated)
	}
	// 只改用户名不动密码（省略或回传掩码都表示不修改）。
	if _, err := setCredentials(engine, account, "new@example.com", ptr(SecretMask)); err != nil {
		t.Fatal(err)
	}
	stored, _ := st.GetAccount(ctx, account.ID)
	if stored.Credentials.Username != "new@example.com" || stored.Credentials.Password != "right" {
		t.Fatalf("credentials not stored correctly: %+v", stored.Credentials)
	}
	if _, err := setCredentials(engine, account, "new@example.com", ptr("")); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.LoginAccount(ctx, account.ID); err == nil {
		t.Fatal("login without password must fail")
	}
}

func TestRejectedPasswordPausesAutoLogin(t *testing.T) {
	ctx := context.Background()
	engine, st, site, _ := newAuthEngine(t)
	account := newAuthAccount(t, engine, site, "wrong")
	watch, _ := engine.SaveWatch(ctx, domain.Watch{SiteID: site.ID, ProductID: "1", AccountID: account.ID, Enabled: true})
	_, _ = engine.OrderWatch(ctx, watch.ID, false, true)
	_, _ = engine.OrderWatch(ctx, watch.ID, false, true)
	if logins, _ := authState.reset(); logins != 1 {
		t.Fatalf("rejected password must not be retried automatically, got %d logins", logins)
	}
	stored, _ := st.GetAccount(ctx, account.ID)
	if !stored.CredentialsRejected {
		t.Fatalf("account should be flagged: %+v", stored)
	}
	// 手动登录不受暂停限制。
	_, _ = engine.LoginAccount(ctx, account.ID)
	if logins, _ := authState.reset(); logins != 1 {
		t.Fatalf("manual login must always try, got %d", logins)
	}
	// 改密码后恢复自动登录。
	if _, err := setCredentials(engine, account, "me@example.com", ptr("right")); err != nil {
		t.Fatal(err)
	}
	order, err := engine.OrderWatch(ctx, watch.ID, false, true)
	if err != nil || order.Status != domain.OrderPlaced {
		t.Fatalf("order should work after fixing password: %v %+v", err, order)
	}
}

func TestUsernameChangeDropsOldSession(t *testing.T) {
	ctx := context.Background()
	engine, st, site, _ := newAuthEngine(t)
	account := newAuthAccount(t, engine, site, "right")
	_, _ = engine.SetCookies(ctx, account.ID, []*http.Cookie{{Name: "sid", Value: "fresh"}}, "")
	if _, err := setCredentials(engine, account, "other@example.com", nil); err != nil {
		t.Fatal(err)
	}
	stored, _ := st.GetAccount(ctx, account.ID)
	if len(stored.Cookies) != 0 || stored.Status != domain.AccountUnknown {
		t.Fatalf("old user's cookies must be dropped: %+v", stored)
	}
	// 只改密码不动 Cookie。
	_, _ = engine.SetCookies(ctx, account.ID, []*http.Cookie{{Name: "sid", Value: "fresh"}}, "")
	_, _ = setCredentials(engine, account, "other@example.com", ptr("right"))
	stored, _ = st.GetAccount(ctx, account.ID)
	if len(stored.Cookies) != 1 {
		t.Fatalf("password-only change must keep cookies: %+v", stored.Cookies)
	}
}

func TestPublicPollDoesNotPreLogin(t *testing.T) {
	ctx := context.Background()
	engine, _, site, _ := newAuthEngine(t)
	account := newAuthAccount(t, engine, site, "right")
	site.MonitorAccount = account.ID
	if _, err := engine.SaveSite(ctx, site); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.PollNow(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	if logins, _ := authState.reset(); logins != 0 {
		t.Fatalf("public catalog poll must not log in, got %d", logins)
	}
}

func TestIDSchemeChanged(t *testing.T) {
	old := []domain.Product{{ID: "21"}, {ID: "22"}}
	if !idSchemeChanged(old, []domain.Product{{ID: "pinnacle-network-2"}}) {
		t.Fatal("disjoint ids must be detected")
	}
	if idSchemeChanged(old, []domain.Product{{ID: "21"}, {ID: "new"}}) || idSchemeChanged(nil, old) || idSchemeChanged(old, nil) {
		t.Fatal("overlap / empty sides are normal polls")
	}
}

func TestEgressClientAndRateLimitNotRetried(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "test.db"))
	defer st.Close()
	engine, _ := New(Options{Store: st})
	engine.ApplySettings(Settings{Egress: egress.Settings{Proxies: []egress.Proxy{{ID: "dmit", URL: "socks5://127.0.0.1:10820", Enabled: true}}}})
	if _, ok := engine.clientFor(domain.Site{ID: "a"}).(*egress.Client); !ok {
		t.Fatal("sites must go through the egress client")
	}
	client := engine.clientFor(domain.Site{ID: "b", Egress: []string{"missing"}})
	if _, err := client.Do(ctx, nil, &fetch.Request{URL: "https://x"}); !errors.Is(err, egress.ErrNoEgress) {
		t.Fatalf("unusable explicit egress must surface an error, got %v", err)
	}
	if retryable(fmt.Errorf("load: %w", &fetch.RateLimitError{RetryAfter: time.Hour})) {
		t.Fatal("rate limits must not be retried within an order")
	}
}

func accountNotices(capture *captureNotifier) int {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	count := 0
	for _, message := range capture.messages {
		if message.Event == string(domain.EventAccount) {
			count++
		}
	}
	return count
}

func TestFailedLoginPausesAutoLoginAndNotifiesOnce(t *testing.T) {
	ctx := context.Background()
	engine, st, site, capture := newAuthEngine(t)
	account := newAuthAccount(t, engine, site, "flaky")
	watch, _ := engine.SaveWatch(ctx, domain.Watch{SiteID: site.ID, ProductID: "1", AccountID: account.ID, Enabled: true})
	for i := 0; i < 3; i++ {
		if order, _ := engine.OrderWatch(ctx, watch.ID, false, true); order.Status != domain.OrderFailed {
			t.Fatalf("order must fail: %+v", order)
		}
	}
	if logins, _ := authState.reset(); logins != 1 {
		t.Fatalf("auto login must pause after a failure, got %d logins", logins)
	}
	stored, _ := st.GetAccount(ctx, account.ID)
	if stored.CredentialsRejected || stored.Status != domain.AccountExpired {
		t.Fatalf("non-rejection failure must not flag credentials: %+v", stored)
	}
	// 手动登录不受暂停限制；状态未变（仍失效）不重复通知。
	if _, err := engine.LoginAccount(ctx, account.ID); !errors.Is(err, ErrUpstream) {
		t.Fatalf("manual login error must be upstream: %v", err)
	}
	if logins, _ := authState.reset(); logins != 1 {
		t.Fatalf("manual login must bypass the pause, got %d", logins)
	}
	waitFor(t, "account notice", func() bool { return accountNotices(capture) >= 1 })
	time.Sleep(100 * time.Millisecond)
	if n := accountNotices(capture); n != 1 {
		t.Fatalf("account alerts must fire once per transition, got %d", n)
	}
	// 修改密码解除暂停。
	if _, err := setCredentials(engine, account, "me@example.com", ptr("right")); err != nil {
		t.Fatal(err)
	}
	if order, err := engine.OrderWatch(ctx, watch.ID, false, true); err != nil || order.Status != domain.OrderPlaced {
		t.Fatalf("credential change must lift the pause: %v %+v", err, order)
	}
}

func TestMarkAccountStatusNotifiesOnTransition(t *testing.T) {
	ctx := context.Background()
	engine, st, site, capture := newAuthEngine(t)
	account, _ := engine.SaveAccount(ctx, AccountInput{SiteID: site.ID, Label: "main"})
	for i := 0; i < 3; i++ {
		engine.markAccountStatus(ctx, account.ID, domain.AccountExpired, "session expired")
	}
	events, _ := st.ListEvents(ctx, site.ID, 50)
	accountEvents := 0
	for _, event := range events {
		if event.Type == domain.EventAccount {
			accountEvents++
		}
	}
	if accountEvents != 1 {
		t.Fatalf("want 1 account event, got %d", accountEvents)
	}
	waitFor(t, "account notice", func() bool { return accountNotices(capture) == 1 })
	// 恢复后再次失效重新提醒。
	engine.markAccountStatus(ctx, account.ID, domain.AccountActive, "")
	engine.markAccountStatus(ctx, account.ID, domain.AccountExpired, "session expired")
	waitFor(t, "second notice", func() bool { return accountNotices(capture) == 2 })
}

func TestPersistSessionSkipsReplacedCookies(t *testing.T) {
	ctx := context.Background()
	engine, st, site, _ := newAuthEngine(t)
	account, _ := engine.SaveAccount(ctx, AccountInput{SiteID: site.ID, Label: "main", Cookies: []*http.Cookie{{Name: "sid", Value: "old"}}})
	inflight, err := engine.prepare(ctx, site, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 在途调用期间用户粘贴了新 Cookie；旧会话随后滚动刷新并试图写回。
	if _, err := engine.SetCookies(ctx, account.ID, []*http.Cookie{{Name: "sid", Value: "pasted"}}, ""); err != nil {
		t.Fatal(err)
	}
	inflight.env.Session.SetCookies([]*http.Cookie{{Name: "sid", Value: "rolled-old", Path: "/"}})
	engine.persistSession(ctx, inflight.account, inflight.env.Session)
	if stored, _ := st.GetAccount(ctx, account.ID); len(stored.Cookies) != 1 || stored.Cookies[0].Value != "pasted" {
		t.Fatalf("stale session overwrote pasted cookies: %+v", stored.Cookies)
	}
	// 当前会话照常回写。
	current, _ := engine.prepare(ctx, site, account.ID)
	if current.env.Session.Cookie("sid") != "pasted" {
		t.Fatalf("new session must start from pasted cookies, got %q", current.env.Session.Cookie("sid"))
	}
	current.env.Session.SetCookies([]*http.Cookie{{Name: "sid", Value: "rolled", Path: "/"}})
	engine.persistSession(ctx, current.account, current.env.Session)
	if stored, _ := st.GetAccount(ctx, account.ID); stored.Cookies[0].Value != "rolled" {
		t.Fatalf("current session not persisted: %+v", stored.Cookies)
	}
}

func TestSaveAccountAtomicCreate(t *testing.T) {
	ctx := context.Background()
	engine, st, site, _ := newAuthEngine(t)
	if _, err := engine.SaveAccount(ctx, AccountInput{SiteID: "missing"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown site must be invalid, got %v", err)
	}
	account, err := engine.SaveAccount(ctx, AccountInput{SiteID: site.ID, Label: "x", Username: ptr(" u "), Password: ptr("p"), Cookies: []*http.Cookie{{Name: "sid", Value: "v"}}})
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := st.GetAccount(ctx, account.ID)
	if stored.Credentials != (domain.Credentials{Username: "u", Password: "p"}) || len(stored.Cookies) != 1 || stored.Status != domain.AccountUnknown {
		t.Fatalf("create not applied atomically: %+v", stored)
	}
}
