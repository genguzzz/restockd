package engine

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/egress"
	"github.com/genguzzz/restockd/internal/store"
)

func TestRedactProxyURL(t *testing.T) {
	cases := map[string]string{
		"socks5://u:secret@127.0.0.1:1080": "socks5://u:******@127.0.0.1:1080",
		"http://127.0.0.1:10809":           "http://127.0.0.1:10809",
		"http://user@host:8080":            "http://user@host:8080",
	}
	for in, want := range cases {
		if got := redactProxyURL(in); got != want {
			t.Errorf("redactProxyURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMergeProxyURLRestoresOnlyPassword(t *testing.T) {
	stored := "socks5://u:p%40ss@old.host:1080"
	cases := map[string]string{
		"socks5://u:******@new.host:2080": "socks5://u:p%40ss@new.host:2080", // 改了主机端口，密码沿用
		"socks5://v:******@old.host:1080": "socks5://v:p%40ss@old.host:1080", // 改了用户名
		"socks5://u:new@old.host:1080":    "socks5://u:new@old.host:1080",    // 改了密码
		"socks5://old.host:1080":          "socks5://old.host:1080",          // 去掉认证
	}
	for in, want := range cases {
		if got := mergeProxyURL(in, stored); got != want {
			t.Errorf("mergeProxyURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := mergeProxyURL("http://u:******@h:1", ""); got != "http://u@h:1" {
		t.Errorf("mask without stored password must drop it, got %q", got)
	}
}

func newSettingsEngine(t *testing.T) (*Engine, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	engine, _ := New(Options{Store: st})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Stop)
	return engine, st
}

func TestSaveSettingsSecretsAndValidation(t *testing.T) {
	ctx := context.Background()
	engine, _ := newSettingsEngine(t)
	settings := DefaultSettings()
	settings.Telegram.BotToken = "tok"
	settings.Egress.Proxies = []egress.Proxy{{ID: "dmit", URL: "socks5://u:pw@old:1080", Enabled: true}}
	if err := engine.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	redacted := engine.Settings().Redacted()
	if redacted.Telegram.BotToken != SecretMask || redacted.Egress.Proxies[0].URL != "socks5://u:******@old:1080" {
		t.Fatalf("not redacted: %+v", redacted)
	}
	if engine.Settings().Egress.Proxies[0].URL != "socks5://u:pw@old:1080" {
		t.Fatal("Redacted must not mutate the live settings")
	}
	redacted.Egress.Proxies[0].URL = "socks5://u:******@new:2080"
	if err := engine.SaveSettings(ctx, redacted); err != nil {
		t.Fatal(err)
	}
	current := engine.Settings()
	if current.Telegram.BotToken != "tok" || current.Egress.Proxies[0].URL != "socks5://u:pw@new:2080" {
		t.Fatalf("masked round trip lost data: %+v", current)
	}
	for _, proxies := range [][]egress.Proxy{{{ID: "direct"}}, {{ID: " "}}, {{ID: "a"}, {ID: "a"}}} {
		bad := current
		bad.Egress.Proxies = proxies
		if err := engine.SaveSettings(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("proxies %+v must be invalid, got %v", proxies, err)
		}
	}
}

func TestRedactedHasNoNullLists(t *testing.T) {
	raw, _ := json.Marshal(Settings{}.Redacted())
	if strings.Contains(string(raw), "null") {
		t.Fatalf("null list in settings JSON: %s", raw)
	}
}

func TestSaveSettingsPrunesRemovedProxyFromSites(t *testing.T) {
	ctx := context.Background()
	engine, st := newSettingsEngine(t)
	settings := DefaultSettings()
	settings.Egress.Proxies = []egress.Proxy{{ID: "dmit", URL: "socks5://127.0.0.1:1", Enabled: true}, {ID: "home", URL: "socks5://127.0.0.1:2", Enabled: true}}
	if err := engine.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	_ = st.SaveSite(ctx, domain.Site{ID: "a", Egress: []string{"dmit", "home", "direct"}})
	_ = st.SaveSite(ctx, domain.Site{ID: "b", Egress: []string{"home"}})
	settings.Egress.Proxies = settings.Egress.Proxies[1:] // 删除 dmit
	if err := engine.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	a, _ := st.GetSite(ctx, "a")
	b, _ := st.GetSite(ctx, "b")
	if !slices.Equal(a.Egress, []string{"home", "direct"}) || !slices.Equal(b.Egress, []string{"home"}) {
		t.Fatalf("removed proxy not pruned: a=%v b=%v", a.Egress, b.Egress)
	}
}
