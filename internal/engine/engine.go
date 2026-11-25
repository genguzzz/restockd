// Package engine 监控引擎：每站点一个调度协程轮询目录（无关注的站点按需降频）、比对快照产生事件、
// 匹配关注规则、执行自动锁单、托管账号会话与自动登录、通知。
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/egress"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/notify"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/solver"
	"github.com/genguzzz/restockd/internal/store"
)

const (
	httpTimeout = 30 * time.Second
	// eventRetention / orderRetention 定期清理的保留期。
	eventRetention = 30 * 24 * time.Hour
	orderRetention = 90 * 24 * time.Hour
)

// Options 引擎构造参数。
type Options struct {
	Store *store.Store
	Log   *slog.Logger
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

	// egress 出口登记表（直连 + 设置里的代理）与共享冷却状态。
	egress       *egress.Registry
	egressHealth *egress.Health

	// 锁顺序：docMu → mu，反之不允许。
	mu       sync.RWMutex
	settings Settings
	flare    *fetch.FlareSolverrClient
	solver   solver.Solver
	sites    map[string]*siteRunner
	sessions map[string]*fetch.Session
	// loginPauses 账号自动登录冷却截止时间（非密码错误的登录失败后）。
	loginPauses map[string]time.Time
	// docMu 串行化库中文档的读改写（账号、站点、关注）。
	docMu   sync.Mutex
	viewers viewerTracker
	// orderLocks 每个关注一把（同一关注不并发锁单）；loginLocks 每个账号一把（串行化登录）。
	orderLocks keyedMutex
	loginLocks keyedMutex

	rootCtx context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// New 构造引擎；调用 Start 后开始调度。
func New(options Options) (*Engine, error) {
	direct, err := fetch.NewHTTPClient(httpTimeout, "")
	if err != nil {
		return nil, err
	}
	logger := options.Log
	if logger == nil {
		logger = slog.Default()
	}
	health := egress.NewHealth()
	engine := &Engine{
		store:        options.Store,
		log:          logger,
		bus:          NewBus(),
		hub:          notify.NewHub(),
		broker:       solver.NewManualBroker(defaultManualTimeout),
		publicURL:    strings.TrimRight(options.PublicURL, "/"),
		egress:       egress.NewRegistry(direct, httpTimeout, health),
		egressHealth: health,
		sites:        map[string]*siteRunner{},
		sessions:     map[string]*fetch.Session{},
		loginPauses:  map[string]time.Time{},
	}
	engine.broker.OnNew = engine.onChallenge
	engine.broker.OnDone = func(id string) { engine.bus.Publish("challenge_done", map[string]string{"id": id}) }
	return engine, nil
}

// Bus 事件总线。
func (e *Engine) Bus() *Bus { return e.bus }

// Broker 人工验证收件箱。
func (e *Engine) Broker() *solver.ManualBroker { return e.broker }

// Hub 通知分发器（测试中替换通知通道）。
func (e *Engine) Hub() *notify.Hub { return e.hub }

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

// Stop 停止所有调度与在途通知并等待退出。
func (e *Engine) Stop() {
	if e.cancel != nil {
		e.cancel()
	}
	e.wg.Wait()
}

// housekeeping 定期清理过期的事件、订单与在售期记录。
func (e *Engine) housekeeping() {
	defer e.wg.Done()
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-e.rootCtx.Done():
			return
		case <-ticker.C:
			if err := e.store.PruneEvents(e.rootCtx, time.Now().Add(-eventRetention)); err != nil {
				e.log.Warn("prune events", "err", err)
			}
			if err := e.store.PruneOrders(e.rootCtx, time.Now().Add(-orderRetention)); err != nil {
				e.log.Warn("prune orders", "err", err)
			}
			if err := e.store.PruneStockWindows(e.rootCtx, time.Now().Add(-stockWindowRetention)); err != nil {
				e.log.Warn("prune stock windows", "err", err)
			}
		}
	}
}

// ApplySettings 热更新出口、通知通道与 Solver 链：外部服务 → FlareSolverr → 人工兜底。
func (e *Engine) ApplySettings(settings Settings) {
	e.egress.Apply(settings.Egress)
	e.hub.Apply(notify.Settings{Telegram: settings.Telegram, Email: settings.Email, Bark: settings.Bark, Events: settings.NotifyEvents})
	e.broker.SetTimeout(settings.Solver.manualTimeout())
	var flare *fetch.FlareSolverrClient
	chain := solver.Chain{}
	if settings.Solver.ExternalURL != "" {
		chain = append(chain, &solver.ExternalSolver{Endpoint: settings.Solver.ExternalURL, Token: settings.Solver.ExternalToken})
	}
	if settings.Solver.FlareSolverrURL != "" {
		flare = fetch.NewFlareSolverrClient(settings.Solver.FlareSolverrURL, 60*time.Second)
	}
	chain = append(chain, e.broker)
	e.mu.Lock()
	e.settings = settings
	e.flare = flare
	e.solver = chain
	e.mu.Unlock()
}

// interactiveTimeout 界面触发的登录 / 校验 / 报价的超时：留足人工验证的等待时间。
func (e *Engine) interactiveTimeout() time.Duration {
	return max(e.Settings().Solver.manualTimeout()+time.Minute, 2*time.Minute)
}

// clientFor 按站点出口与抓取模式选择客户端；未配置 FlareSolverr 时退化为直连。
func (e *Engine) clientFor(site domain.Site) fetch.Client {
	e.mu.RLock()
	flare := e.flare
	e.mu.RUnlock()
	routes, err := e.egress.Routes(site.ID, site.Egress)
	if err != nil {
		return errClient{err: err}
	}
	client := &egress.Client{SiteID: site.ID, Routes: routes, Health: e.egressHealth, Log: e.log}
	if meta, ok := provider.Lookup(site.Kind); ok {
		client.RateLimitCooldown = meta.RateLimitCooldown // 站点 429 无 Retry-After 时的冷却（usvps24 封禁约 6 小时）
	}
	if site.FetchMode == domain.FetchFlareSolverr && flare != nil {
		client.Browser = flare
	}
	return client
}

// errClient 出口配置不可用时让每次请求都返回同一个错误（体现在站点 / 订单错误里）。
type errClient struct{ err error }

func (c errClient) Do(context.Context, *fetch.Session, *fetch.Request) (*fetch.Response, error) {
	return nil, c.err
}

func (e *Engine) currentSolver() solver.Solver {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.solver
}

// providerFor 优先复用运行中的实例（保留 Inertia 版本等缓存）。
func (e *Engine) providerFor(site domain.Site) (provider.Provider, error) {
	e.mu.RLock()
	runner, ok := e.sites[site.ID]
	e.mu.RUnlock()
	if ok {
		return runner.provider, nil
	}
	return provider.New(site)
}

// EgressProbe 出口连通性测试结果（经该出口访问 Cloudflare trace）。
type EgressProbe struct {
	OK    bool   `json:"ok"`
	IP    string `json:"ip,omitempty"`
	Loc   string `json:"loc,omitempty"`
	Colo  string `json:"colo,omitempty"`
	Ms    int64  `json:"ms,omitempty"`
	Error string `json:"error,omitempty"`
}

// TestEgress 经指定出口访问 Cloudflare trace。出口不存在返回 ErrInvalid；请求失败记在 probe.Error。
func (e *Engine) TestEgress(ctx context.Context, id string) (EgressProbe, error) {
	route, err := e.egress.Route(id)
	if err != nil {
		return EgressProbe{}, tagged{err: err, tag: ErrInvalid}
	}
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	session, err := fetch.NewSession("https://www.cloudflare.com", nil, "")
	if err != nil {
		return EgressProbe{}, err
	}
	started := time.Now()
	resp, err := route.Client.Do(ctx, session, &fetch.Request{Method: http.MethodGet, URL: "https://www.cloudflare.com/cdn-cgi/trace"})
	if err != nil {
		return EgressProbe{Error: err.Error()}, nil
	}
	probe := EgressProbe{OK: true, Ms: time.Since(started).Milliseconds()}
	for _, line := range strings.Split(string(resp.Body), "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "ip":
			probe.IP = value
		case "loc":
			probe.Loc = value
		case "colo":
			probe.Colo = value
		}
	}
	return probe, nil
}
