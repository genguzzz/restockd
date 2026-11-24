package usvps24

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
)

func TestCancelInvoice(t *testing.T) {
	status := "unpaid"
	var cancelCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "XSRF-TOKEN", Value: "x%3D", Path: "/"})
		switch {
		case r.URL.Path == "/invoices/777" && r.Method == http.MethodGet:
			page, _ := json.Marshal(map[string]any{"component": "Invoices/Show", "url": "/invoices/777", "version": "v9", "props": map[string]any{"invoice": map[string]any{"status": status}}})
			fmt.Fprintf(w, `<div id="app" data-page="%s"></div>`, html.EscapeString(string(page)))
		case r.URL.Path == "/invoices/777/cancel" && r.Method == http.MethodPost:
			cancelCalls++
			if r.Header.Get("X-Inertia-Version") != "v9" || r.Header.Get("X-XSRF-TOKEN") != "x=" {
				w.WriteHeader(419)
				return
			}
			status = "cancelled"
			http.Redirect(w, r, "/invoices/777", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	instance, _ := New(domain.Site{ID: "usvps24", BaseURL: server.URL})
	client, _ := fetch.NewHTTPClient(5*time.Second, "")
	session, _ := fetch.NewSession(server.URL, nil, "")
	env := provider.Env{Client: client, Session: session}
	p := instance.(*Provider)
	if err := p.CancelOrder(context.Background(), env, domain.OrderResult{PayURL: server.URL + "/invoices/777"}); err != nil {
		t.Fatal(err)
	}
	if cancelCalls != 1 || status != "cancelled" {
		t.Fatalf("want one cancel request, got %d (status %s)", cancelCalls, status)
	}
	// 已作废的账单再取消：不再提交。
	if err := p.CancelOrder(context.Background(), env, domain.OrderResult{InvoiceID: "777"}); err != nil || cancelCalls != 1 {
		t.Fatalf("already cancelled invoice must be a no-op: %v calls=%d", err, cancelCalls)
	}
	if err := p.CancelOrder(context.Background(), env, domain.OrderResult{InvoiceID: "404"}); err == nil || errors.Is(err, provider.ErrNotSupported) {
		t.Fatalf("missing invoice must be a plain error, got %v", err)
	}
}
