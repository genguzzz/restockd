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
	return &testServer{t: t, http: server, client: server.Client()}
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
	if telegram["bot_token"] != secretMask || telegram["chat_id"] != "42" {
		t.Fatalf("secret not masked: %v", telegram)
	}
	// 回传掩码不应覆盖原值
	server.do(http.MethodPut, "/api/settings", `{"telegram":{"enabled":true,"bot_token":"******","chat_id":"43"},"email":{"password":"******"},"solver":{"external_token":""},"notify_events":["order"]}`)
	_, current, _ := server.do(http.MethodGet, "/api/settings", "")
	if current["telegram"].(map[string]any)["bot_token"] != secretMask {
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
