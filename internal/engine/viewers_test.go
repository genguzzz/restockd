package engine

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/store"
)

// lazyProvider 间隔很短的假站点，用来观察调度。
type lazyProvider struct{ polls atomic.Int32 }

var lazyState = &lazyProvider{}

func init() {
	provider.Register(provider.Meta{Kind: "fakelazy", Name: "fakelazy", MinInterval: time.Second, DefaultInterval: time.Second, Caps: provider.Capabilities{Catalog: true}}, func(domain.Site) (provider.Provider, error) {
		return lazyState, nil
	})
}

func (f *lazyProvider) Meta() provider.Meta {
	meta, _ := provider.Lookup("fakelazy")
	return meta
}

func (f *lazyProvider) Catalog(context.Context, provider.Env) (domain.Catalog, error) {
	f.polls.Add(1)
	return domain.Catalog{Products: []domain.Product{product("1", domain.StockOut, 100)}}, nil
}

func nextPollIn(t *testing.T, st *store.Store, siteID string) time.Duration {
	t.Helper()
	site, err := st.GetSite(context.Background(), siteID)
	if err != nil {
		t.Fatal(err)
	}
	return time.Until(site.NextPollAt)
}

func TestOnDemandPolling(t *testing.T) {
	restore := viewerRefreshMin
	viewerRefreshMin = 0
	defer func() { viewerRefreshMin = restore }()
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "test.db"))
	defer st.Close()
	engine, _ := New(Options{Store: st})
	_ = engine.Start(ctx)
	defer engine.Stop()
	engine.Hub().SetNotifiers(nil, nil)
	lazyState.polls.Store(0)
	site, err := engine.SaveSite(ctx, domain.Site{ID: "lz", Kind: "fakelazy", Name: "Lazy", BaseURL: "https://lazy.test", Enabled: true, IntervalSec: 1, Config: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	// 无关注、无人访问：首轮后排到 ~1h。
	waitFor(t, "first poll", func() bool { return lazyState.polls.Load() >= 1 })
	waitFor(t, "idle schedule", func() bool { return nextPollIn(t, st, site.ID) > 50*time.Minute })

	// 有人访问：立即刷一次，之后 ~5min。
	before := lazyState.polls.Load()
	engine.TouchViewer()
	waitFor(t, "viewer refresh", func() bool { return lazyState.polls.Load() > before })
	waitFor(t, "viewer schedule", func() bool {
		next := nextPollIn(t, st, site.ID)
		return next > 4*time.Minute && next < 6*time.Minute
	})
	// 访问持续时再次 Touch 不重复触发。
	steady := lazyState.polls.Load()
	engine.TouchViewer()
	time.Sleep(200 * time.Millisecond)
	if lazyState.polls.Load() != steady {
		t.Fatal("touch while already active must not trigger another poll")
	}

	// 加关注：立即按站点间隔（1s）轮询。
	watch, err := engine.SaveWatch(ctx, domain.Watch{SiteID: site.ID, ProductID: "1", Enabled: true, Notify: true})
	if err != nil {
		t.Fatal(err)
	}
	afterWatch := lazyState.polls.Load()
	waitFor(t, "watched fast polling", func() bool { return lazyState.polls.Load() >= afterWatch+2 })

	// 删关注：降回低频（访问中 → ~5min）。
	if err := engine.DeleteWatch(ctx, watch.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "back to lazy", func() bool { return nextPollIn(t, st, site.ID) > 4*time.Minute })
}

func TestSiteIntervalNeverBelowSiteSetting(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "test.db"))
	defer st.Close()
	engine, _ := New(Options{Store: st})
	engine.ApplySettings(Settings{Polling: PollingSettings{IdleIntervalSec: 60, ViewerIntervalSec: 30}})
	runner := &siteRunner{site: domain.Site{ID: "x", IntervalSec: 600}, provider: lazyState}
	if interval, watched := engine.siteInterval(ctx, runner); watched || interval != 10*time.Minute {
		t.Fatalf("lazy interval must not undercut the site's own interval, got %s", interval)
	}
}
