package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// FlareSolverrClient 通过 FlareSolverr（真实浏览器）发请求，用于 Cloudflare 托管质询站点。
//
// 每次请求把会话 Cookie 带给浏览器，并把浏览器拿到的 Cookie（含 cf_clearance）与 UA 写回会话，
// 之后普通 HTTP 客户端即可复用 cf_clearance。cf_clearance 绑定出口 IP，因此浏览器必须走与原请求
// 相同的代理（见 egress.Client.Browser）。
type FlareSolverrClient struct {
	endpoint   string
	httpClient *http.Client
	maxTimeout time.Duration
}

// NewFlareSolverrClient endpoint 形如 http://127.0.0.1:8191。
func NewFlareSolverrClient(endpoint string, maxTimeout time.Duration) *FlareSolverrClient {
	return &FlareSolverrClient{
		endpoint:   strings.TrimRight(endpoint, "/") + "/v1",
		httpClient: &http.Client{Timeout: maxTimeout + 15*time.Second},
		maxTimeout: maxTimeout,
	}
}

type flareCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain,omitempty"`
	Path     string  `json:"path,omitempty"`
	Expires  float64 `json:"expires,omitempty"`
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
}

type flareRequest struct {
	Cmd        string        `json:"cmd"`
	URL        string        `json:"url"`
	MaxTimeout int64         `json:"maxTimeout"`
	Cookies    []flareCookie `json:"cookies,omitempty"`
	PostData   string        `json:"postData,omitempty"`
	Proxy      *flareProxy   `json:"proxy,omitempty"`
}

// flareProxy 浏览器不接受 URL 里的账号密码，需拆成单独字段。
type flareProxy struct {
	URL      string `json:"url"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

func toFlareProxy(proxyURL string) (*flareProxy, error) {
	if proxyURL == "" {
		return nil, nil
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy: %w", err)
	}
	proxy := &flareProxy{}
	if parsed.User != nil {
		proxy.Username = parsed.User.Username()
		proxy.Password, _ = parsed.User.Password()
		parsed.User = nil
	}
	proxy.URL = parsed.String()
	return proxy, nil
}

type flareResponse struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Solution struct {
		URL       string            `json:"url"`
		Status    int               `json:"status"`
		Headers   map[string]string `json:"headers"`
		Response  string            `json:"response"`
		Cookies   []flareCookie     `json:"cookies"`
		UserAgent string            `json:"userAgent"`
	} `json:"solution"`
}

// Do 经 proxyURL（空为直连）用浏览器发请求。浏览器只能发 GET 与表单 POST，且不带自定义请求头：
// JSON 请求返回错误，调用方应先用 GET 过质询、再用普通客户端重发（见 egress.Client）。
// 连不上 FlareSolverr 等错误不包装原始网络错误，避免被出口层误判为出口故障。
func (c *FlareSolverrClient) Do(ctx context.Context, session *Session, req *Request, proxyURL string) (*Response, error) {
	if req.JSON != nil {
		return nil, fmt.Errorf("flaresolverr: json body not supported")
	}
	payload := flareRequest{Cmd: "request.get", URL: req.URL, MaxTimeout: c.maxTimeout.Milliseconds()}
	if req.Method == http.MethodPost {
		payload.Cmd = "request.post"
		payload.PostData = req.Form.Encode()
	}
	proxy, err := toFlareProxy(proxyURL)
	if err != nil {
		return nil, err
	}
	payload.Proxy = proxy
	for _, cookie := range session.Cookies() {
		payload.Cookies = append(payload.Cookies, flareCookie{Name: cookie.Name, Value: cookie.Value})
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("flaresolverr: %v", err)
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("flaresolverr: read: %v", err)
	}
	var parsed flareResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("flaresolverr: decode: %w", err)
	}
	if parsed.Status != "ok" {
		return nil, fmt.Errorf("flaresolverr: %s", parsed.Message)
	}
	session.SetUserAgent(parsed.Solution.UserAgent)
	session.SetCookies(toHTTPCookies(parsed.Solution.Cookies))
	header := http.Header{}
	for key, value := range parsed.Solution.Headers {
		header.Set(key, value)
	}
	status := parsed.Solution.Status
	if status == 0 {
		status = http.StatusOK
	}
	resp := &Response{StatusCode: status, Header: header, Body: []byte(parsed.Solution.Response), URL: parsed.Solution.URL}
	return resp, Classify(resp)
}

func toHTTPCookies(cookies []flareCookie) []*http.Cookie {
	out := make([]*http.Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		item := &http.Cookie{Name: cookie.Name, Value: cookie.Value, Path: cookie.Path, HttpOnly: cookie.HTTPOnly, Secure: cookie.Secure}
		if cookie.Expires > 0 {
			item.Expires = time.Unix(int64(cookie.Expires), 0)
		}
		out = append(out, item)
	}
	return out
}
