package solver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeSolver struct {
	name  string
	kinds []Kind
	token string
	err   error
	calls int
}

func (f *fakeSolver) Name() string { return f.name }
func (f *fakeSolver) Supports(kind Kind) bool {
	for _, k := range f.kinds {
		if k == kind {
			return true
		}
	}
	return false
}
func (f *fakeSolver) Solve(context.Context, Challenge) (Solution, error) {
	f.calls++
	return Solution{Token: f.token}, f.err
}

func TestChainSkipsUnsupportedAndFallsThrough(t *testing.T) {
	cfOnly := &fakeSolver{name: "cf", kinds: []Kind{KindCloudflare}, token: "nope"}
	failing := &fakeSolver{name: "ext", kinds: []Kind{KindTurnstile}, err: errors.New("boom")}
	manual := &fakeSolver{name: "manual", kinds: []Kind{KindTurnstile, KindCloudflare}, token: "tok"}
	chain := Chain{cfOnly, failing, manual}
	solution, err := chain.Solve(context.Background(), Challenge{Kind: KindTurnstile})
	if err != nil {
		t.Fatal(err)
	}
	if solution.Token != "tok" || solution.SolvedBy != "manual" {
		t.Fatalf("unexpected solution %+v", solution)
	}
	if cfOnly.calls != 0 || failing.calls != 1 {
		t.Fatalf("calls cf=%d ext=%d", cfOnly.calls, failing.calls)
	}
}

func TestChainAllFailJoinsErrors(t *testing.T) {
	chain := Chain{&fakeSolver{name: "a", kinds: []Kind{KindTurnstile}, err: errors.New("a failed")}}
	if _, err := chain.Solve(context.Background(), Challenge{Kind: KindTurnstile}); err == nil {
		t.Fatal("want error")
	}
	if _, err := chain.Solve(context.Background(), Challenge{Kind: KindHCaptcha}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("want ErrUnsupported, got %v", err)
	}
}

func TestManualBrokerSubmit(t *testing.T) {
	broker := NewManualBroker(time.Minute)
	created := make(chan Pending, 1)
	done := make(chan string, 1)
	broker.OnNew = func(p Pending) { created <- p }
	broker.OnDone = func(id string) { done <- id }
	result := make(chan Solution, 1)
	go func() {
		solution, err := broker.Solve(context.Background(), Challenge{Kind: KindTurnstile, Purpose: "test"})
		if err != nil {
			t.Error(err)
		}
		result <- solution
	}()
	pending := <-created
	if len(broker.List()) != 1 {
		t.Fatal("pending not listed")
	}
	if err := broker.Submit(pending.ID, Solution{Token: "abc"}); err != nil {
		t.Fatal(err)
	}
	solution := <-result
	if solution.Token != "abc" || solution.SolvedBy != "manual" {
		t.Fatalf("unexpected %+v", solution)
	}
	if <-done != pending.ID || len(broker.List()) != 0 {
		t.Fatal("challenge not removed")
	}
	if err := broker.Submit(pending.ID, Solution{}); !errors.Is(err, ErrChallengeNotFound) {
		t.Fatalf("second submit should fail, got %v", err)
	}
}

func TestManualBrokerCancelAndTimeout(t *testing.T) {
	broker := NewManualBroker(time.Minute)
	created := make(chan Pending, 1)
	broker.OnNew = func(p Pending) { created <- p }
	errCh := make(chan error, 1)
	go func() {
		_, err := broker.Solve(context.Background(), Challenge{Kind: KindCloudflare})
		errCh <- err
	}()
	if err := broker.Cancel((<-created).ID); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; !errors.Is(err, ErrChallengeCancelled) {
		t.Fatalf("want cancelled, got %v", err)
	}
	broker.SetTimeout(20 * time.Millisecond)
	broker.OnNew = nil
	if _, err := broker.Solve(context.Background(), Challenge{Kind: KindCloudflare}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want timeout, got %v", err)
	}
}

func TestExternalSolverContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"bad token"}`))
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["kind"] != "turnstile" || body["site_key"] != "0xKEY" || body["page_url"] != "https://usvps24.com/cart" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"bad payload"}`))
			return
		}
		_, _ = w.Write([]byte(`{"token":"T0K","cookies":"a=1","user_agent":"UA"}`))
	}))
	defer server.Close()
	ext := &ExternalSolver{Endpoint: server.URL, Token: "secret"}
	if ext.Supports(KindCloudflare) || !ext.Supports(KindTurnstile) {
		t.Fatal("default kinds wrong")
	}
	solution, err := ext.Solve(context.Background(), Challenge{Kind: KindTurnstile, SiteKey: "0xKEY", PageURL: "https://usvps24.com/cart"})
	if err != nil {
		t.Fatal(err)
	}
	if solution.Token != "T0K" || solution.UserAgent != "UA" || len(solution.Cookies) != 1 {
		t.Fatalf("unexpected %+v", solution)
	}
	ext.Token = "wrong"
	if _, err := ext.Solve(context.Background(), Challenge{Kind: KindTurnstile}); err == nil {
		t.Fatal("want auth error")
	}
}
