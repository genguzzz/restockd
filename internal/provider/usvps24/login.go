package usvps24

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/solver"
)

var _ provider.Authenticator = (*Provider)(nil)

// loginProps /login 页面（Inertia 组件 Auth/Login）里登录用到的字段。
type loginProps struct {
	TurnstileSiteKey string               `json:"turnstileSiteKey"`
	Errors           map[string]any       `json:"errors"`
	Auth             struct{ User *user } `json:"auth"`
}

type user struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// Login 实现 provider.Authenticator：与前端一致 POST /login {email,password,remember,cf_turnstile_response}。
// 只访问登录页（站点说明登录不受库存页频率限制），不碰下单页。
func (p *Provider) Login(ctx context.Context, env provider.Env, creds domain.Credentials) (provider.SessionInfo, error) {
	page, err := p.getPage(ctx, env, "/login")
	if err != nil {
		return provider.SessionInfo{}, fmt.Errorf("login page: %w", err)
	}
	var props loginProps
	if err := json.Unmarshal(page.Props, &props); err != nil {
		return provider.SessionInfo{}, fmt.Errorf("decode login props: %w", err)
	}
	if props.Auth.User != nil && page.Component != "Auth/Login" {
		return sessionInfo(props.Auth.User), nil // 已登录（被重定向离开登录页）
	}
	payload := map[string]any{"email": creds.Username, "password": creds.Password, "remember": true, "cf_turnstile_response": ""}
	if props.TurnstileSiteKey != "" {
		p.mu.Lock()
		p.turnstileKey = props.TurnstileSiteKey
		p.mu.Unlock()
		solution, err := env.Solve(ctx, solver.Challenge{Kind: solver.KindTurnstile, SiteKey: props.TurnstileSiteKey, PageURL: p.baseURL + "/login", Purpose: env.Site.Name + " 登录"})
		if err != nil {
			return provider.SessionInfo{}, err
		}
		payload["cf_turnstile_response"] = solution.Token
	}
	resp, err := p.postJSON(ctx, env, "/login", payload, page.Version)
	if err != nil {
		return provider.SessionInfo{}, err
	}
	return parseLoginResult(resp)
}

// parseLoginResult 成功时 302 到控制台（Inertia 跟随后为其它组件且带 auth.user）；失败时回到 Auth/Login 并带 errors。
// 429 已由抓取层转成 RateLimitError，不会到这里。
func parseLoginResult(resp *fetch.Response) (provider.SessionInfo, error) {
	if resp.StatusCode >= http.StatusBadRequest {
		return provider.SessionInfo{}, fmt.Errorf("login http %d", resp.StatusCode)
	}
	page, err := ParsePage(resp.Body)
	if err != nil {
		return provider.SessionInfo{}, fmt.Errorf("login response: %w", err)
	}
	var props loginProps
	if err := json.Unmarshal(page.Props, &props); err != nil {
		return provider.SessionInfo{}, fmt.Errorf("decode login response: %w", err)
	}
	if message := first(laravelErrors(props.Errors)); message != "" {
		return provider.SessionInfo{}, fmt.Errorf("%w: %s", provider.ErrLoginFailed, message)
	}
	if page.Component == "Auth/Login" || props.Auth.User == nil {
		return provider.SessionInfo{}, fmt.Errorf("%w: still on login page", provider.ErrLoginFailed)
	}
	return sessionInfo(props.Auth.User), nil
}

func sessionInfo(u *user) provider.SessionInfo {
	return provider.SessionInfo{Identity: cmp.Or(u.Email, u.Name)}
}
