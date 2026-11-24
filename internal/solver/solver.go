// Package solver 定义人机验证的统一求解接口。
//
// Provider 在下单、登录等环节遇到验证码时构造 Challenge 交给 Solver（经 provider.Env.Solve）；Solver 可以是
// 外部服务（用户自配的 HTTP 接口），也可以是 ManualBroker——把挑战挂到界面上，由用户手动粘贴 token 或 Cookie。
// Cloudflare 托管质询不走这里，由出口层的浏览器回落（FlareSolverr）处理。
// Chain 按顺序尝试多个 Solver，因此"自动优先、手动兜底"只是一种组合方式。
package solver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Kind 验证类型。
type Kind string

const (
	KindTurnstile   Kind = "turnstile"
	KindRecaptchaV2 Kind = "recaptcha_v2"
	KindRecaptchaV3 Kind = "recaptcha_v3"
	KindHCaptcha    Kind = "hcaptcha"
)

// ErrUnsupported Solver 不支持该类型。
var ErrUnsupported = errors.New("solver: kind not supported")

// Challenge 一次待求解的验证。
type Challenge struct {
	Kind      Kind   `json:"kind"`
	SiteKey   string `json:"site_key,omitempty"`
	PageURL   string `json:"page_url"`
	Action    string `json:"action,omitempty"`
	SiteID    string `json:"site_id"`
	AccountID string `json:"account_id,omitempty"`
	// Purpose 给人看的用途说明，如"usvps24 下单 pinnacle-network-2"。
	Purpose string `json:"purpose"`
}

// Solution 求解结果：Token 填入表单；附带的 Cookies 与 UserAgent（人工或外部服务给出）由 provider.Env.Solve 写回会话。
type Solution struct {
	Token     string         `json:"token,omitempty"`
	Cookies   []*http.Cookie `json:"-"`
	UserAgent string         `json:"user_agent,omitempty"`
	SolvedBy  string         `json:"solved_by"`
}

// Solver 统一求解接口。
type Solver interface {
	Name() string
	Supports(kind Kind) bool
	Solve(ctx context.Context, challenge Challenge) (Solution, error)
}

// Chain 依次尝试，返回第一个成功结果；全部失败时汇总错误。
type Chain []Solver

// Name 实现 Solver。
func (c Chain) Name() string {
	names := make([]string, 0, len(c))
	for _, item := range c {
		names = append(names, item.Name())
	}
	return "chain(" + strings.Join(names, ",") + ")"
}

// Supports 任一成员支持即可。
func (c Chain) Supports(kind Kind) bool {
	for _, item := range c {
		if item.Supports(kind) {
			return true
		}
	}
	return false
}

// Solve 实现 Solver。
func (c Chain) Solve(ctx context.Context, challenge Challenge) (Solution, error) {
	var errs []error
	for _, item := range c {
		if !item.Supports(challenge.Kind) {
			continue
		}
		solution, err := item.Solve(ctx, challenge)
		if err == nil {
			if solution.SolvedBy == "" {
				solution.SolvedBy = item.Name()
			}
			return solution, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", item.Name(), err))
		if ctx.Err() != nil {
			break
		}
	}
	if len(errs) == 0 {
		return Solution{}, fmt.Errorf("%w: %s", ErrUnsupported, challenge.Kind)
	}
	return Solution{}, errors.Join(errs...)
}
