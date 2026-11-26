package provider

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/solver"
)

func TestSplitList(t *testing.T) {
	got := SplitList(" a, b\nc;d  e,,")
	if want := []string{"a", "b", "c", "d", "e"}; !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

type stubSolver struct {
	challenge solver.Challenge
	solution  solver.Solution
	err       error
}

func (s *stubSolver) Name() string              { return "stub" }
func (s *stubSolver) Supports(solver.Kind) bool { return true }
func (s *stubSolver) Solve(_ context.Context, challenge solver.Challenge) (solver.Solution, error) {
	s.challenge = challenge
	return s.solution, s.err
}

func TestEnvSolve(t *testing.T) {
	if _, err := (Env{}).Solve(context.Background(), solver.Challenge{Kind: solver.KindTurnstile}); err == nil || !strings.Contains(err.Error(), "no solver") {
		t.Fatalf("want no solver error, got %v", err)
	}
	session, _ := fetch.NewSession("https://shop.test", nil, "")
	slv := &stubSolver{solution: solver.Solution{Token: "T", Cookies: []*http.Cookie{{Name: "cf_clearance", Value: "c"}}, UserAgent: "UA-X"}}
	env := Env{Site: domain.Site{ID: "s1"}, Account: &domain.Account{ID: "a1"}, Session: session, Solver: slv}
	solution, err := env.Solve(context.Background(), solver.Challenge{Kind: solver.KindTurnstile})
	if err != nil || solution.Token != "T" {
		t.Fatalf("unexpected %v %+v", err, solution)
	}
	if slv.challenge.SiteID != "s1" || slv.challenge.AccountID != "a1" {
		t.Fatalf("site / account not filled: %+v", slv.challenge)
	}
	if session.Cookie("cf_clearance") != "c" || session.UserAgent() != "UA-X" {
		t.Fatal("solution cookies / UA must be applied to the session")
	}
	slv.err = errors.New("boom")
	if _, err := env.Solve(context.Background(), solver.Challenge{Kind: solver.KindTurnstile}); err == nil || !strings.Contains(err.Error(), "solve turnstile: ") || !errors.Is(err, slv.err) {
		t.Fatalf("want wrapped error, got %v", err)
	}
}

// proxyClient 带出口代理提示的假 Client。
type proxyClient struct{ proxy string }

func (c proxyClient) Do(context.Context, *fetch.Session, *fetch.Request) (*fetch.Response, error) {
	return nil, errors.New("unused")
}

func (c proxyClient) ProxyFor(string) string { return c.proxy }

// 站点走代理出口时，求解请求带上同一代理（token 与提交表单同一 IP）。
func TestEnvSolvePassesEgressProxy(t *testing.T) {
	slv := &stubSolver{solution: solver.Solution{Token: "T"}}
	env := Env{Site: domain.Site{ID: "s1"}, Client: proxyClient{proxy: "socks5://127.0.0.1:10808"}, Solver: slv}
	if _, err := env.Solve(context.Background(), solver.Challenge{Kind: solver.KindRecaptchaV2, PageURL: "https://shop.test/login"}); err != nil {
		t.Fatal(err)
	}
	if slv.challenge.Proxy != "socks5://127.0.0.1:10808" {
		t.Fatalf("egress proxy not passed to solver: %+v", slv.challenge)
	}
}
