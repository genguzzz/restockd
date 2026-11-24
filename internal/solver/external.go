package solver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/genguzzz/restockd/internal/fetch"
)

// ExternalSolver 调用用户自配的 HTTP 求解服务。约定：
//
//	POST <endpoint>  Authorization: Bearer <token>
//	请求 {"kind":"turnstile","site_key":"...","page_url":"...","action":"..."}
//	响应 {"token":"...","cookies":"a=1; b=2","user_agent":"..."}，失败时 {"error":"..."}
//
// 用户可以把它接到自己的服务上（自建浏览器农场、第三方打码平台的转换层等），本程序不内置任何打码实现。
type ExternalSolver struct {
	Endpoint string
	Token    string
	Kinds    []Kind
	Client   *http.Client
}

// Name 实现 Solver。
func (s *ExternalSolver) Name() string { return "external" }

// Supports 未配置 Kinds 时视为支持全部类型。
func (s *ExternalSolver) Supports(kind Kind) bool {
	return s.Endpoint != "" && (len(s.Kinds) == 0 || slices.Contains(s.Kinds, kind))
}

type externalResponse struct {
	Token     string `json:"token"`
	Cookies   string `json:"cookies"`
	UserAgent string `json:"user_agent"`
	Error     string `json:"error"`
}

// Solve 实现 Solver。
func (s *ExternalSolver) Solve(ctx context.Context, challenge Challenge) (Solution, error) {
	raw, err := json.Marshal(map[string]string{"kind": string(challenge.Kind), "site_key": challenge.SiteKey, "page_url": challenge.PageURL, "action": challenge.Action})
	if err != nil {
		return Solution{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(raw))
	if err != nil {
		return Solution{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 180 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Solution{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Solution{}, err
	}
	var parsed externalResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Solution{}, fmt.Errorf("decode: %w (http %d)", err, resp.StatusCode)
	}
	if parsed.Error != "" || resp.StatusCode >= 400 {
		return Solution{}, fmt.Errorf("http %d: %s", resp.StatusCode, parsed.Error)
	}
	solution := Solution{Token: parsed.Token, UserAgent: parsed.UserAgent, SolvedBy: s.Name()}
	if parsed.Cookies != "" {
		cookies, err := fetch.ParseCookies(parsed.Cookies)
		if err != nil {
			return Solution{}, fmt.Errorf("cookies: %w", err)
		}
		solution.Cookies = cookies
	}
	if solution.Token == "" && len(solution.Cookies) == 0 {
		return Solution{}, fmt.Errorf("empty solution")
	}
	return solution, nil
}
