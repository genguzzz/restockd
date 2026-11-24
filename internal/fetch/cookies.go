package fetch

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrNoCookies 粘贴内容中没有解析出任何 Cookie。
var ErrNoCookies = errors.New("no cookies found")

// ParseCookies 解析手动粘贴的 Cookie，支持三种常见格式：
//   - 请求头格式：`a=1; b=2`，可带 `Cookie:` 前缀
//   - JSON 数组：Cookie-Editor / EditThisCookie 导出的 [{name,value,domain,path,expirationDate,...}]
//   - Netscape cookies.txt：制表符分隔七列
func ParseCookies(raw string) ([]*http.Cookie, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil, ErrNoCookies
	}
	var cookies []*http.Cookie
	switch {
	case strings.HasPrefix(text, "[") || strings.HasPrefix(text, "{"):
		parsed, err := parseJSONCookies(text)
		if err != nil {
			return nil, err
		}
		cookies = parsed
	case strings.Contains(text, "\t"):
		cookies = parseNetscapeCookies(text)
	default:
		cookies = parseHeaderCookies(text)
	}
	if len(cookies) == 0 {
		return nil, ErrNoCookies
	}
	return dedupeCookies(cookies), nil
}

type jsonCookie struct {
	Name           string  `json:"name"`
	Value          string  `json:"value"`
	Domain         string  `json:"domain"`
	Path           string  `json:"path"`
	ExpirationDate float64 `json:"expirationDate"`
	Expires        any     `json:"expires"`
	HTTPOnly       bool    `json:"httpOnly"`
	Secure         bool    `json:"secure"`
}

func parseJSONCookies(text string) ([]*http.Cookie, error) {
	var items []jsonCookie
	if strings.HasPrefix(text, "{") {
		var single jsonCookie
		if err := json.Unmarshal([]byte(text), &single); err != nil {
			return nil, err
		}
		items = []jsonCookie{single}
	} else if err := json.Unmarshal([]byte(text), &items); err != nil {
		return nil, err
	}
	out := make([]*http.Cookie, 0, len(items))
	for _, item := range items {
		if item.Name == "" {
			continue
		}
		cookie := &http.Cookie{Name: item.Name, Value: item.Value, Domain: item.Domain, Path: item.Path, HttpOnly: item.HTTPOnly, Secure: item.Secure}
		if item.ExpirationDate > 0 {
			cookie.Expires = time.Unix(int64(item.ExpirationDate), 0)
		}
		out = append(out, cookie)
	}
	return out, nil
}

func parseNetscapeCookies(text string) []*http.Cookie {
	var out []*http.Cookie
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || (strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#HttpOnly_")) {
			continue
		}
		httpOnly := strings.HasPrefix(line, "#HttpOnly_")
		line = strings.TrimPrefix(line, "#HttpOnly_")
		fields := strings.Split(line, "\t")
		if len(fields) < 7 {
			continue
		}
		cookie := &http.Cookie{Domain: fields[0], Path: fields[2], Secure: strings.EqualFold(fields[3], "TRUE"), Name: fields[5], Value: fields[6], HttpOnly: httpOnly}
		if expires, err := strconv.ParseInt(fields[4], 10, 64); err == nil && expires > 0 {
			cookie.Expires = time.Unix(expires, 0)
		}
		out = append(out, cookie)
	}
	return out
}

func parseHeaderCookies(text string) []*http.Cookie {
	if idx := strings.Index(strings.ToLower(text), "cookie:"); idx == 0 {
		text = text[len("cookie:"):]
	}
	var out []*http.Cookie
	for _, part := range strings.Split(text, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}
		out = append(out, &http.Cookie{Name: name, Value: strings.TrimSpace(value), Path: "/"})
	}
	return out
}

func dedupeCookies(cookies []*http.Cookie) []*http.Cookie {
	index := map[string]int{}
	out := make([]*http.Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		if pos, ok := index[cookie.Name]; ok {
			out[pos] = cookie
			continue
		}
		index[cookie.Name] = len(out)
		out = append(out, cookie)
	}
	return out
}

// CookieHeader 序列化为请求头格式，便于存储与展示。
func CookieHeader(cookies []*http.Cookie) string {
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}
