package provider

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/genguzzz/restockd/internal/solver"
)

// countingSolver 记录求解次数；fail 为真时预取（第一次）失败。
type countingSolver struct {
	mu    sync.Mutex
	calls []solver.Challenge
	fail  bool
}

func (s *countingSolver) Name() string              { return "count" }
func (s *countingSolver) Supports(solver.Kind) bool { return true }
func (s *countingSolver) Solve(_ context.Context, c solver.Challenge) (solver.Solution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, c)
	if s.fail && len(s.calls) == 1 {
		return solver.Solution{}, errors.New("boom")
	}
	return solver.Solution{Token: c.Action + "-" + c.SiteKey}, nil
}

func TestPrefetch(t *testing.T) {
	ctx := context.Background()
	want := solver.Challenge{Kind: solver.KindTurnstile, SiteKey: "k", Action: "checkoutCompletion", PageURL: "https://shop.test/cart.php?a=checkout"}

	used := &countingSolver{}
	env := Env{Solver: used}
	pre := env.Prefetch(ctx, want)
	defer pre.Stop()
	solution, err := pre.Solve(ctx, env, want)
	if err != nil || solution.Token != "checkoutCompletion-k" || len(used.calls) != 1 {
		t.Fatalf("matching prefetch must be reused: %v %+v calls=%d", err, solution, len(used.calls))
	}

	other := want
	other.Action = "login"
	mismatch := &countingSolver{}
	env = Env{Solver: mismatch}
	pre2 := env.Prefetch(ctx, want)
	defer pre2.Stop()
	if solution, _ := pre2.Solve(ctx, env, other); solution.Token != "login-k" {
		t.Fatalf("a different action must be solved fresh, got %+v", solution)
	}

	failing := &countingSolver{fail: true}
	env = Env{Solver: failing}
	pre3 := env.Prefetch(ctx, want)
	defer pre3.Stop()
	if solution, err := pre3.Solve(ctx, env, want); err != nil || solution.Token == "" || len(failing.calls) != 2 {
		t.Fatalf("failed prefetch must fall back to a fresh solve: %v %+v calls=%d", err, solution, len(failing.calls))
	}

	var none *Prefetch
	if solution, err := none.Solve(ctx, Env{Solver: &countingSolver{}}, want); err != nil || solution.Token == "" || none.Elapsed() != 0 {
		t.Fatal("nil prefetch must just solve")
	}
	none.Stop()
}
