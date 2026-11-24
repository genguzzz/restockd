package notify

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBarkTargets(t *testing.T) {
	cfg := BarkConfig{DeviceURLs: " https://bark.example.com/KEY1/ ,\nhttp://127.0.0.1:8087/KEY2\nnot-a-url"}
	got := cfg.Targets()
	if len(got) != 2 || got[0] != "https://bark.example.com/KEY1" || got[1] != "http://127.0.0.1:8087/KEY2" {
		t.Fatalf("unexpected targets %v", got)
	}
}

func TestBarkSend(t *testing.T) {
	var mu sync.Mutex
	var bodies []barkPayload
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload barkPayload
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		bodies = append(bodies, payload)
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "BADKEY") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":400,"message":"failed to get device token"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"message":"success"}`))
	}))
	defer server.Close()

	bark := &Bark{Config: BarkConfig{DeviceURLs: server.URL + "/KEY1", Sound: "minuet"}}
	if err := bark.Send(context.Background(), Message{Title: "补货", Body: "LAS-COX Lite", URL: "https://x/y", Event: "restock"}); err != nil {
		t.Fatal(err)
	}
	if paths[0] != "/KEY1" || bodies[0].Level != "timeSensitive" || bodies[0].Group != "restockd" || bodies[0].Sound != "minuet" || bodies[0].URL != "https://x/y" {
		t.Fatalf("unexpected push %s %+v", paths[0], bodies[0])
	}
	if err := bark.Send(context.Background(), Message{Title: "价格变动", Event: "price_change"}); err != nil {
		t.Fatal(err)
	}
	if bodies[1].Level != "active" || bodies[1].Body != "价格变动" {
		t.Fatalf("non-urgent events should be active with body fallback, got %+v", bodies[1])
	}

	bad := &Bark{Config: BarkConfig{DeviceURLs: server.URL + "/BADKEY"}}
	err := bad.Send(context.Background(), Message{Title: "x"})
	if err == nil || strings.Contains(err.Error(), "BADKEY") || !strings.Contains(err.Error(), "failed to get device token") {
		t.Fatalf("error must report failure without leaking key, got %v", err)
	}
}

func TestHubAppliesBark(t *testing.T) {
	hub := NewHub()
	hub.Apply(Settings{Bark: BarkConfig{Enabled: true, DeviceURLs: "https://bark.example.com/KEY"}})
	if len(hub.notifiers) != 1 || hub.notifiers[0].Name() != "bark" {
		t.Fatalf("bark notifier not applied: %+v", hub.notifiers)
	}
	hub.Apply(Settings{Bark: BarkConfig{Enabled: true}})
	if len(hub.notifiers) != 0 {
		t.Fatal("bark without device url must be skipped")
	}
}

func TestMaskBarkURLHidesEverySegment(t *testing.T) {
	for _, target := range []string{"https://api.day.app/AbCdKey", "https://api.day.app/AbCdKey/extra", "http://127.0.0.1:8087/KEY/"} {
		if masked := maskBarkURL(target); strings.Contains(masked, "Key") || strings.Contains(masked, "KEY") {
			t.Fatalf("key leaked: %s -> %s", target, masked)
		}
	}
}

// 启用但没配全的通道（包括邮件）不发送，并在通道状态里标出来。
func TestHubChannels(t *testing.T) {
	hub := NewHub()
	hub.Apply(Settings{
		Telegram: TelegramConfig{Enabled: true, BotToken: "T", ChatID: "1"},
		Email:    EmailConfig{Enabled: true, Host: "smtp.example.com"},
	})
	if len(hub.notifiers) != 1 || hub.notifiers[0].Name() != "telegram" {
		t.Fatalf("only fully configured channels may send: %+v", hub.notifiers)
	}
	got := hub.Channels()
	if got["telegram"] != ChannelOK || got["email"] != ChannelNotConfigured || got["bark"] != ChannelDisabled {
		t.Fatalf("unexpected channel status %v", got)
	}
}

func TestTelegramRedactsTokenFromEveryError(t *testing.T) {
	tg := &Telegram{Config: TelegramConfig{BotToken: "123:SECRET", ChatID: "1", APIBase: "http://bad host"}}
	err := tg.Send(context.Background(), Message{Title: "x"})
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("token leaked or no error: %v", err)
	}
	tg.Config.APIBase = "http://127.0.0.1:1"
	err = tg.Send(context.Background(), Message{Title: "x"})
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("token leaked or no error: %v", err)
	}
}

func TestSplitListSemantics(t *testing.T) {
	if got := splitList(" a@x.com, b@x.com;c@x.com\nd@x.com "); len(got) != 4 || got[3] != "d@x.com" {
		t.Fatalf("unexpected split %v", got)
	}
}

// StartTLS=true 表示必须加密：服务器不支持 STARTTLS 时拒绝发送（不能明文发出密码与邮件）。
func TestEmailRequiresStartTLSWhenConfigured(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		fmt.Fprint(conn, "220 fake ESMTP\r\n")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				fmt.Fprint(conn, "250-fake\r\n250 AUTH PLAIN\r\n")
			case strings.HasPrefix(line, "QUIT"):
				fmt.Fprint(conn, "221 bye\r\n")
				return
			default:
				fmt.Fprint(conn, "250 ok\r\n")
			}
		}
	}()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	email := &Email{Config: EmailConfig{Host: host, Port: port, From: "a@x.com", To: "b@x.com", StartTLS: true}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := email.Send(ctx, Message{Title: "x"}); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("want starttls required error, got %v", err)
	}
}
