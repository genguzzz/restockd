package engine

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/store"
)

func TestDeleteAccountRejectsReferencesAndStaysDeleted(t *testing.T) {
	ctx := context.Background()
	engine, st, site, _ := newAuthEngine(t)
	account, _ := engine.SaveAccount(ctx, AccountInput{SiteID: site.ID, Label: "main", Cookies: []*http.Cookie{{Name: "sid", Value: "v"}}})
	watch, err := engine.SaveWatch(ctx, domain.Watch{SiteID: site.ID, ProductID: "1", AccountID: account.ID, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	site.MonitorAccount = account.ID
	if _, err := engine.SaveSite(ctx, site); err != nil {
		t.Fatal(err)
	}
	err = engine.DeleteAccount(ctx, account.ID)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), watch.ID) || !strings.Contains(err.Error(), "site "+site.ID) {
		t.Fatalf("referenced account must not be deleted, got %v", err)
	}
	site.MonitorAccount = ""
	if _, err := engine.SaveSite(ctx, site); err != nil {
		t.Fatal(err)
	}
	if err := engine.DeleteWatch(ctx, watch.ID); err != nil {
		t.Fatal(err)
	}
	inflight, _ := engine.prepare(ctx, site, account.ID)
	if err := engine.DeleteAccount(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	// 删除后在途调用的写回不能让账号 / 关注复活。
	engine.persistSession(ctx, inflight.account, inflight.env.Session)
	engine.markAccountStatus(ctx, account.ID, domain.AccountExpired, "x")
	if _, err := st.GetAccount(ctx, account.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted account resurrected: %v", err)
	}
	if err := engine.touchWatch(ctx, watch.ID, true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted watch resurrected: %v", err)
	}
	if _, err := st.GetWatch(ctx, watch.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted watch resurrected: %v", err)
	}
}

func TestSaveSiteValidatesMonitorAccount(t *testing.T) {
	ctx := context.Background()
	engine, _, site, _ := newAuthEngine(t)
	site.MonitorAccount = "missing"
	if _, err := engine.SaveSite(ctx, site); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown monitor account must be invalid, got %v", err)
	}
}

func TestDeleteSiteDropsCachedSessions(t *testing.T) {
	ctx := context.Background()
	engine, _, site, _ := newAuthEngine(t)
	account, _ := engine.SaveAccount(ctx, AccountInput{SiteID: site.ID, Label: "main"})
	if _, err := engine.prepare(ctx, site, account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.prepare(ctx, site, ""); err != nil {
		t.Fatal(err)
	}
	if err := engine.DeleteSite(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if len(engine.sessions) != 0 {
		t.Fatalf("cached sessions survived site deletion: %v", engine.sessions)
	}
}
