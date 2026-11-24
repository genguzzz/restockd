package engine

import (
	"context"
	"sync"
	"time"

	"github.com/genguzzz/restockd/internal/provider"
)

// 按需轮询：站点有启用中的关注规则时按站点间隔轮询；没有关注时降频——有人在看（管理界面 SSE 连着，
// 或最近 ViewerInterval 内有已登录的 API 请求）按 ViewerInterval，否则按 IdleInterval。
// 有人开始访问时，无关注的站点立即刷一次（最近 viewerRefreshMin 内刚刷过的跳过）。

const (
	defaultIdleInterval   = time.Hour
	defaultViewerInterval = 5 * time.Minute
)

// viewerRefreshMin 访问触发的即时刷新与上次轮询的最小间隔，避免刷新页面就打一轮站点（测试可调小）。
var viewerRefreshMin = time.Minute

// PollingSettings 无关注站点的降频策略；0 表示使用默认值。
type PollingSettings struct {
	IdleIntervalSec   int `json:"idle_interval_sec"`
	ViewerIntervalSec int `json:"viewer_interval_sec"`
}

func (p PollingSettings) idle() time.Duration {
	if p.IdleIntervalSec > 0 {
		return time.Duration(p.IdleIntervalSec) * time.Second
	}
	return defaultIdleInterval
}

func (p PollingSettings) viewer() time.Duration {
	if p.ViewerIntervalSec > 0 {
		return time.Duration(p.ViewerIntervalSec) * time.Second
	}
	return defaultViewerInterval
}

type wakeKind int

const (
	// wakeReschedule 关注规则变化：按新间隔重排，已到期则立即轮询。
	wakeReschedule wakeKind = iota
	// wakeViewer 有人开始访问：无关注站点立即刷一次。
	wakeViewer
)

// viewerTracker 记录管理界面的访问情况。
type viewerTracker struct {
	mu       sync.Mutex
	lastSeen time.Time
	streams  int
}

// TouchViewer 已登录的 API 请求调用；从「无人访问」变为「有人访问」时唤醒各站点。
func (e *Engine) TouchViewer() {
	window := e.Settings().Polling.viewer()
	e.viewers.mu.Lock()
	wasActive := e.viewers.streams > 0 || time.Since(e.viewers.lastSeen) < window
	e.viewers.lastSeen = time.Now()
	e.viewers.mu.Unlock()
	if !wasActive {
		e.wakeSites(wakeViewer)
	}
}

// ViewerStreamOpened SSE 连接期间视为一直有人在看；返回的函数在连接关闭时调用。
func (e *Engine) ViewerStreamOpened() func() {
	e.viewers.mu.Lock()
	e.viewers.streams++
	e.viewers.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			e.viewers.mu.Lock()
			e.viewers.streams--
			e.viewers.lastSeen = time.Now() // 关页后再按访问窗口保持一会儿
			e.viewers.mu.Unlock()
		})
	}
}

func (e *Engine) viewerActive() bool {
	window := e.Settings().Polling.viewer()
	e.viewers.mu.Lock()
	defer e.viewers.mu.Unlock()
	return e.viewers.streams > 0 || time.Since(e.viewers.lastSeen) < window
}

// wakeSites 非阻塞通知所有站点协程；协程忙时丢弃（它轮询完会按最新状态重新排期）。
func (e *Engine) wakeSites(kind wakeKind) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, runner := range e.sites {
		select {
		case runner.wake <- kind:
		default:
		}
	}
}

// RescheduleSite 关注规则增删改后调用，让站点按新间隔重排。
func (e *Engine) RescheduleSite(siteID string) {
	e.mu.RLock()
	runner, ok := e.sites[siteID]
	e.mu.RUnlock()
	if !ok {
		return
	}
	select {
	case runner.wake <- wakeReschedule:
	default:
	}
}

// siteWatched 站点是否有启用中的关注规则（读库失败按「有」处理，宁可多轮询）。
func (e *Engine) siteWatched(ctx context.Context, siteID string) bool {
	watches, err := e.store.ListWatches(ctx, siteID)
	if err != nil {
		return true
	}
	for _, watch := range watches {
		if watch.Enabled {
			return true
		}
	}
	return false
}

// siteInterval 当前应使用的轮询间隔；降频值不会低于站点自身配置与 Provider 下限。
func (e *Engine) siteInterval(ctx context.Context, runner *siteRunner) (time.Duration, bool) {
	base := provider.EffectiveInterval(runner.provider.Meta(), runner.site)
	if e.siteWatched(ctx, runner.site.ID) {
		return base, true
	}
	lazy := e.Settings().Polling.idle()
	if e.viewerActive() {
		lazy = e.Settings().Polling.viewer()
	}
	if lazy < base {
		lazy = base
	}
	return lazy, false
}
