package usvps24

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

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
	resp, err := env.Client.Do(ctx, env.Session, &fetch.Request{Method: http.MethodGet, URL: p.baseURL + "/login"})
	if err != nil {
		return provider.SessionInfo{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return provider.SessionInfo{}, &fetch.HTTPError{StatusCode: resp.StatusCode, URL: resp.URL}
	}
	page, err := ParsePage(resp.Body)
	if err != nil {
		return provider.SessionInfo{}, fmt.Errorf("login page: %w", err)
	}
	p.setVersion(page.Version)
	var props loginProps
	if err := json.Unmarshal(page.Props, &props); err != nil {
		return provider.SessionInfo{}, fmt.Errorf("decode login props: %w", err)
	}
	if props.Auth.User != nil && page.Component != "Auth/Login" {
		return sessionInfo(props.Auth.User), nil // 已登录（被重定向离开登录页）
	}
	payload := map[string]any{"email": creds.Username, "password": creds.Password, "remember": true, "cf_turnstile_response": ""}
	if props.TurnstileSiteKey != "" {
		if env.Solver == nil {
			return provider.SessionInfo{}, fmt.Errorf("login requires turnstile but no solver configured")
		}
		solution, err := env.Solver.Solve(ctx, solver.Challenge{Kind: solver.KindTurnstile, SiteKey: props.TurnstileSiteKey, PageURL: p.baseURL + "/login", SiteID: env.Site.ID, AccountID: accountID(env), Purpose: env.Site.Name + " 登录"})
		if err != nil {
			return provider.SessionInfo{}, fmt.Errorf("solve turnstile: %w", err)
		}
		payload["cf_turnstile_response"] = solution.Token
	}
	resp, err = p.postJSON(ctx, env, "/login", payload, true)
	if err != nil {
		return provider.SessionInfo{}, err
	}
	return parseLoginResult(resp)
}

// parseLoginResult 成功时 302 到控制台（Inertia 跟随后为其它组件且带 auth.user）；失败时回到 Auth/Login 并带 errors。
func parseLoginResult(resp *fetch.Response) (provider.SessionInfo, error) {
	if resp.StatusCode == http.StatusTooManyRequests {
		return provider.SessionInfo{}, fmt.Errorf("login rate limited (http 429)")
	}
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
	if message := firstMessage(props.Errors); message != "" {
		return provider.SessionInfo{}, fmt.Errorf("%w: %s", provider.ErrLoginFailed, message)
	}
	if page.Component == "Auth/Login" || props.Auth.User == nil {
		return provider.SessionInfo{}, fmt.Errorf("%w: still on login page", provider.ErrLoginFailed)
	}
	return sessionInfo(props.Auth.User), nil
}

func sessionInfo(u *user) provider.SessionInfo {
	return provider.SessionInfo{Identity: firstNonEmpty(u.Email, u.Name)}
}

// firstMessage Laravel 校验错误：{"email": "这些凭据与我们的记录不符"} 或数组形式，按键排序取第一条。
func firstMessage(errs map[string]any) string {
	keys := make([]string, 0, len(errs))
	for key := range errs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		switch value := errs[key].(type) {
		case string:
			if value != "" {
				return value
			}
		case []any:
			if len(value) > 0 {
				return fmt.Sprint(value[0])
			}
		}
	}
	return ""
}
