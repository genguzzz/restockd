package whmcs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/provider"
)

// fakeTermsSite 模拟 AaITR 的条款插件：同意前结算页只有 agreetermsform；条款页要求 readconfirmed=1 且勾选同意。
type fakeTermsSite struct {
	mu     sync.Mutex
	agreed bool
	posts  []string
}

func (f *fakeTermsSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = r.ParseForm()
	switch {
	case r.URL.Path == "/cart.php" && r.URL.Query().Get("a") == "checkout":
		if !f.agreed {
			fmt.Fprint(w, `<html><body><a href="/logout.php">Logout</a><form name="agreetermsform" id="agreetermsform" action="index.php?m=agreeterms" method="post"><input type="hidden" name="productid" value="4"><input type="hidden" name="redirectto" value="checkout"></form></body></html>`)
			return
		}
		fmt.Fprint(w, `<html><body><a href="/logout.php">Logout</a><form id="frmCheckout" method="post" action="/cart.php?a=checkout"><input type="radio" name="paymentmethod" value="epusdt"/></form></body></html>`)
	case r.URL.Path == "/index.php" && r.URL.Query().Get("m") == "agreeterms" && r.PostForm.Get("readconfirmed") == "":
		f.posts = append(f.posts, "open:"+r.PostForm.Get("productid"))
		fmt.Fprint(w, `<html><body><a href="/logout.php">Logout</a><script>var waitSeconds = 3;</script><form id="agreetermssuccess" method="post" action=""><input type="hidden" name="token" value="t"/><input type="hidden" name="agreeterms" value="true"><input type="hidden" name="productid" value="4"><input type="hidden" name="readconfirmed" id="readconfirmed" value="0"><input type="checkbox" name="agreeterms" id="agreeterms" value="1"></form></body></html>`)
	case r.URL.Path == "/index.php" && r.URL.Query().Get("m") == "agreeterms":
		values := r.PostForm["agreeterms"]
		f.posts = append(f.posts, "agree:"+r.PostForm.Get("readconfirmed")+":"+strings.Join(values, ","))
		if r.PostForm.Get("readconfirmed") == "1" && len(values) > 0 && values[len(values)-1] == "1" && r.PostForm.Get("token") == "t" {
			f.agreed = true
		}
		http.Redirect(w, r, "/cart.php?a=view", http.StatusFound)
	case r.URL.Path == "/cart.php":
		fmt.Fprint(w, `<html><body><a href="/logout.php">Logout</a></body></html>`)
	default:
		http.NotFound(w, r)
	}
}

func TestAcceptTerms(t *testing.T) {
	var waited time.Duration
	restore := termsSleep
	termsSleep = func(_ context.Context, d time.Duration) error { waited = d; return nil }
	defer func() { termsSleep = restore }()

	site := &fakeTermsSite{}
	server := httptest.NewServer(site)
	defer server.Close()
	instance, _ := New(domain.Site{ID: "aa", BaseURL: server.URL, Config: map[string]string{"groups": "srv"}})
	p := instance.(*Provider)
	client, _ := fetch.NewHTTPClient(5*time.Second, "")
	session, _ := fetch.NewSession(server.URL, nil, "")
	env := provider.Env{Client: client, Session: session}

	checkout, err := p.get(context.Background(), env, "/cart.php?a=checkout")
	if err != nil {
		t.Fatal(err)
	}
	checkout, err = p.acceptTerms(context.Background(), env, checkout)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.doc.Find("#frmCheckout").Length() != 1 {
		t.Fatal("checkout form must be available after agreeing")
	}
	if waited != 4*time.Second {
		t.Fatalf("must wait the page's waitSeconds + 1s, waited %s", waited)
	}
	if len(site.posts) != 2 || site.posts[0] != "open:4" || site.posts[1] != "agree:1:true,1" {
		t.Fatalf("unexpected terms requests %v", site.posts)
	}
}

func TestAcceptTermsGivesUp(t *testing.T) {
	restore := termsSleep
	termsSleep = func(context.Context, time.Duration) error { return nil }
	defer func() { termsSleep = restore }()
	// 永不放行：每轮同意后结算页仍是条款表单。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Query().Get("m") == "agreeterms" && r.PostForm.Get("readconfirmed") == "" {
			fmt.Fprint(w, `<a href="/logout.php">Logout</a><form id="agreetermssuccess" action=""><input type="hidden" name="productid" value="4"></form>`)
			return
		}
		fmt.Fprint(w, `<a href="/logout.php">Logout</a><form id="agreetermsform" action="index.php?m=agreeterms"><input type="hidden" name="productid" value="4"></form>`)
	}))
	defer server.Close()
	instance, _ := New(domain.Site{ID: "aa", BaseURL: server.URL, Config: map[string]string{"groups": "srv"}})
	p := instance.(*Provider)
	client, _ := fetch.NewHTTPClient(5*time.Second, "")
	session, _ := fetch.NewSession(server.URL, nil, "")
	env := provider.Env{Client: client, Session: session}
	checkout, _ := p.get(context.Background(), env, "/cart.php?a=checkout")
	if _, err := p.acceptTerms(context.Background(), env, checkout); err == nil || !strings.Contains(err.Error(), "not accepted") {
		t.Fatalf("want a clear error when the terms gate never opens, got %v", err)
	}
}
