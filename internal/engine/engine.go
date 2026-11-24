// Package engine 监控引擎：每站点一个调度协程轮询目录、比对快照产生事件、匹配关注规则、执行自动锁单与通知。
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/notify"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/solver"
	"github.com/genguzzz/restockd/internal/store"
)

// SettingsKey 设置在 settings 表中的键。
const SettingsKey = "app"

// SolverSettings 验证码求解设置。
type SolverSettings struct {
	FlareSolverrURL  string `json:"flaresolverr_url"`
	ExternalURL      string `json:"external_url"`
	ExternalToken    string `json:"external_token"`
	ManualTimeoutSec int    `json:"manual_timeout_sec"`
}

// Settings 全局设置。
type Settings struct {
	Telegram     notify.TelegramConfig `json:"telegram"`
	Email        notify.EmailConfig    `json:"email"`
	Bark         notify.BarkConfig     `json:"bark"`
	Solver       SolverSettings        `json:"solver"`
	Polling      PollingSettings       `json:"polling"`
	NotifyEvents []string              `json:"notify_events"`
}

// DefaultSettings 初始设置。
func DefaultSettings() Settings {
	return Settings{
		Email:        notify.EmailConfig{Port: 587, StartTLS: true},
		Solver:       SolverSettings{FlareSolverrURL: "http://127.0.0.1:8191", ManualTimeoutSec: 300},
		Polling:      PollingSettings{IdleIntervalSec: int(defaultIdleInterval / time.Second), ViewerIntervalSec: int(defaultViewerInterval / time.Second)},
		NotifyEvents: []string{string(domain.EventRestock), string(domain.EventOrder), string(domain.EventChallenge), string(domain.EventAccount)},
	}
}

// Options 引擎构造参数。
type Options struct {
	Store       *store.Store
	Log         *slog.Logger
	HTTPTimeout time.Duration
	ProxyURL    string
	// PublicURL 本服务对外地址，用于通知里的跳转链接（如人工验证收件箱）。
	PublicURL string
}

// Engine 见包说明。
type Engine struct {
	store     *store.Store
	log       *slog.Logger
	bus       *Bus
	hub       *notify.Hub
	broker    *solver.ManualBroker
	publicURL string

	direct *fetch.DirectClient

	mu       sync.RWMutex
	settings Settings
	flare    *fetch.FlareSolverrClient
	solver   solver.Solver
	sites    map[string]*siteRunner
	sessions map[string]*fetch.Session
	viewers  viewerTracker
	// loginLocks 每个账号一把锁，串行化自动登录。
	loginLocks map[string]*sync.Mutex
	orderLock  map[string]*sync.Mutex
	docMu      sync.Mutex

	rootCtx context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// New 构造引擎；调用 Start 后开始调度。
func New(options Options) (*Engine, error) {
	timeout := options.HTTPTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	direct, err := fetch.NewDirectClient(timeout, options.ProxyURL)
	if err != nil {
		return nil, err
	}
	logger := options.Log
	if logger == nil {
		logger = slog.Default()
	}
	engine := &Engine{
		store:     options.Store,
		log:       logger,
		bus:       NewBus(),
		hub:       notify.NewHub(),
		broker:    solver.NewManualBroker(5 * time.Minute),
		publicURL: strings.TrimRight(options.PublicURL, "/"),
		direct:    direct,
		sites:     map[string]*siteRunner{},
		sessions:  map[string]*fetch.Session{},
		orderLock: map[string]*sync.Mutex{},
	}
	engine.broker.OnNew = engine.onChallenge
	engine.broker.OnDone = func(id string) { engine.bus.Publish("challenge_done", map[string]string{"id": id}) }
	return engine, nil
}

// Bus 事件总线。
func (e *Engine) Bus() *Bus { return e.bus }

// Broker 人工验证收件箱。
func (e *Engine) Broker() *solver.ManualBroker { return e.broker }

// Start 载入设置并为每个启用站点启动调度协程。
func (e *Engine) Start(ctx context.Context) error {
	e.rootCtx, e.cancel = context.WithCancel(ctx)
	settings := DefaultSettings()
	if err := e.store.GetSetting(ctx, SettingsKey, &settings); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("load settings: %w", err)
	}
	e.ApplySettings(settings)
	sites, err := e.store.ListSites(ctx)
	if err != nil {
		return err
	}
	for _, site := range sites {
		if err := e.ReloadSite(site); err != nil {
			e.log.Warn("site not started", "site", site.ID, "err", err)
		}
	}
	e.wg.Add(1)
	go e.housekeeping()
	return nil
}

// Stop 停止所有调度并等待退出。
func (e *Engine) Stop() {
	if e.cancel != nil {
		e.cancel()
	}
	e.wg.Wait()
}

// housekeeping 定期清理 30 天前的事件。
func (e *Engine) housekeeping() {
	defer e.wg.Done()
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-e.rootCtx.Done():
			return
		case <-ticker.C:
			if err := e.store.PruneEvents(e.rootCtx, time.Now().Add(-30*24*time.Hour)); err != nil {
				e.log.Warn("prune events", "err", err)
			}
		}
	}
}

// Settings 当前设置副本。
func (e *Engine) Settings() Settings {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.settings
}

// ApplySettings 热更新通知通道与 Solver 链：外部服务 → FlareSolverr → 人工兜底。
func (e *Engine) ApplySettings(settings Settings) {
	e.hub.Apply(notify.Settings{Telegram: settings.Telegram, Email: settings.Email, Bark: settings.Bark, Events: settings.NotifyEvents})
	timeout := time.Duration(settings.Solver.ManualTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	e.broker.SetTimeout(timeout)
	var flare *fetch.FlareSolverrClient
	chain := solver.Chain{}
	if settings.Solver.ExternalURL != "" {
		chain = append(chain, &solver.ExternalSolver{Endpoint: settings.Solver.ExternalURL, Token: settings.Solver.ExternalToken})
	}
	if settings.Solver.FlareSolverrURL != "" {
		flare = fetch.NewFlareSolverrClient(settings.Solver.FlareSolverrURL, 60*time.Second)
		chain = append(chain, &solver.FlareSolverrSolver{Client: flare})
	}
	chain = append(chain, e.broker)
	e.mu.Lock()
	e.settings = settings
	e.flare = flare
	e.solver = chain
	e.mu.Unlock()
}

// SaveSettings 持久化并应用。
func (e *Engine) SaveSettings(ctx context.Context, settings Settings) error {
	if err := e.store.PutSetting(ctx, SettingsKey, settings); err != nil {
		return err
	}
	e.ApplySettings(settings)
	return nil
}

// TestNotify 向所有启用通道发送测试消息。
func (e *Engine) TestNotify(ctx context.Context) map[string]error {
	return e.hub.Notify(ctx, notify.Message{Title: "restockd 测试通知", Body: "如果你看到这条消息，说明通知通道配置正确。"})
}

// clientFor 按站点抓取模式选择客户端；未配置 FlareSolverr 时退化为直连。
func (e *Engine) clientFor(site domain.Site) fetch.Client {
	e.mu.RLock()
	flare := e.flare
	e.mu.RUnlock()
	if site.FetchMode == domain.FetchFlareSolverr && flare != nil {
		return &fetch.HybridClient{Primary: e.direct, Fallback: flare}
	}
	return e.direct
}

func (e *Engine) currentSolver() solver.Solver {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.solver
}

// sessionFor 账号会话缓存：首次从库中 Cookie 构建，之后复用以保留站点刷新的 Cookie。
// accountID 为空时返回站点级匿名会话（保留 cf_clearance 等）。
func (e *Engine) sessionFor(site domain.Site, account *domain.Account) (*fetch.Session, error) {
	key := "anon:" + site.ID
	if account != nil {
		key = "acct:" + account.ID
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

// ResetSession 账号 Cookie 被替换后丢弃缓存会话。
func (e *Engine) ResetSession(accountID string) {
	e.mu.Lock()
	delete(e.sessions, "acct:"+accountID)
	e.mu.Unlock()
}

// buildEnv 组装一次 Provider 调用的环境。
func (e *Engine) buildEnv(site domain.Site, account *domain.Account) (provider.Env, error) {
	session, err := e.sessionFor(site, account)
	if err != nil {
		return provider.Env{}, err
	}
	return provider.Env{Site: site, Client: e.clientFor(site), Session: session, Account: account, Solver: e.currentSolver(), Log: e.log.With("site", site.ID)}, nil
}

// persistSession 把会话中最新 Cookie 写回账号（站点会滚动刷新 laravel_session / cf_clearance）。
func (e *Engine) persistSession(ctx context.Context, account *domain.Account, session *fetch.Session) {
	if account == nil || session == nil {
		return
	}
	e.updateAccount(ctx, account.ID, func(stored *domain.Account) {
		stored.Cookies = session.Cookies()
		if session.UserAgent() != "" && session.UserAgent() != fetch.DefaultUserAgent {
			stored.UserAgent = session.UserAgent()
		}
	})
}

// updateAccount 串行化账号读改写，避免调度与 API 同时写覆盖。
func (e *Engine) updateAccount(ctx context.Context, id string, mutate func(*domain.Account)) (domain.Account, error) {
	e.docMu.Lock()
	defer e.docMu.Unlock()
	account, err := e.store.GetAccount(ctx, id)
	if err != nil {
		return domain.Account{}, err
	}
	mutate(&account)
	account.SyncCookieNames()
	if err := e.store.SaveAccount(ctx, account); err != nil {
		return domain.Account{}, err
	}
	return account, nil
}

// UpdateAccount 对外暴露的串行化更新。
func (e *Engine) UpdateAccount(ctx context.Context, id string, mutate func(*domain.Account)) (domain.Account, error) {
	return e.updateAccount(ctx, id, mutate)
}

// recordEvent 写库、广播，并按类型发送通知。
func (e *Engine) recordEvent(ctx context.Context, event domain.Event, message *notify.Message) {
	if err := e.store.AddEvent(ctx, &event); err != nil {
		e.log.Warn("save event", "err", err)
	}
	e.bus.Publish("event", event)
	if message == nil {
		return
	}
	message.Event = string(event.Type)
	go func() {
		notifyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for channel, err := range e.hub.Notify(notifyCtx, *message) {
			if err != nil {
				e.log.Warn("notify failed", "channel", channel, "err", err)
			}
		}
	}()
}

// onChallenge 人工验证挂起：推送界面并通知用户去处理。
func (e *Engine) onChallenge(pending solver.Pending) {
	e.bus.Publish("challenge", pending)
	link := ""
	if e.publicURL != "" {
		link = e.publicURL + "/challenges"
	}
	e.recordEvent(context.Background(), domain.Event{Type: domain.EventChallenge, SiteID: pending.Challenge.SiteID, Title: "需要人工验证: " + pending.Challenge.Purpose, Detail: fmt.Sprintf("%s，%s 前处理", pending.Challenge.Kind, pending.ExpiresAt.Local().Format("15:04:05"))},
		&notify.Message{Title: "需要人工验证 " + string(pending.Challenge.Kind), Body: pending.Challenge.Purpose + "\n请在过期前打开收件箱粘贴 token / Cookie", URL: link})
}

// Hub 通知分发器（测试中替换通知通道）。
func (e *Engine) Hub() *notify.Hub { return e.hub }
