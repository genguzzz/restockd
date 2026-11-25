package provider

import (
	"context"
	"net/url"
	"time"

	"github.com/genguzzz/restockd/internal/solver"
)

// Prefetch 提前在后台求解验证码，与下单的其它步骤（加购、配置、打开结算页，常需十几秒）并行；
// 到提交时若页面要求的验证码与预取的一致就直接用，否则照常现解。
type Prefetch struct {
	challenge solver.Challenge
	done      chan struct{}
	solution  solver.Solution
	err       error
	elapsed   time.Duration
	cancel    context.CancelFunc
}

// Prefetch 启动后台求解；调用方用完须 Stop（未取用时取消，避免占着求解服务）。
func (e Env) Prefetch(ctx context.Context, challenge solver.Challenge) *Prefetch {
	ctx, cancel := context.WithCancel(ctx)
	p := &Prefetch{challenge: challenge, done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(p.done)
		started := time.Now()
		p.solution, p.err = e.Solve(ctx, challenge)
		p.elapsed = time.Since(started)
	}()
	return p
}

// Matches 预取的验证码能否用于 challenge：类型、sitekey、action 与页面主机（token 绑定 hostname）都相同。
func (p *Prefetch) Matches(challenge solver.Challenge) bool {
	if p == nil {
		return false
	}
	return p.challenge.Kind == challenge.Kind && p.challenge.SiteKey == challenge.SiteKey &&
		p.challenge.Action == challenge.Action && host(p.challenge.PageURL) == host(challenge.PageURL)
}

// Solve 优先使用匹配的预取结果；预取失败或不匹配时现解。
func (p *Prefetch) Solve(ctx context.Context, env Env, challenge solver.Challenge) (solver.Solution, error) {
	if p.Matches(challenge) {
		select {
		case <-p.done:
			if p.err == nil {
				return p.solution, nil
			}
		case <-ctx.Done():
			return solver.Solution{}, ctx.Err()
		}
	}
	return env.Solve(ctx, challenge)
}

// Elapsed 预取求解耗时（未完成为 0），用于耗时分解。
func (p *Prefetch) Elapsed() time.Duration {
	if p == nil {
		return 0
	}
	select {
	case <-p.done:
		return p.elapsed
	default:
		return 0
	}
}

// Stop 取消尚未完成的预取。
func (p *Prefetch) Stop() {
	if p != nil {
		p.cancel()
	}
}

func host(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return parsed.Host
}
