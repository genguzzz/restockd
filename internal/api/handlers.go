package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/egress"
	"github.com/genguzzz/restockd/internal/engine"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/solver"
)

const secretMask = "******"

// ---- providers ----

type providerDTO struct {
	provider.Meta
	MinIntervalSec     int `json:"min_interval_sec"`
	DefaultIntervalSec int `json:"default_interval_sec"`
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	metas := provider.List()
	out := make([]providerDTO, 0, len(metas))
	for _, meta := range metas {
		out = append(out, providerDTO{Meta: meta, MinIntervalSec: int(meta.MinInterval.Seconds()), DefaultIntervalSec: int(meta.DefaultInterval.Seconds())})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- sites ----

type siteDTO struct {
	domain.Site
	ProductCount int `json:"product_count"`
	InStockCount int `json:"in_stock_count"`
}

func (s *Server) siteDTO(ctx context.Context, site domain.Site) siteDTO {
	dto := siteDTO{Site: site}
	products, err := s.store.ListProducts(ctx, site.ID)
	if err != nil {
		return dto
	}
	dto.ProductCount = len(products)
	for _, product := range products {
		if product.Stock.Status.Orderable() {
			dto.InStockCount++
		}
	}
	return dto
}

func (s *Server) handleListSites(w http.ResponseWriter, r *http.Request) {
	sites, err := s.store.ListSites(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := make([]siteDTO, 0, len(sites))
	for _, site := range sites {
		out = append(out, s.siteDTO(r.Context(), site))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSaveSite(w http.ResponseWriter, r *http.Request) {
	var site domain.Site
	if !decode(w, r, &site) {
		return
	}
	if id := r.PathValue("id"); id != "" {
		site.ID = id
	}
	if site.Config == nil {
		site.Config = map[string]string{}
	}
	saved, err := s.engine.SaveSite(r.Context(), site)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.siteDTO(r.Context(), saved))
}

func (s *Server) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.DeleteSite(r.Context(), r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handlePollSite(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	id := r.PathValue("id")
	if err := s.engine.PollNow(ctx, id); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	products, _ := s.store.ListProducts(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "products": len(products)})
}

// ---- products ----

type productDTO struct {
	domain.Product
	Watched bool `json:"watched"`
}

func (s *Server) handleListProducts(w http.ResponseWriter, r *http.Request) {
	siteID := r.PathValue("id")
	if siteID == "" {
		siteID = r.URL.Query().Get("site_id")
	}
	products, err := s.store.ListProducts(r.Context(), siteID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	watches, err := s.store.ListWatches(r.Context(), siteID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	watched := map[string]bool{}
	for _, watch := range watches {
		watched[watch.SiteID+"/"+watch.ProductID] = true
	}
	status := r.URL.Query().Get("status")
	keyword := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	out := make([]productDTO, 0, len(products))
	for _, product := range products {
		if status != "" && string(product.Stock.Status) != status {
			continue
		}
		if keyword != "" && !strings.Contains(strings.ToLower(product.Name+" "+product.Group+" "+product.Specs.Location), keyword) {
			continue
		}
		out = append(out, productDTO{Product: product, Watched: watched[product.SiteID+"/"+product.ID]})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- accounts ----

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.store.ListAccounts(r.Context(), r.URL.Query().Get("site_id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if accounts == nil {
		accounts = []domain.Account{}
	}
	writeJSON(w, http.StatusOK, accounts)
}

func (s *Server) handleSaveAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		domain.Account
		CookiesRaw string `json:"cookies_raw"`
		// 托管凭据：username 省略表示不修改；password 省略或 "******" 不修改，空串清除。
		Username *string `json:"username"`
		Password *string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	account := body.Account
	if id := r.PathValue("id"); id != "" {
		account.ID = id
	}
	saved, err := s.engine.SaveAccount(r.Context(), account)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if body.Username != nil || body.Password != nil {
		username := saved.Credentials.Username
		if body.Username != nil {
			username = strings.TrimSpace(*body.Username)
		}
		password := body.Password
		if password != nil && *password == secretMask {
			password = nil
		}
		if saved, err = s.engine.SetCredentials(r.Context(), saved.ID, username, password); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if strings.TrimSpace(body.CookiesRaw) != "" {
		cookies, err := fetch.ParseCookies(body.CookiesRaw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "cookies: "+err.Error())
			return
		}
		if saved, err = s.engine.SetCookies(r.Context(), saved.ID, cookies, account.UserAgent); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.DeleteAccount(r.Context(), r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSetCookies(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Raw       string `json:"raw"`
		UserAgent string `json:"user_agent"`
	}
	if !decode(w, r, &body) {
		return
	}
	cookies, err := fetch.ParseCookies(body.Raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	account, err := s.engine.SetCookies(r.Context(), r.PathValue("id"), cookies, strings.TrimSpace(body.UserAgent))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cookie_names": account.CookieNames})
}

// handleLoginAccount 用托管凭据立即登录；站点侧失败返回 502 与原因（账号状态同步更新），
// 本地前提不满足（无凭据 / 站点不支持）返回 400。超时留足人工验证的时间。
func (s *Server) handleLoginAccount(w http.ResponseWriter, r *http.Request) {
	timeout := time.Duration(s.engine.Settings().Solver.ManualTimeoutSec)*time.Second + time.Minute
	if timeout < 2*time.Minute {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	account, err := s.engine.LoginAccount(ctx, r.PathValue("id"))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, account)
	case errors.Is(err, engine.ErrNoCredentials), errors.Is(err, engine.ErrNoAuthenticator):
		writeError(w, http.StatusBadRequest, err.Error())
	case account.ID == "":
		writeStoreError(w, err) // 账号或站点不存在
	default:
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

func (s *Server) handleCheckAccount(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	account, err := s.engine.CheckAccount(ctx, r.PathValue("id"))
	if err != nil && account.ID == "" {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, account)
}

// ---- watches ----

func (s *Server) handleListWatches(w http.ResponseWriter, r *http.Request) {
	watches, err := s.store.ListWatches(r.Context(), r.URL.Query().Get("site_id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, watches)
}

func (s *Server) handleSaveWatch(w http.ResponseWriter, r *http.Request) {
	var watch domain.Watch
	if !decode(w, r, &watch) {
		return
	}
	if id := r.PathValue("id"); id != "" {
		watch.ID = id
	}
	saved, err := s.engine.SaveWatch(r.Context(), watch)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteWatch(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.DeleteWatch(r.Context(), r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleQuote(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	quote, err := s.engine.QuoteWatch(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, quote)
}

// handleOrder 手动锁单在后台上下文执行：浏览器断开不应中断已提交到站点的流程。
func (s *Server) handleOrder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DryRun bool `json:"dry_run"`
	}
	if !decode(w, r, &body) {
		return
	}
	order, err := s.engine.OrderWatch(context.WithoutCancel(r.Context()), r.PathValue("id"), body.DryRun)
	if err != nil && order.ID == "" {
		status := http.StatusBadRequest
		if errors.Is(err, engine.ErrOrderInFlight) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, order)
}

// ---- orders / events ----

func (s *Server) handleListOrders(w http.ResponseWriter, r *http.Request) {
	orders, err := s.store.ListOrders(r.Context(), queryInt(r, "limit", 100))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, orders)
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.ListEvents(r.Context(), r.URL.Query().Get("site_id"), queryInt(r, "limit", 200))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if events == nil {
		events = []domain.Event{}
	}
	writeJSON(w, http.StatusOK, events)
}

// handleStream SSE：订阅总线，25 秒注释行保活。
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	messages, unsubscribe := s.engine.Bus().Subscribe()
	defer unsubscribe()
	defer s.engine.ViewerStreamOpened()() // 连接期间视为有人在看，无关注站点保持较高频率
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case message, open := <-messages:
			if !open {
				return
			}
			payload := message.Payload
			if site, isSite := payload.(domain.Site); isSite {
				payload = s.siteDTO(r.Context(), site)
			}
			data, err := json.Marshal(payload)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", message.Name, data)
			flusher.Flush()
		}
	}
}

// ---- challenges ----

func (s *Server) handleListChallenges(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.engine.Broker().List())
}

func (s *Server) handleSolveChallenge(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token      string `json:"token"`
		CookiesRaw string `json:"cookies_raw"`
		UserAgent  string `json:"user_agent"`
	}
	if !decode(w, r, &body) {
		return
	}
	solution := solver.Solution{Token: strings.TrimSpace(body.Token), UserAgent: strings.TrimSpace(body.UserAgent)}
	if strings.TrimSpace(body.CookiesRaw) != "" {
		cookies, err := fetch.ParseCookies(body.CookiesRaw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "cookies: "+err.Error())
			return
		}
		solution.Cookies = cookies
	}
	if solution.Token == "" && len(solution.Cookies) == 0 {
		writeError(w, http.StatusBadRequest, "token or cookies_raw required")
		return
	}
	if err := s.engine.Broker().Submit(r.PathValue("id"), solution); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleCancelChallenge(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.Broker().Cancel(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- settings ----

func maskSecret(value string) string {
	if value == "" {
		return ""
	}
	return secretMask
}

func keepSecret(incoming, current string) string {
	if incoming == secretMask {
		return current
	}
	return incoming
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings := s.engine.Settings()
	settings.Telegram.BotToken = maskSecret(settings.Telegram.BotToken)
	settings.Email.Password = maskSecret(settings.Email.Password)
	settings.Bark.DeviceURLs = maskSecret(settings.Bark.DeviceURLs)
	settings.Solver.ExternalToken = maskSecret(settings.Solver.ExternalToken)
	settings.Egress.Proxies = append([]egress.Proxy(nil), settings.Egress.Proxies...)
	for i := range settings.Egress.Proxies {
		settings.Egress.Proxies[i].URL = maskProxyURL(settings.Egress.Proxies[i].URL)
	}
	if settings.Egress.Proxies == nil {
		settings.Egress.Proxies = []egress.Proxy{}
	}
	if settings.Egress.Default == nil {
		settings.Egress.Default = []string{}
	}
	if settings.NotifyEvents == nil {
		settings.NotifyEvents = []string{}
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var incoming engine.Settings
	if !decode(w, r, &incoming) {
		return
	}
	current := s.engine.Settings()
	incoming.Telegram.BotToken = keepSecret(incoming.Telegram.BotToken, current.Telegram.BotToken)
	incoming.Email.Password = keepSecret(incoming.Email.Password, current.Email.Password)
	incoming.Bark.DeviceURLs = keepSecret(incoming.Bark.DeviceURLs, current.Bark.DeviceURLs)
	incoming.Solver.ExternalToken = keepSecret(incoming.Solver.ExternalToken, current.Solver.ExternalToken)
	stored := map[string]string{}
	for _, proxy := range current.Egress.Proxies {
		stored[proxy.ID] = proxy.URL
	}
	for i, proxy := range incoming.Egress.Proxies {
		if strings.Contains(proxy.URL, secretMask) {
			incoming.Egress.Proxies[i].URL = stored[proxy.ID] // 打码的地址表示密码未改
		}
		if strings.TrimSpace(proxy.ID) == "" || proxy.ID == egress.Direct {
			writeError(w, http.StatusBadRequest, "proxy id is required and must not be \"direct\"")
			return
		}
	}
	if err := s.engine.SaveSettings(r.Context(), incoming); err != nil {
		writeStoreError(w, err)
		return
	}
	s.handleGetSettings(w, r)
}

func (s *Server) handleTestNotify(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	settings := s.engine.Settings()
	results := map[string]string{"telegram": "disabled", "email": "disabled", "bark": "disabled"}
	if settings.Telegram.Enabled {
		results["telegram"] = "not configured"
	}
	if settings.Email.Enabled {
		results["email"] = "not configured"
	}
	if settings.Bark.Enabled {
		results["bark"] = "not configured"
	}
	for channel, err := range s.engine.TestNotify(ctx) {
		results[channel] = "ok"
		if err != nil {
			results[channel] = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "results": results})
}

// maskProxyURL 隐去代理地址里的密码：socks5://user:******@host:port。
func maskProxyURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return raw
	}
	if _, has := parsed.User.Password(); !has {
		return raw
	}
	return strings.Replace(raw, parsed.User.String()+"@", parsed.User.Username()+":"+secretMask+"@", 1)
}

// handleTestEgress 经指定出口访问 Cloudflare trace，返回出口 IP / 地区 / 耗时。
func (s *Server) handleTestEgress(w http.ResponseWriter, r *http.Request) {
	route, err := s.engine.EgressRoute(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	session, _ := fetch.NewSession("https://www.cloudflare.com", nil, "")
	started := time.Now()
	resp, err := route.Client.Do(ctx, session, &fetch.Request{Method: http.MethodGet, URL: "https://www.cloudflare.com/cdn-cgi/trace"})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	result := map[string]any{"ok": true, "ms": time.Since(started).Milliseconds()}
	for _, line := range strings.Split(string(resp.Body), "\n") {
		if key, value, found := strings.Cut(line, "="); found && (key == "ip" || key == "loc" || key == "colo") {
			result[key] = value
		}
	}
	writeJSON(w, http.StatusOK, result)
}

// ---- stats ----

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since := time.Now().Add(-24 * time.Hour)
	sites, _ := s.store.ListSites(ctx)
	products, _ := s.store.ListProducts(ctx, "")
	watches, _ := s.store.ListWatches(ctx, "")
	orders24h, _ := s.store.CountPlacedSince(ctx, since)
	events24h, _ := s.store.CountEventsSince(ctx, since)
	inStock, autoWatches := 0, 0
	for _, product := range products {
		if product.Stock.Status.Orderable() {
			inStock++
		}
	}
	for _, watch := range watches {
		if watch.Enabled && watch.AutoOrder {
			autoWatches++
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{
		"sites": len(sites), "products": len(products), "in_stock": inStock, "watches": len(watches), "auto_watches": autoWatches,
		"orders_24h": orders24h, "pending_challenges": len(s.engine.Broker().List()), "events_24h": events24h,
	})
}

// ---- static ----

// handleStatic 内嵌前端：命中文件直接返回，其余路径回落 index.html（前端 history 路由）。
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if s.static == nil {
		writeError(w, http.StatusNotFound, "web ui not embedded")
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if info, err := fs.Stat(s.static, name); err != nil || info.IsDir() {
		name = "index.html"
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFileFS(w, r, s.static, name)
}
