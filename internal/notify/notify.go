// Package notify 通知抽象：Telegram 与 SMTP 邮件，统一由 Hub 按设置分发。
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
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"time"
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

// TelegramConfig Telegram Bot 配置。
type TelegramConfig struct {
	Enabled  bool   `json:"enabled"`
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
	// APIBase 可替换为自建反代，默认 https://api.telegram.org。
	APIBase string `json:"api_base"`
}

// EmailConfig SMTP 配置；端口 465 走隐式 TLS，其余按 StartTLS 选项。
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+t.Config.BotToken+"/sendMessage", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: %w", redactToken(err, t.Config.BotToken))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram: http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// redactToken URL 错误里会带上 bot token，避免泄露到日志与界面。
func redactToken(err error, token string) error {
	if token == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "***"))
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
	recipients := splitAddresses(cfg.To)
	if cfg.Host == "" || cfg.From == "" || len(recipients) == 0 {
		return errors.New("email: host, from and to are required")
	}
	port := cfg.Port
	if port == 0 {
		port = 587
	}
	address := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error
	if port == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
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
	if port != 465 && cfg.StartTLS {
		if err := client.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("email: starttls: %w", err)
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

func splitAddresses(value string) []string {
	var out []string
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if item != "" {
			out = append(out, item)
		}
	}
	return out
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
	fmt.Fprintf(&buf, "Subject: =?UTF-8?B?%s?=\r\n", base64Encode(message.Title))
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
	Events   []string
}

// Hub 按设置分发通知；设置可热更新。
type Hub struct {
	mu        sync.RWMutex
	notifiers []Notifier
	events    map[string]bool
}

// NewHub 创建空 Hub。
func NewHub() *Hub {
	return &Hub{events: map[string]bool{}}
}

// Apply 按设置重建通知器列表。
func (h *Hub) Apply(settings Settings) {
	var notifiers []Notifier
	if settings.Telegram.Enabled && settings.Telegram.BotToken != "" && settings.Telegram.ChatID != "" {
		notifiers = append(notifiers, &Telegram{Config: settings.Telegram})
	}
	if settings.Email.Enabled {
		notifiers = append(notifiers, &Email{Config: settings.Email})
	}
	events := map[string]bool{}
	for _, event := range settings.Events {
		events[event] = true
	}
	h.mu.Lock()
	h.notifiers = notifiers
	h.events = events
	h.mu.Unlock()
}

// SetNotifiers 直接注入（测试用）。
func (h *Hub) SetNotifiers(notifiers []Notifier, events []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifiers = notifiers
	h.events = map[string]bool{}
	for _, event := range events {
		h.events[event] = true
	}
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

func base64Encode(text string) string {
	return base64.StdEncoding.EncodeToString([]byte(text))
}
