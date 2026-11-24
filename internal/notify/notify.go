// Package notify 通知抽象：Telegram、SMTP 邮件与 Bark（iOS 推送），统一由 Hub 按设置分发。
package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"maps"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/genguzzz/restockd/internal/domain"
)

// Message 一条通知。
type Message struct {
	Title string
	Body  string
	URL   string
	// Event 事件类型，用于按设置过滤（restock / order / challenge ...）。
	Event string
}

// Notifier 统一通知接口。
type Notifier interface {
	Name() string
	Send(ctx context.Context, message Message) error
}

// defaultClient Telegram / Bark 共用的 HTTP 客户端。
var defaultClient = &http.Client{Timeout: 15 * time.Second}

// TelegramConfig Telegram Bot 配置。
type TelegramConfig struct {
	Enabled  bool   `json:"enabled"`
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
	// APIBase 可替换为自建反代，默认 https://api.telegram.org。
	APIBase string `json:"api_base"`
}

func (c TelegramConfig) ready() bool { return c.BotToken != "" && c.ChatID != "" }

// EmailConfig SMTP 配置。端口 465 走隐式 TLS；其余端口服务器支持即升级 STARTTLS，
// StartTLS=true 表示必须加密，服务器不支持时拒绝发送。
type EmailConfig struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
	To       string `json:"to"`
	StartTLS bool   `json:"starttls"`
}

func (c EmailConfig) ready() bool { return c.Host != "" && c.From != "" && len(c.recipients()) > 0 }

func (c EmailConfig) recipients() []string { return splitList(c.To) }

// BarkConfig Bark（https://github.com/Finb/bark-server）iOS 推送配置。
// DeviceURLs 为设备地址 https://<server>/<device key>，多个用逗号或换行分隔；含设备 key，属于密钥。
type BarkConfig struct {
	Enabled    bool   `json:"enabled"`
	DeviceURLs string `json:"device_urls"`
	// Group 通知分组（iOS 通知中心按组折叠），默认 restockd。
	Group string `json:"group"`
	// Sound 铃声名（Bark App 内置铃声），空为系统默认。
	Sound string `json:"sound"`
	// Icon 通知图标 URL（iOS 15+），可空。
	Icon string `json:"icon"`
}

func (c BarkConfig) ready() bool { return len(c.Targets()) > 0 }

// Targets 解析设备地址列表，忽略非 http(s) 项。
func (c BarkConfig) Targets() []string {
	var targets []string
	for _, item := range splitList(c.DeviceURLs) {
		item = strings.TrimRight(item, "/")
		if strings.HasPrefix(item, "https://") || strings.HasPrefix(item, "http://") {
			targets = append(targets, item)
		}
	}
	return targets
}

// splitList 与 provider.SplitList 同一语义：逗号、分号、空白（含换行）分隔。notify 不依赖 provider，故单独保留。
func splitList(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || unicode.IsSpace(r) })
}

// postJSON POST JSON 并读取响应（最多 4 KiB）；secret 非空时从所有错误里抹掉（URL 里的 bot token / 设备 key）。
func postJSON(ctx context.Context, client *http.Client, target string, payload []byte, secret string) (int, []byte, error) {
	redact := func(err error) error {
		if secret == "" {
			return err
		}
		return errors.New(strings.ReplaceAll(err.Error(), secret, "***"))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, redact(err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if client == nil {
		client = defaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, redact(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, body, nil
}

// barkTimeSensitive 需要立即处理的事件用 timeSensitive（可突破专注模式），其余为普通推送。
var barkTimeSensitive = map[domain.EventType]bool{domain.EventRestock: true, domain.EventOrder: true, domain.EventChallenge: true, domain.EventAccount: true}

// Bark iOS 推送（也会同步到配对的 Apple Watch）。
type Bark struct {
	Config BarkConfig
	Client *http.Client
}

type barkPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Group string `json:"group,omitempty"`
	URL   string `json:"url,omitempty"`
	Level string `json:"level,omitempty"`
	Sound string `json:"sound,omitempty"`
	Icon  string `json:"icon,omitempty"`
}

// Name 实现 Notifier。
func (b *Bark) Name() string { return "bark" }

// Send 实现 Notifier：向每个设备 POST JSON，任一失败返回错误（错误里隐去设备 key）。
func (b *Bark) Send(ctx context.Context, message Message) error {
	targets := b.Config.Targets()
	if len(targets) == 0 {
		return errors.New("bark: no device url")
	}
	payload := barkPayload{Title: message.Title, Body: message.Body, Group: b.Config.Group, URL: message.URL, Level: "active", Sound: b.Config.Sound, Icon: b.Config.Icon}
	if payload.Group == "" {
		payload.Group = "restockd"
	}
	if payload.Body == "" {
		payload.Body = message.Title
	}
	if barkTimeSensitive[domain.EventType(message.Event)] {
		payload.Level = "timeSensitive"
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var failures []string
	for _, target := range targets {
		if err := b.send(ctx, target, raw); err != nil {
			failures = append(failures, maskBarkURL(target)+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("bark: %s", strings.Join(failures, "; "))
	}
	return nil
}

func (b *Bark) send(ctx context.Context, target string, raw []byte) error {
	status, body, err := postJSON(ctx, b.Client, target, raw, target)
	if err != nil {
		return err
	}
	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &result); err != nil || status != http.StatusOK || result.Code != http.StatusOK {
		return fmt.Errorf("http %d %s", status, strings.TrimSpace(result.Message))
	}
	return nil
}

// maskBarkURL 只保留协议与主机：https://bark.example.com/…（路径里的设备 key 及其后的段一律隐去）。
func maskBarkURL(target string) string {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" {
		return "bark"
	}
	return parsed.Scheme + "://" + parsed.Host + "/…"
}

// Telegram Bot 通知。
type Telegram struct {
	Config TelegramConfig
	Client *http.Client
}

// Name 实现 Notifier。
func (t *Telegram) Name() string { return "telegram" }

// Send 实现 Notifier。
func (t *Telegram) Send(ctx context.Context, message Message) error {
	base := strings.TrimRight(t.Config.APIBase, "/")
	if base == "" {
		base = "https://api.telegram.org"
	}
	text := "<b>" + html.EscapeString(message.Title) + "</b>"
	if message.Body != "" {
		text += "\n" + html.EscapeString(message.Body)
	}
	if message.URL != "" {
		text += "\n" + html.EscapeString(message.URL)
	}
	payload, err := json.Marshal(map[string]any{"chat_id": t.Config.ChatID, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true})
	if err != nil {
		return err
	}
	status, body, err := postJSON(ctx, t.Client, base+"/bot"+t.Config.BotToken+"/sendMessage", payload, t.Config.BotToken)
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("telegram: http %d: %s", status, strings.TrimSpace(string(body)))
	}
	return nil
}

// Email SMTP 通知。
type Email struct {
	Config EmailConfig
}

// Name 实现 Notifier。
func (e *Email) Name() string { return "email" }

// Send 实现 Notifier。
func (e *Email) Send(ctx context.Context, message Message) error {
	cfg := e.Config
	if !cfg.ready() {
		return errors.New("email: host, from and to are required")
	}
	recipients := cfg.recipients()
	port := cfg.Port
	if port == 0 {
		port = 587
	}
	address := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	tlsConfig := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error
	if port == 465 {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fmt.Errorf("email: dial: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("email: %w", err)
	}
	defer client.Close()
	if port != 465 {
		// 服务器支持就加密：smtp.PlainAuth 拒绝在明文连接上向非本机发送密码。
		if offered, _ := client.Extension("STARTTLS"); offered {
			if err := client.StartTLS(tlsConfig); err != nil {
				return fmt.Errorf("email: starttls: %w", err)
			}
		} else if cfg.StartTLS {
			return errors.New("email: server does not offer STARTTLS")
		}
	}
	if cfg.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("email: auth: %w", err)
		}
	}
	if err := client.Mail(cfg.From); err != nil {
		return fmt.Errorf("email: mail from: %w", err)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("email: rcpt %s: %w", recipient, err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("email: data: %w", err)
	}
	if _, err := writer.Write(BuildMIME(cfg.From, recipients, message)); err != nil {
		return fmt.Errorf("email: write: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("email: close: %w", err)
	}
	return client.Quit()
}

// BuildMIME 生成 UTF-8 纯文本邮件，标题按 RFC 2047 编码。
func BuildMIME(from string, to []string, message Message) []byte {
	var buf bytes.Buffer
	body := message.Body
	if message.URL != "" {
		body += "\n\n" + message.URL
	}
	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&buf, "Subject: =?UTF-8?B?%s?=\r\n", base64.StdEncoding.EncodeToString([]byte(message.Title)))
	fmt.Fprintf(&buf, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	buf.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	buf.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	buf.WriteString("\r\n")
	return buf.Bytes()
}

// Settings Hub 的整体配置。
type Settings struct {
	Telegram TelegramConfig
	Email    EmailConfig
	Bark     BarkConfig
	Events   []string
}

// 通道状态（Hub.Channels）。
const (
	ChannelOK            = "ok"
	ChannelDisabled      = "disabled"
	ChannelNotConfigured = "not_configured"
)

// Hub 按设置分发通知；设置可热更新。
type Hub struct {
	mu        sync.RWMutex
	notifiers []Notifier
	events    map[string]bool
	channels  map[string]string
}

// NewHub 创建空 Hub。
func NewHub() *Hub {
	return &Hub{events: map[string]bool{}, channels: map[string]string{}}
}

// Apply 按设置重建通知器列表：启用且配置完整的通道才会发送。
func (h *Hub) Apply(settings Settings) {
	var notifiers []Notifier
	channels := map[string]string{}
	add := func(enabled, ready bool, notifier Notifier) {
		switch {
		case !enabled:
			channels[notifier.Name()] = ChannelDisabled
		case !ready:
			channels[notifier.Name()] = ChannelNotConfigured
		default:
			channels[notifier.Name()] = ChannelOK
			notifiers = append(notifiers, notifier)
		}
	}
	add(settings.Telegram.Enabled, settings.Telegram.ready(), &Telegram{Config: settings.Telegram})
	add(settings.Email.Enabled, settings.Email.ready(), &Email{Config: settings.Email})
	add(settings.Bark.Enabled, settings.Bark.ready(), &Bark{Config: settings.Bark})
	h.set(notifiers, settings.Events, channels)
}

// SetNotifiers 直接注入（测试用），注入的通道状态记为 ok。
func (h *Hub) SetNotifiers(notifiers []Notifier, events []string) {
	channels := map[string]string{}
	for _, notifier := range notifiers {
		channels[notifier.Name()] = ChannelOK
	}
	h.set(notifiers, events, channels)
}

func (h *Hub) set(notifiers []Notifier, events []string, channels map[string]string) {
	allowed := map[string]bool{}
	for _, event := range events {
		allowed[event] = true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifiers = notifiers
	h.events = allowed
	h.channels = channels
}

// Channels 各通道状态（通道名 → ChannelOK / ChannelDisabled / ChannelNotConfigured），供界面展示与测试通知汇报。
func (h *Hub) Channels() map[string]string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return maps.Clone(h.channels)
}

// Notify 过滤事件类型后并发发送，返回各通道结果；未订阅的事件直接忽略。
func (h *Hub) Notify(ctx context.Context, message Message) map[string]error {
	h.mu.RLock()
	notifiers := h.notifiers
	allowed := len(h.events) == 0 || message.Event == "" || h.events[message.Event]
	h.mu.RUnlock()
	results := map[string]error{}
	if !allowed {
		return results
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, notifier := range notifiers {
		wg.Add(1)
		go func(item Notifier) {
			defer wg.Done()
			err := item.Send(ctx, message)
			mu.Lock()
			results[item.Name()] = err
			mu.Unlock()
		}(notifier)
	}
	wg.Wait()
	return results
}
