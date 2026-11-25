// Package fetch 提供统一的 HTTP 抓取抽象：Client 接口、会话（Cookie 罐 + UA）、HTTPClient（直连或经代理）、
// FlareSolverrClient（真实浏览器，过 Cloudflare 质询）与 Cookie 文本解析。
//
// Provider 只依赖 Client 接口；走哪个出口、遇质询是否回落浏览器由 egress 层决定。
package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultUserAgent 未配置 UA 时使用的桌面 Chrome UA。
const DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

const maxBodyBytes = 8 << 20

// Request 一次抓取请求。
type Request struct {
	Method string
	URL    string
	Header http.Header
	// Form 非空时按 application/x-www-form-urlencoded 发送。
	Form url.Values
	// JSON 非空时按 application/json 发送（优先级低于 Form）。
	JSON []byte
	// NoRedirect 不跟随重定向，直接返回 3xx（结果页用不上时省一次往返）。
	NoRedirect bool
}

// Response 抓取响应，Body 已完整读取。
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	URL        string
}

// Client 统一抓取接口。
type Client interface {
	Do(ctx context.Context, session *Session, req *Request) (*Response, error)
}

// ChallengeError 站点返回了人机验证（Cloudflare 质询等），需要 Solver 介入。
type ChallengeError struct {
	Kind string
	URL  string
}

func (e *ChallengeError) Error() string {
	return fmt.Sprintf("challenge %s at %s", e.Kind, e.URL)
}

// RateLimitError 站点限流；RetryAfter 为 0 表示站点没说多久。
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited, retry after %s", e.RetryAfter)
}

// HTTPError 非预期状态码。
type HTTPError struct {
	StatusCode int
	URL        string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("http %d at %s", e.StatusCode, e.URL)
}

// IsChallenge 判断错误是否为人机验证。
func IsChallenge(err error) bool {
	var challengeErr *ChallengeError
	return errors.As(err, &challengeErr)
}

// Session 一个账号（或匿名）的会话：Cookie 罐与 UA。并发安全。
type Session struct {
	mu        sync.RWMutex
	jar       *cookiejar.Jar
	userAgent string
	baseURL   *url.URL
}

// NewSession 以 baseURL 为作用域创建会话并注入初始 Cookie。
func NewSession(baseURL string, cookies []*http.Cookie, userAgent string) (*Session, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	if len(cookies) > 0 {
		jar.SetCookies(parsed, normalizeCookies(cookies))
	}
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	return &Session{jar: jar, userAgent: userAgent, baseURL: parsed}, nil
}

// normalizeCookies 去掉 Domain，让 Cookie 作用于 baseURL 主机，避免粘贴来的 .domain 前缀不匹配。
func normalizeCookies(cookies []*http.Cookie) []*http.Cookie {
	out := make([]*http.Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name == "" {
			continue
		}
		copied := *cookie
		copied.Domain = ""
		if copied.Path == "" {
			copied.Path = "/"
		}
		out = append(out, &copied)
	}
	return out
}

// UserAgent 当前 UA。
func (s *Session) UserAgent() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.userAgent
}

// SetUserAgent 更新 UA（FlareSolverr 返回的 UA 必须与 cf_clearance 搭配使用）。
func (s *Session) SetUserAgent(userAgent string) {
	if userAgent == "" {
		return
	}
	s.mu.Lock()
	s.userAgent = userAgent
	s.mu.Unlock()
}

// Jar 返回 Cookie 罐。
func (s *Session) Jar() http.CookieJar {
	return s.jar
}

// BaseURL 会话作用域。
func (s *Session) BaseURL() *url.URL {
	return s.baseURL
}

// SetCookies 合并 Cookie。
func (s *Session) SetCookies(cookies []*http.Cookie) {
	s.jar.SetCookies(s.baseURL, normalizeCookies(cookies))
}

// Cookies 导出作用域内 Cookie，用于持久化回账号。
func (s *Session) Cookies() []*http.Cookie {
	return s.jar.Cookies(s.baseURL)
}

// DropCookie 删除名为 name 的 Cookie（主机 Cookie 与各级父域 Cookie 都删），返回原先是否存在。
func (s *Session) DropCookie(name string) bool {
	if s.Cookie(name) == "" {
		return false
	}
	expired := func(domain string) *http.Cookie {
		return &http.Cookie{Name: name, Path: "/", Domain: domain, MaxAge: -1}
	}
	// 服务端以 Domain=.example.com 下发的与粘贴导入的（normalizeCookies 去掉了 Domain）在罐里是不同条目。
	doomed := []*http.Cookie{expired("")}
	for host := s.baseURL.Hostname(); strings.Contains(host, "."); host = host[strings.Index(host, ".")+1:] {
		doomed = append(doomed, expired(host))
	}
	s.jar.SetCookies(s.baseURL, doomed)
	return true
}

// Cookie 按名称取值。
func (s *Session) Cookie(name string) string {
	for _, cookie := range s.Cookies() {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

// HTTPClient 基于共享 Transport 的 HTTP 客户端（直连或经单个代理），连接复用、支持 HTTP/2。
type HTTPClient struct {
	transport http.RoundTripper
	timeout   time.Duration
}

// NewHTTPClient 创建客户端；proxyURL 为空即直连（不读取环境变量代理，出口只由 egress 决定）。
func NewHTTPClient(timeout time.Duration, proxyURL string) (*HTTPClient, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 16
	transport.IdleConnTimeout = 90 * time.Second
	transport.Proxy = nil
	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy: %w", err)
		}
		transport.Proxy = http.ProxyURL(parsed)
	}
	return &HTTPClient{transport: transport, timeout: timeout}, nil
}

// Do 发送请求并按约定把质询、限流转换为类型化错误。
func (c *HTTPClient) Do(ctx context.Context, session *Session, req *Request) (*Response, error) {
	httpReq, err := buildRequest(ctx, session, req)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Transport: c.transport, Timeout: c.timeout, Jar: session.Jar()}
	if req.NoRedirect {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	resp := &Response{StatusCode: httpResp.StatusCode, Header: httpResp.Header, Body: body, URL: httpResp.Request.URL.String()}
	return resp, Classify(resp)
}

func buildRequest(ctx context.Context, session *Session, req *Request) (*http.Request, error) {
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	contentType := ""
	switch {
	case req.Form != nil:
		body = strings.NewReader(req.Form.Encode())
		contentType = "application/x-www-form-urlencoded"
	case req.JSON != nil:
		body = bytes.NewReader(req.JSON)
		contentType = "application/json"
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("User-Agent", session.UserAgent())
	httpReq.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	for key, values := range req.Header {
		httpReq.Header.Del(key)
		for _, value := range values {
			httpReq.Header.Add(key, value)
		}
	}
	if httpReq.Header.Get("Accept") == "" {
		httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/json;q=0.9,*/*;q=0.8")
	}
	return httpReq, nil
}

// Classify 把响应映射为错误：Cloudflare 质询、429 限流；其余由调用方判断。
func Classify(resp *Response) error {
	if resp.StatusCode == http.StatusTooManyRequests {
		return &RateLimitError{RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	}
	if IsCloudflareChallenge(resp) {
		return &ChallengeError{Kind: "cloudflare", URL: resp.URL}
	}
	return nil
}

// IsCloudflareChallenge 识别 Cloudflare 托管质询页。
func IsCloudflareChallenge(resp *Response) bool {
	if resp.Header.Get("Cf-Mitigated") == "challenge" {
		return true
	}
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusServiceUnavailable {
		return false
	}
	head := resp.Body
	if len(head) > 16<<10 {
		head = head[:16<<10]
	}
	return bytes.Contains(head, []byte("challenge-platform")) || bytes.Contains(head, []byte("<title>Just a moment..."))
}

// parseRetryAfter 缺失或无法解析时返回 0，由调用方选默认冷却。
func parseRetryAfter(value string) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if wait := time.Until(at); wait > 0 {
			return wait
		}
	}
	return 0
}
