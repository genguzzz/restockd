package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/engine"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
	"github.com/genguzzz/restockd/internal/solver"
	"github.com/genguzzz/restockd/internal/store"
)

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

// siteDTO 计数不含隐藏商品，隐藏的单独计 hidden_count。
type siteDTO struct {
	domain.Site
	ProductCount int `json:"product_count"`
	InStockCount int `json:"in_stock_count"`
	HiddenCount  int `json:"hidden_count"`
}

func (s *Server) siteDTO(ctx context.Context, site domain.Site) siteDTO {
	counts, _ := s.store.CountProducts(ctx, site.ID, site.HiddenProducts)
	return siteDTO{Site: site, ProductCount: counts.Visible, InStockCount: counts.InStock, HiddenCount: counts.Hidden}
}

// handleHideProduct 隐藏（hidden=true）/ 取消隐藏商品，返回更新后的站点。
func (s *Server) handleHideProduct(hidden bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		site, err := s.engine.SetProductHidden(r.Context(), r.PathValue("id"), r.PathValue("product_id"), hidden)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.siteDTO(r.Context(), site))
	}
}

func (s *Server) handleListSites(w http.ResponseWriter, r *http.Request) {
	sites, err := s.store.ListSites(r.Context())
	if err != nil {
		writeErr(w, err)
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
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.siteDTO(r.Context(), saved))
}

func (s *Server) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.DeleteSite(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handlePollSite(w http.ResponseWriter, r *http.Request) {
	products, err := s.engine.PollNow(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "products": products})
}

// ---- products ----

type productDTO struct {
	domain.Product
	Watched bool `json:"watched"`
	// Hidden 只在 include_hidden=1 时可能为 true（默认不列出隐藏商品）。
	Hidden bool `json:"hidden,omitempty"`
	// LastOrder 该商品最近一单（任意状态，含试下单）；LastPlacedAt 最近成功锁单时间。
	LastOrder    *orderSummary `json:"last_order,omitempty"`
	LastPlacedAt *time.Time    `json:"last_placed_at,omitempty"`
}

type orderSummary struct {
	Status    domain.OrderStatus `json:"status"`
	CreatedAt time.Time          `json:"created_at"`
	PayURL    string             `json:"pay_url,omitempty"`
	InvoiceID string             `json:"invoice_id,omitempty"`
	Error     string             `json:"error,omitempty"`
}

func (s *Server) handleListProducts(w http.ResponseWriter, r *http.Request) {
	siteID := r.PathValue("id")
	if siteID == "" {
		siteID = r.URL.Query().Get("site_id")
	}
	products, err := s.store.ListProducts(r.Context(), siteID)
	if err != nil {
		writeErr(w, err)
		return
	}
	watches, err := s.store.ListWatches(r.Context(), siteID)
	if err != nil {
		writeErr(w, err)
		return
	}
	orders, err := s.store.LatestOrders(r.Context(), siteID)
	if err != nil {
		writeErr(w, err)
		return
	}
	sites, err := s.store.ListSites(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	watched, hidden := map[store.ProductKey]bool{}, map[store.ProductKey]bool{}
	for _, watch := range watches {
		watched[store.ProductKey{SiteID: watch.SiteID, ProductID: watch.ProductID}] = true
	}
	for _, site := range sites {
		for _, id := range site.HiddenProducts {
			hidden[store.ProductKey{SiteID: site.ID, ProductID: id}] = true
		}
	}
	status := r.URL.Query().Get("status")
	keyword := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	includeHidden := r.URL.Query().Get("include_hidden") == "1"
	out := make([]productDTO, 0, len(products))
	for _, product := range products {
		key := store.ProductKey{SiteID: product.SiteID, ProductID: product.ID}
		if hidden[key] && !includeHidden {
			continue
		}
		if status != "" && string(product.Stock.Status) != status {
			continue
		}
		if keyword != "" && !strings.Contains(strings.ToLower(product.Name+" "+product.Group+" "+product.Specs.Location), keyword) {
			continue
		}
		dto := productDTO{Product: product, Watched: watched[key], Hidden: hidden[key]}
		if state, ok := orders[key]; ok {
			last := state.Last
			dto.LastOrder = &orderSummary{Status: last.Status, CreatedAt: last.CreatedAt, PayURL: last.Result.PayURL, InvoiceID: last.Result.InvoiceID, Error: last.Error}
			if !state.LastPlacedAt.IsZero() {
				dto.LastPlacedAt = &state.LastPlacedAt
			}
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- accounts ----

// accountDTO 账号的对外视图：Cookie 与凭据只回名称 / 用户名 / 是否有密码。
type accountDTO struct {
	domain.Account
	Username    string   `json:"username,omitempty"`
	HasPassword bool     `json:"has_password"`
	CookieNames []string `json:"cookie_names"`
}

func newAccountDTO(account domain.Account) accountDTO {
	names := make([]string, 0, len(account.Cookies))
	for _, cookie := range account.Cookies {
		names = append(names, cookie.Name)
	}
	return accountDTO{Account: account, Username: account.Credentials.Username, HasPassword: account.Credentials.Password != "", CookieNames: names}
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.store.ListAccounts(r.Context(), r.URL.Query().Get("site_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]accountDTO, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, newAccountDTO(account))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSaveAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SiteID     string               `json:"site_id"`
		Label      string               `json:"label"`
		UserAgent  string               `json:"user_agent"`
		Defaults   domain.OrderDefaults `json:"defaults"`
		CookiesRaw string               `json:"cookies_raw"`
		// 托管凭据：username 省略表示不修改；password 省略或掩码不修改，空串清除。
		Username *string `json:"username"`
		Password *string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	input := engine.AccountInput{ID: r.PathValue("id"), SiteID: body.SiteID, Label: body.Label, UserAgent: body.UserAgent, Defaults: body.Defaults, Username: body.Username, Password: body.Password}
	if strings.TrimSpace(body.CookiesRaw) != "" {
		cookies, err := fetch.ParseCookies(body.CookiesRaw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "cookies: "+err.Error())
			return
		}
		input.Cookies = cookies
	}
	saved, err := s.engine.SaveAccount(r.Context(), input)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newAccountDTO(saved))
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.DeleteAccount(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err)
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
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cookie_names": newAccountDTO(account).CookieNames})
}

// handleLoginAccount 用托管凭据立即登录；站点侧失败 502（账号状态同步更新），无凭据 / 站点不支持 400。
func (s *Server) handleLoginAccount(w http.ResponseWriter, r *http.Request) {
	account, err := s.engine.LoginAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newAccountDTO(account))
}

// handleCheckAccount 站点侧的校验结果记在返回的账号状态里；站点类型不支持校验时 400。
func (s *Server) handleCheckAccount(w http.ResponseWriter, r *http.Request) {
	account, err := s.engine.CheckAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newAccountDTO(account))
}

// ---- watches ----

func (s *Server) handleListWatches(w http.ResponseWriter, r *http.Request) {
	watches, err := s.store.ListWatches(r.Context(), r.URL.Query().Get("site_id"))
	if err != nil {
		writeErr(w, err)
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
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteWatch(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.DeleteWatch(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleQuote(w http.ResponseWriter, r *http.Request) {
	quote, err := s.engine.QuoteWatch(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, quote)
}

// handleOrder 手动锁单在后台上下文执行：浏览器断开不应中断已提交到站点的流程。
func (s *Server) handleOrder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DryRun bool `json:"dry_run"`
		Force  bool `json:"force"`
	}
	if !decode(w, r, &body) {
		return
	}
	order, err := s.engine.OrderWatch(context.WithoutCancel(r.Context()), r.PathValue("id"), body.DryRun, body.Force)
	writeOrder(w, order, err)
}

// handleOrderProduct 不经关注规则直接对商品锁单 / 试下单，执行方式同 handleOrder。
func (s *Server) handleOrderProduct(w http.ResponseWriter, r *http.Request) {
	var body engine.ProductOrderInput
	if !decode(w, r, &body) {
		return
	}
	order, err := s.engine.OrderProduct(context.WithoutCancel(r.Context()), r.PathValue("id"), r.PathValue("product_id"), body)
	writeOrder(w, order, err)
}

// writeOrder 已建订单记录（含失败）返回订单本身；24 小时内已锁单返回 409 与已有订单；其余前置错误按 writeErr 映射。
func writeOrder(w http.ResponseWriter, order domain.Order, err error) {
	var duplicate *engine.DuplicateOrderError
	if errors.As(err, &duplicate) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "order": duplicate.Existing})
		return
	}
	if err != nil && order.ID == "" {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, order)
}

// ---- orders / events ----

func (s *Server) handleListOrders(w http.ResponseWriter, r *http.Request) {
	orders, err := s.store.ListOrders(r.Context(), queryInt(r, "limit", 100))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, orders)
}

// handleCancelOrder 在站点取消已锁单的未付款订单；站点不允许取消时 400 并说明原因。
func (s *Server) handleCancelOrder(w http.ResponseWriter, r *http.Request) {
	order, err := s.engine.CancelOrder(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.ListEvents(r.Context(), r.URL.Query().Get("site_id"), queryInt(r, "limit", 200))
	if err != nil {
		writeErr(w, err)
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
			switch item := payload.(type) {
			case domain.Site:
				payload = s.siteDTO(r.Context(), item)
			case domain.Account:
				payload = newAccountDTO(item)
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

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.engine.Settings().Redacted())
}

// handlePutSettings 整体覆盖；打码字段原样传回表示不修改。
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var incoming engine.Settings
	if !decode(w, r, &incoming) {
		return
	}
	if err := s.engine.SaveSettings(r.Context(), incoming); err != nil {
		writeErr(w, err)
		return
	}
	s.handleGetSettings(w, r)
}

func (s *Server) handleTestNotify(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "results": s.engine.TestNotify(r.Context())})
}

func (s *Server) handleTestEgress(w http.ResponseWriter, r *http.Request) {
	probe, err := s.engine.TestEgress(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, probe)
}

// ---- stats ----

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since := time.Now().Add(-24 * time.Hour)
	sites, _ := s.store.ListSites(ctx)
	products, inStock := 0, 0 // 不含隐藏商品
	for _, site := range sites {
		counts, _ := s.store.CountProducts(ctx, site.ID, site.HiddenProducts)
		products += counts.Visible
		inStock += counts.InStock
	}
	watches, _ := s.store.ListWatches(ctx, "")
	orders24h, _ := s.store.CountPlacedSince(ctx, since)
	events24h, _ := s.store.CountEventsSince(ctx, since)
	autoWatches := 0
	for _, watch := range watches {
		if watch.Enabled && watch.AutoOrder {
			autoWatches++
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{
		"sites": len(sites), "products": products, "in_stock": inStock, "watches": len(watches), "auto_watches": autoWatches,
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
