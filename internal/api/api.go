// Package api 提供 docs/api.md 描述的 JSON 接口、SSE 推送与内嵌前端静态资源。
package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/genguzzz/restockd/internal/engine"
	"github.com/genguzzz/restockd/internal/store"
)

// Version 服务版本。
const Version = "0.1.0"

const (
	sessionCookie = "restockd_session"
	sessionTTL    = 30 * 24 * time.Hour
	maxBodyBytes  = 1 << 20
)

// Server HTTP 处理器。
type Server struct {
	engine   *engine.Engine
	store    *store.Store
	log      *slog.Logger
	password string
	secret   []byte
	static   fs.FS
	limiter  *loginLimiter
	mux      *http.ServeMux
}

// Options 构造参数。
type Options struct {
	Engine   *engine.Engine
	Store    *store.Store
	Log      *slog.Logger
	Password string
	// Static 前端构建产物（dist 目录）；为 nil 时只提供 API。
	Static fs.FS
}

// New 构造服务并注册路由。会话签名密钥持久化在 settings 表，重启后登录态不失效。
func New(ctx context.Context, options Options) (*Server, error) {
	if options.Password == "" {
		return nil, errors.New("admin password is required")
	}
	secret, err := loadSecret(ctx, options.Store)
	if err != nil {
		return nil, err
	}
	server := &Server{engine: options.Engine, store: options.Store, log: options.Log, password: options.Password, secret: secret, static: options.Static, limiter: newLoginLimiter(), mux: http.NewServeMux()}
	server.routes()
	return server, nil
}

func loadSecret(ctx context.Context, st *store.Store) ([]byte, error) {
	var encoded string
	err := st.GetSetting(ctx, "auth_secret", &encoded)
	if err == nil {
		return hex.DecodeString(encoded)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return secret, st.PutSetting(ctx, "auth_secret", hex.EncodeToString(secret))
}

// ServeHTTP 实现 http.Handler。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/me", s.auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": Version})
	}))
	s.mux.HandleFunc("GET /api/providers", s.auth(s.handleProviders))
	s.mux.HandleFunc("GET /api/sites", s.auth(s.handleListSites))
	s.mux.HandleFunc("POST /api/sites", s.auth(s.handleSaveSite))
	s.mux.HandleFunc("PUT /api/sites/{id}", s.auth(s.handleSaveSite))
	s.mux.HandleFunc("DELETE /api/sites/{id}", s.auth(s.handleDeleteSite))
	s.mux.HandleFunc("POST /api/sites/{id}/poll", s.auth(s.handlePollSite))
	s.mux.HandleFunc("GET /api/sites/{id}/products", s.auth(s.handleListProducts))
	s.mux.HandleFunc("GET /api/products", s.auth(s.handleListProducts))
	s.mux.HandleFunc("GET /api/accounts", s.auth(s.handleListAccounts))
	s.mux.HandleFunc("POST /api/accounts", s.auth(s.handleSaveAccount))
	s.mux.HandleFunc("PUT /api/accounts/{id}", s.auth(s.handleSaveAccount))
	s.mux.HandleFunc("DELETE /api/accounts/{id}", s.auth(s.handleDeleteAccount))
	s.mux.HandleFunc("POST /api/accounts/{id}/cookies", s.auth(s.handleSetCookies))
	s.mux.HandleFunc("POST /api/accounts/{id}/check", s.auth(s.handleCheckAccount))
	s.mux.HandleFunc("POST /api/accounts/{id}/login", s.auth(s.handleLoginAccount))
	s.mux.HandleFunc("GET /api/watches", s.auth(s.handleListWatches))
	s.mux.HandleFunc("POST /api/watches", s.auth(s.handleSaveWatch))
	s.mux.HandleFunc("PUT /api/watches/{id}", s.auth(s.handleSaveWatch))
	s.mux.HandleFunc("DELETE /api/watches/{id}", s.auth(s.handleDeleteWatch))
	s.mux.HandleFunc("POST /api/watches/{id}/quote", s.auth(s.handleQuote))
	s.mux.HandleFunc("POST /api/watches/{id}/order", s.auth(s.handleOrder))
	s.mux.HandleFunc("GET /api/orders", s.auth(s.handleListOrders))
	s.mux.HandleFunc("GET /api/events", s.auth(s.handleListEvents))
	s.mux.HandleFunc("GET /api/stream", s.auth(s.handleStream))
	s.mux.HandleFunc("GET /api/challenges", s.auth(s.handleListChallenges))
	s.mux.HandleFunc("POST /api/challenges/{id}/solve", s.auth(s.handleSolveChallenge))
	s.mux.HandleFunc("POST /api/challenges/{id}/cancel", s.auth(s.handleCancelChallenge))
	s.mux.HandleFunc("GET /api/settings", s.auth(s.handleGetSettings))
	s.mux.HandleFunc("PUT /api/settings", s.auth(s.handlePutSettings))
	s.mux.HandleFunc("POST /api/settings/test-notify", s.auth(s.handleTestNotify))
	s.mux.HandleFunc("GET /api/stats", s.auth(s.handleStats))
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeError(w, http.StatusNotFound, "not found") })
	s.mux.HandleFunc("/", s.handleStatic)
}

// ---- auth ----

func (s *Server) sign(expires int64) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(strconv.FormatInt(expires, 10)))
	return strconv.FormatInt(expires, 10) + "." + hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) validSession(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	expiresText, _, ok := strings.Cut(cookie.Value, ".")
	if !ok {
		return false
	}
	expires, err := strconv.ParseInt(expiresText, 10, 64)
	if err != nil || time.Now().Unix() > expires {
		return false
	}
	return hmac.Equal([]byte(cookie.Value), []byte(s.sign(expires)))
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.validSession(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		s.engine.TouchViewer() // 有人在用管理界面：无关注站点提速并立即刷新
		next(w, r)
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.allow(ip) {
		writeError(w, http.StatusTooManyRequests, "too many attempts, try later")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(s.password)) != 1 {
		s.limiter.fail(ip)
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	s.limiter.reset(ip)
	expires := time.Now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: s.sign(expires.Unix()), Path: "/", Expires: expires, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// loginLimiter 每 IP 15 分钟内最多 8 次失败。
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{failures: map[string][]time.Time{}}
}

const (
	loginWindow      = 15 * time.Minute
	loginMaxFailures = 8
)

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.failures[ip][:0]
	for _, at := range l.failures[ip] {
		if time.Since(at) < loginWindow {
			recent = append(recent, at)
		}
	}
	l.failures[ip] = recent
	return len(recent) < loginMaxFailures
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	l.failures[ip] = append(l.failures[ip], time.Now())
	l.mu.Unlock()
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.failures, ip)
	l.mu.Unlock()
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return false
	}
	return true
}

// writeStoreError 把 ErrNotFound 映射为 404，其余为 400/500。
func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

func queryInt(r *http.Request, key string, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return fallback
	}
	return value
}
