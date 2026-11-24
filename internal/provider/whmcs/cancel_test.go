package whmcs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
)

// fakeClientArea 客户中心：服务 101 对应账单 555；allowCancel 决定取消页是否有表单；首次访问先要求选择账户。
type fakeClientArea struct {
	mu          sync.Mutex
	allowCancel bool
	chosen      string
	cancelled   map[string]string
}

func (f *fakeClientArea) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = r.ParseForm()
	if r.URL.Path == "/user/accounts" {
		if r.Method == http.MethodPost {
			f.chosen = r.PostForm.Get("id")
			http.Redirect(w, r, "/clientarea.php", http.StatusFound)
			return
		}
		fmt.Fprint(w, `<html><body><a href="/logout.php">Logout</a><div class="select-account"><a href="#" data-id="7">家人</a><a href="#" data-id="6657">gu gen <span>所有者</span></a></div>
<form method="post" action="/user/accounts"><input type="hidden" name="token" value="tk"/><input type="hidden" name="id" value=""/></form></body></html>`)
		return
	}
	if f.chosen == "" {
		http.Redirect(w, r, "/user/accounts", http.StatusFound)
		return
	}
	q := r.URL.Query()
	switch {
	case q.Get("action") == "services":
		fmt.Fprint(w, `<a href="/logout.php">Logout</a><a href="clientarea.php?action=productdetails&amp;id=100">a</a><a href="clientarea.php?action=productdetails&amp;id=101">b</a>`)
	case q.Get("action") == "productdetails":
		invoice := map[string]string{"100": "444", "101": "555"}[q.Get("id")]
		fmt.Fprintf(w, `<a href="/logout.php">Logout</a><a href="viewinvoice.php?id=%s">pay</a>`, invoice)
	case q.Get("action") == "cancel" && r.Method == http.MethodGet:
		if !f.allowCancel {
			http.Redirect(w, r, "/clientarea.php?action=productdetails&id="+q.Get("id"), http.StatusFound)
			return
		}
		fmt.Fprintf(w, `<a href="/logout.php">Logout</a><form method="post" action="clientarea.php?action=cancel&id=%s"><input type="hidden" name="token" value="tk"/><input type="hidden" name="sub" value="submit"/><textarea name="cancellationreason"></textarea><select name="type"><option value="Immediate">立即</option><option value="End of Billing Period">到期</option></select></form>`, q.Get("id"))
	case q.Get("action") == "cancel":
		f.cancelled[q.Get("id")] = r.PostForm.Get("type") + "|" + r.PostForm.Get("cancellationreason")
		fmt.Fprint(w, `<a href="/logout.php">Logout</a><div class="alert alert-success">取消请求已提交</div>`)
	default:
		fmt.Fprint(w, `<a href="/logout.php">Logout</a>`)
	}
}

func newCancelEnv(t *testing.T, site *fakeClientArea) (*Provider, provider.Env) {
	t.Helper()
	server := httptest.NewServer(site)
	t.Cleanup(server.Close)
	instance, _ := New(domain.Site{ID: "cs", BaseURL: server.URL, Config: map[string]string{"groups": "g"}})
	client, _ := fetch.NewHTTPClient(5*time.Second, "")
	session, _ := fetch.NewSession(server.URL, nil, "")
	return instance.(*Provider), provider.Env{Client: client, Session: session}
}

func TestCancelOrderSubmitsImmediateCancellation(t *testing.T) {
	site := &fakeClientArea{allowCancel: true, cancelled: map[string]string{}}
	p, env := newCancelEnv(t, site)
	if err := p.CancelOrder(context.Background(), env, domain.OrderResult{InvoiceID: "555", PayURL: "https://x/viewinvoice.php?id=555"}); err != nil {
		t.Fatal(err)
	}
	if site.chosen != "6657" {
		t.Fatalf("account chooser must pick the owner account, got %q", site.chosen)
	}
	if site.cancelled["101"] != "Immediate|"+cancelReason || len(site.cancelled) != 1 {
		t.Fatalf("must cancel the service tied to invoice 555 immediately, got %v", site.cancelled)
	}
}

func TestCancelOrderNotAllowed(t *testing.T) {
	site := &fakeClientArea{allowCancel: false, cancelled: map[string]string{}}
	p, env := newCancelEnv(t, site)
	err := p.CancelOrder(context.Background(), env, domain.OrderResult{PayURL: "https://x/viewinvoice.php?id=555"})
	if !errors.Is(err, provider.ErrNotSupported) {
		t.Fatalf("pending services that cannot be cancelled must report ErrNotSupported, got %v", err)
	}
	if err := p.CancelOrder(context.Background(), env, domain.OrderResult{InvoiceID: "999"}); err == nil || errors.Is(err, provider.ErrNotSupported) {
		t.Fatalf("unknown invoice must be a plain error, got %v", err)
	}
}

// Lagom2（RSThemes）：服务列表首屏只有 AJAX 模板，服务 ID 来自 clientApi.php。
func TestServiceForInvoiceRSThemes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/modules/addons/RSThemes/src/Api/clientApi.php" && q.Get("method") == "getClientServices":
			fmt.Fprint(w, `{"draw":1,"recordsTotal":2,"data":[{"id":17515},{"id":"17743"}]}`)
		case q.Get("action") == "services":
			fmt.Fprint(w, `<a href="/logout.php">Logout</a><script>ajax: { url: '/modules/addons/RSThemes/src/Api/clientApi.php?controller=ClientData&method=getClientServices' } // productdetails&id=`+"`+data.id+`"+`</script>`)
		case q.Get("action") == "productdetails":
			fmt.Fprintf(w, `<a href="/logout.php">Logout</a><a href="viewinvoice.php?id=%s">pay</a>`, map[string]string{"17515": "1", "17743": "33031"}[q.Get("id")])
		default:
			fmt.Fprint(w, `<a href="/logout.php">Logout</a>`)
		}
	}))
	defer server.Close()
	instance, _ := New(domain.Site{ID: "nv", BaseURL: server.URL, Config: map[string]string{"groups": "g"}})
	client, _ := fetch.NewHTTPClient(5*time.Second, "")
	session, _ := fetch.NewSession(server.URL, nil, "")
	id, err := instance.(*Provider).serviceForInvoice(context.Background(), provider.Env{Client: client, Session: session}, "33031")
	if err != nil || id != "17743" {
		t.Fatalf("want service 17743 via RSThemes api, got %q %v", id, err)
	}
}
