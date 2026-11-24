package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/engine"
	_ "github.com/genguzzz/restockd/internal/provider/usvps24"
	_ "github.com/genguzzz/restockd/internal/provider/whmcs"
	"github.com/genguzzz/restockd/internal/store"
)

type testServer struct {
	t      *testing.T
	http   *httptest.Server
	client *http.Client
	cookie *http.Cookie
	store  *store.Store
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng, err := engine.New(engine.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Stop)
	static := fstest.MapFS{"index.html": {Data: []byte("<html>spa</html>")}, "assets/app.js": {Data: []byte("console.log(1)")}}
	handler, err := New(ctx, Options{Engine: eng, Store: st, Password: "pw", Static: static})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &testServer{t: t, http: server, client: server.Client(), store: st}
}

func (s *testServer) do(method, path, body string) (*http.Response, map[string]any, []any) {
	s.t.Helper()
	req, _ := http.NewRequest(method, s.http.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.cookie != nil {
		req.AddCookie(s.cookie)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&raw)
	var object map[string]any
	var list []any
	if json.Unmarshal(raw, &object) != nil {
		_ = json.Unmarshal(raw, &list)
	}
	return resp, object, list
}

func (s *testServer) login() {
	resp, _, _ := s.do(http.MethodPost, "/api/login", `{"password":"pw"}`)
	if resp.StatusCode != http.StatusOK {
		s.t.Fatalf("login status %d", resp.StatusCode)
	}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == sessionCookie {
			s.cookie = cookie
		}
	}
	if s.cookie == nil || !s.cookie.HttpOnly {
		s.t.Fatal("session cookie missing or not HttpOnly")
	}
}

func TestAuthRequired(t *testing.T) {
	server := newTestServer(t)
	if resp, _, _ := server.do(http.MethodGet, "/api/sites", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	if resp, body, _ := server.do(http.MethodPost, "/api/login", `{"password":"nope"}`); resp.StatusCode != http.StatusUnauthorized || body["error"] == nil {
		t.Fatalf("wrong password should be 401 with error, got %d %v", resp.StatusCode, body)
	}
	server.cookie = &http.Cookie{Name: sessionCookie, Value: "99999999999.deadbeef"}
	if resp, _, _ := server.do(http.MethodGet, "/api/me", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("forged cookie accepted")
	}
	server.cookie = nil
	server.login()
	if resp, body, _ := server.do(http.MethodGet, "/api/me", ""); resp.StatusCode != http.StatusOK || body["version"] != Version {
		t.Fatalf("me failed: %d %v", resp.StatusCode, body)
	}
}

func TestLoginRateLimited(t *testing.T) {
	server := newTestServer(t)
	for i := 0; i < loginMaxFailures; i++ {
		server.do(http.MethodPost, "/api/login", `{"password":"bad"}`)
	}
	if resp, _, _ := server.do(http.MethodPost, "/api/login", `{"password":"pw"}`); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("want 429 after repeated failures, got %d", resp.StatusCode)
	}
}

func TestSettingsMaskRoundTrip(t *testing.T) {
	server := newTestServer(t)
	server.login()
	_, saved, _ := server.do(http.MethodPut, "/api/settings", `{"telegram":{"enabled":true,"bot_token":"123:SECRET","chat_id":"42"},"email":{"password":"mailpw"},"solver":{"external_token":"ext"},"notify_events":["restock"]}`)
	telegram := saved["telegram"].(map[string]any)
	if telegram["bot_token"] != engine.SecretMask || telegram["chat_id"] != "42" {
		t.Fatalf("secret not masked: %v", telegram)
	}
	// 回传掩码不应覆盖原值
	server.do(http.MethodPut, "/api/settings", `{"telegram":{"enabled":true,"bot_token":"******","chat_id":"43"},"email":{"password":"******"},"solver":{"external_token":""},"notify_events":["order"]}`)
	_, current, _ := server.do(http.MethodGet, "/api/settings", "")
	if current["telegram"].(map[string]any)["bot_token"] != engine.SecretMask {
		t.Fatal("bot token lost after masked round trip")
	}
	if current["solver"].(map[string]any)["external_token"] != "" {
		t.Fatal("explicitly cleared secret should be empty")
	}
}

func TestAccountCookiesNeverReturned(t *testing.T) {
	server := newTestServer(t)
	server.login()
	resp, site, _ := server.do(http.MethodPost, "/api/sites", `{"kind":"usvps24","name":"u","base_url":"https://usvps24.com","config":{},"interval_sec":60,"fetch_mode":"direct","enabled":false}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create site %d %v", resp.StatusCode, site)
	}
	siteID := site["id"].(string)
	_, account, _ := server.do(http.MethodPost, "/api/accounts", `{"site_id":"`+siteID+`","label":"main","cookies_raw":"laravel_session=TOPSECRET; XSRF-TOKEN=x"}`)
	if names, _ := account["cookie_names"].([]any); len(names) != 2 {
		t.Fatalf("cookie names missing: %v", account)
	}
	resp2, _, list := server.do(http.MethodGet, "/api/accounts", "")
	raw, _ := json.Marshal(list)
	if resp2.StatusCode != http.StatusOK || strings.Contains(string(raw), "TOPSECRET") {
		t.Fatalf("cookie value leaked: %s", raw)
	}
	if resp, body, _ := server.do(http.MethodPost, "/api/accounts/"+account["id"].(string)+"/cookies", `{"raw":"   "}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty cookies should 400, got %d %v", resp.StatusCode, body)
	}
}

func TestAccountViewAndErrorMapping(t *testing.T) {
	server := newTestServer(t)
	server.login()
	_, site, _ := server.do(http.MethodPost, "/api/sites", `{"kind":"usvps24","name":"u","base_url":"https://usvps24.com","config":{},"enabled":false}`)
	siteID := site["id"].(string)
	_, account, _ := server.do(http.MethodPost, "/api/accounts", `{"site_id":"`+siteID+`","label":"main","username":"me","password":"pw"}`)
	if account["username"] != "me" || account["has_password"] != true || account["password"] != nil {
		t.Fatalf("credential view wrong: %v", account)
	}
	id := account["id"].(string)
	// password 回传掩码不修改。
	_, updated, _ := server.do(http.MethodPut, "/api/accounts/"+id, `{"site_id":"`+siteID+`","label":"renamed","password":"`+engine.SecretMask+`"}`)
	if updated["label"] != "renamed" || updated["has_password"] != true {
		t.Fatalf("masked password must be kept: %v", updated)
	}
	if resp, _, _ := server.do(http.MethodPost, "/api/watches", `{"site_id":"`+siteID+`","product_id":"1","account_id":"`+id+`","enabled":true}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("create watch %d", resp.StatusCode)
	}
	if resp, body, _ := server.do(http.MethodDelete, "/api/accounts/"+id, ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("deleting a referenced account should 400, got %d %v", resp.StatusCode, body)
	}
	if resp, _, _ := server.do(http.MethodPost, "/api/accounts/missing/check", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown account should 404, got %d", resp.StatusCode)
	}
	if resp, _, _ := server.do(http.MethodPost, "/api/sites/"+siteID+"/poll", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("polling a disabled site should 400, got %d", resp.StatusCode)
	}
	if resp, _, _ := server.do(http.MethodPut, "/api/settings", `{"egress":{"proxies":[{"id":"direct","url":"http://x:1"}]}}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid proxy id should 400, got %d", resp.StatusCode)
	}
	if resp, body, _ := server.do(http.MethodPost, "/api/egress/nope/test", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown egress should 400, got %d %v", resp.StatusCode, body)
	}
}

func TestProductOrderStateAndDuplicateGuard(t *testing.T) {
	server := newTestServer(t)
	server.login()
	ctx := context.Background()
	_, site, _ := server.do(http.MethodPost, "/api/sites", `{"kind":"usvps24","name":"u","base_url":"https://usvps24.com","config":{},"enabled":false}`)
	siteID := site["id"].(string)
	_, account, _ := server.do(http.MethodPost, "/api/accounts", `{"site_id":"`+siteID+`","label":"main"}`)
	accountID := account["id"].(string)
	_ = server.store.ReplaceProducts(ctx, siteID, []domain.Product{{ID: "1", Name: "A"}, {ID: "2", Name: "B"}})
	placedAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	_ = server.store.SaveOrder(ctx, domain.Order{ID: "o1", SiteID: siteID, ProductID: "1", AccountID: accountID, Status: domain.OrderPlaced, Result: domain.OrderResult{InvoiceID: "9", PayURL: "https://pay/9"}, CreatedAt: placedAt})

	_, _, products := server.do(http.MethodGet, "/api/sites/"+siteID+"/products", "")
	if len(products) != 2 {
		t.Fatalf("want 2 products, got %v", products)
	}
	first, second := products[0].(map[string]any), products[1].(map[string]any)
	last, _ := first["last_order"].(map[string]any)
	if last["status"] != "placed" || last["invoice_id"] != "9" || last["pay_url"] != "https://pay/9" || first["last_placed_at"] == nil {
		t.Fatalf("order state missing: %v", first)
	}
	if _, has := second["last_order"]; has || second["last_placed_at"] != nil {
		t.Fatalf("product without orders must omit state: %v", second)
	}

	resp, body, _ := server.do(http.MethodPost, "/api/sites/"+siteID+"/products/1/order", `{"account_id":"`+accountID+`","dry_run":false}`)
	existing, _ := body["order"].(map[string]any)
	if resp.StatusCode != http.StatusConflict || existing["id"] != "o1" || body["error"] == nil {
		t.Fatalf("duplicate order should 409 with existing order, got %d %v", resp.StatusCode, body)
	}
	if resp, body, _ := server.do(http.MethodPost, "/api/sites/"+siteID+"/products/1/order", `{"dry_run":true}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing account should 400, got %d %v", resp.StatusCode, body)
	}
	if resp, _, _ := server.do(http.MethodPost, "/api/sites/"+siteID+"/products/nope/order", `{"account_id":"`+accountID+`"}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown product should 404, got %d", resp.StatusCode)
	}
}

func TestHideProducts(t *testing.T) {
	server := newTestServer(t)
	server.login()
	ctx := context.Background()
	_, site, _ := server.do(http.MethodPost, "/api/sites", `{"kind":"usvps24","name":"u","base_url":"https://usvps24.com","config":{},"enabled":false}`)
	siteID := site["id"].(string)
	in := domain.Stock{Status: domain.StockIn}
	_ = server.store.ReplaceProducts(ctx, siteID, []domain.Product{{ID: "1", Name: "定制套餐", Stock: in}, {ID: "2", Name: "B", Stock: in}})
	resp, hidden, _ := server.do(http.MethodPost, "/api/sites/"+siteID+"/products/1/hide", "")
	if resp.StatusCode != http.StatusOK || hidden["product_count"] != 1.0 || hidden["in_stock_count"] != 1.0 || hidden["hidden_count"] != 1.0 {
		t.Fatalf("hide: %d %v", resp.StatusCode, hidden)
	}
	if ids, _ := hidden["hidden_products"].([]any); len(ids) != 1 || ids[0] != "1" {
		t.Fatalf("hidden_products: %v", hidden)
	}
	// 普通 PUT 站点不会清掉隐藏列表。
	server.do(http.MethodPut, "/api/sites/"+siteID, `{"kind":"usvps24","name":"renamed","base_url":"https://usvps24.com","config":{},"enabled":false}`)
	for _, path := range []string{"/api/products", "/api/sites/" + siteID + "/products"} {
		if _, _, list := server.do(http.MethodGet, path, ""); len(list) != 1 || list[0].(map[string]any)["id"] != "2" {
			t.Fatalf("%s must exclude hidden products: %v", path, list)
		}
	}
	_, _, all := server.do(http.MethodGet, "/api/products?include_hidden=1", "")
	if len(all) != 2 || all[0].(map[string]any)["hidden"] != true || all[1].(map[string]any)["hidden"] != nil {
		t.Fatalf("include_hidden must list and flag hidden products: %v", all)
	}
	if _, stats, _ := server.do(http.MethodGet, "/api/stats", ""); stats["products"] != 1.0 {
		t.Fatalf("stats must exclude hidden products: %v", stats)
	}
	_, shown, _ := server.do(http.MethodPost, "/api/sites/"+siteID+"/products/1/unhide", "")
	if shown["product_count"] != 2.0 || shown["hidden_count"] != 0.0 || shown["hidden_products"] != nil {
		t.Fatalf("unhide: %v", shown)
	}
	if resp, _, _ := server.do(http.MethodPost, "/api/sites/missing/products/1/hide", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown site should 404, got %d", resp.StatusCode)
	}
}

func TestSiteValidationAndProviders(t *testing.T) {
	server := newTestServer(t)
	server.login()
	if resp, _, _ := server.do(http.MethodPost, "/api/sites", `{"kind":"whmcs","name":"x","base_url":"https://x.test","config":{}}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing required groups should 400, got %d", resp.StatusCode)
	}
	if resp, _, _ := server.do(http.MethodPost, "/api/sites", `{"kind":"nope","name":"x","base_url":"https://x.test"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatal("unknown kind accepted")
	}
	_, _, providers := server.do(http.MethodGet, "/api/providers", "")
	kinds := map[string]bool{}
	for _, item := range providers {
		meta := item.(map[string]any)
		kinds[meta["kind"].(string)] = true
		if meta["min_interval_sec"].(float64) <= 0 {
			t.Fatalf("min interval missing: %v", meta)
		}
	}
	if !kinds["whmcs"] || !kinds["usvps24"] {
		t.Fatalf("providers missing: %v", kinds)
	}
}

func TestSPAFallback(t *testing.T) {
	server := newTestServer(t)
	for _, path := range []string{"/", "/watches", "/settings"} {
		resp, err := server.client.Get(server.http.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		n, _ := resp.Body.Read(buf)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(buf[:n]), "spa") {
			t.Fatalf("%s: status %d body %q", path, resp.StatusCode, buf[:n])
		}
	}
	resp, _ := server.client.Get(server.http.URL + "/assets/app.js")
	resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatal("assets should be immutable cached")
	}
	if resp, _, _ := server.do(http.MethodGet, "/api/nope", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown api should 404 not SPA, got %d", resp.StatusCode)
	}
}
