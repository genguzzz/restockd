package engine

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/store"
)

func TestCancelOrder(t *testing.T) {
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(t.TempDir(), "test.db"))
	defer st.Close()
	engine, _ := New(Options{Store: st})
	_ = engine.Start(ctx)
	defer engine.Stop()
	engine.Hub().SetNotifiers(nil, nil)
	fakeState.set(product("41", domain.StockIn, 500))
	fakeState.mu.Lock()
	fakeState.orders, fakeState.orderErr, fakeState.cancelled, fakeState.cancelErr = nil, nil, nil, nil
	fakeState.mu.Unlock()
	site, _ := engine.SaveSite(ctx, domain.Site{ID: "fkc", Kind: "fake", Name: "FakeC", BaseURL: "https://fake.test", Enabled: true, Config: map[string]string{}})
	account, _ := engine.SaveAccount(ctx, AccountInput{SiteID: site.ID, Label: "main", Cookies: []*http.Cookie{{Name: "sid", Value: "s1"}}})
	watch, _ := engine.SaveWatch(ctx, domain.Watch{SiteID: site.ID, ProductID: "41", AccountID: account.ID, Enabled: true})

	dry, err := engine.OrderWatch(ctx, watch.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.CancelOrder(ctx, dry.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("dry runs cannot be cancelled, got %v", err)
	}
	placed, err := engine.OrderWatch(ctx, watch.ID, false, false)
	if err != nil || placed.Status != domain.OrderPlaced {
		t.Fatalf("order failed: %v %+v", err, placed)
	}

	fakeState.mu.Lock()
	fakeState.cancelErr = errors.New("站点不允许: " + provider.ErrNotSupported.Error())
	fakeState.mu.Unlock()
	if _, err := engine.CancelOrder(ctx, placed.ID); err == nil {
		t.Fatal("provider failure must surface")
	}
	if stored, _ := st.GetOrder(ctx, placed.ID); stored.Status != domain.OrderPlaced {
		t.Fatalf("failed cancel must leave the order placed, got %s", stored.Status)
	}

	fakeState.mu.Lock()
	fakeState.cancelErr = nil
	fakeState.mu.Unlock()
	cancelled, err := engine.CancelOrder(ctx, placed.ID)
	if err != nil || cancelled.Status != domain.OrderCancelled || cancelled.CancelledAt.IsZero() {
		t.Fatalf("cancel failed: %v %+v", err, cancelled)
	}
	if fakeState.cancelled[len(fakeState.cancelled)-1] != placed.Result.InvoiceID {
		t.Fatalf("provider must receive the order's invoice, got %v", fakeState.cancelled)
	}
	if _, err := engine.CancelOrder(ctx, placed.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cancelling twice must be rejected, got %v", err)
	}
}
