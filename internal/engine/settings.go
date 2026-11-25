package engine

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/egress"
	"github.com/genguzzz/restockd/internal/notify"
)

// SettingsKey 设置在 settings 表中的键。
const SettingsKey = "app"

// SecretMask 密钥字段对外展示的占位符；写回时原样传回表示不修改。
const SecretMask = "******"

const (
	defaultIdleInterval   = time.Hour
	defaultOrderMarkTTL   = 12 * time.Hour
	defaultViewerInterval = 5 * time.Minute
	defaultManualTimeout  = 5 * time.Minute
)

// Settings 全局设置。
type Settings struct {
	Telegram     notify.TelegramConfig `json:"telegram"`
	Email        notify.EmailConfig    `json:"email"`
	Bark         notify.BarkConfig     `json:"bark"`
	Solver       SolverSettings        `json:"solver"`
	Polling      PollingSettings       `json:"polling"`
	Egress       egress.Settings       `json:"egress"`
	Orders       OrderSettings         `json:"orders"`
	NotifyEvents []string              `json:"notify_events"`
}

// SolverSettings 验证码求解设置。
type SolverSettings struct {
	FlareSolverrURL  string `json:"flaresolverr_url"`
	ExternalURL      string `json:"external_url"`
	ExternalToken    string `json:"external_token"`
	ManualTimeoutSec int    `json:"manual_timeout_sec"`
}

func (s SolverSettings) manualTimeout() time.Duration {
	if s.ManualTimeoutSec > 0 {
		return time.Duration(s.ManualTimeoutSec) * time.Second
	}
	return defaultManualTimeout
}

// PollingSettings 无关注站点的降频策略；0 表示使用默认值。
type PollingSettings struct {
	IdleIntervalSec   int `json:"idle_interval_sec"`
	ViewerIntervalSec int `json:"viewer_interval_sec"`
}

func (p PollingSettings) idle() time.Duration {
	if p.IdleIntervalSec > 0 {
		return time.Duration(p.IdleIntervalSec) * time.Second
	}
	return defaultIdleInterval
}

func (p PollingSettings) viewer() time.Duration {
	if p.ViewerIntervalSec > 0 {
		return time.Duration(p.ViewerIntervalSec) * time.Second
	}
	return defaultViewerInterval
}

// OrderSettings 下单相关；0 表示使用默认值。
type OrderSettings struct {
	// MarkHours 商品上「已锁单 / 已试下单 / 上次失败」标记的有效期，同时是手动真实下单的防重复窗口。
	MarkHours int `json:"mark_hours"`
}

// MarkTTL 下单标记有效期（默认 12 小时）。
func (o OrderSettings) MarkTTL() time.Duration {
	if o.MarkHours > 0 {
		return time.Duration(o.MarkHours) * time.Hour
	}
	return defaultOrderMarkTTL
}

// DefaultSettings 初始设置。
func DefaultSettings() Settings {
	return Settings{
		Email:        notify.EmailConfig{Port: 587, StartTLS: true},
		Solver:       SolverSettings{FlareSolverrURL: "http://127.0.0.1:8191", ManualTimeoutSec: int(defaultManualTimeout / time.Second)},
		Polling:      PollingSettings{IdleIntervalSec: int(defaultIdleInterval / time.Second), ViewerIntervalSec: int(defaultViewerInterval / time.Second)},
		Orders:       OrderSettings{MarkHours: int(defaultOrderMarkTTL / time.Hour)},
		NotifyEvents: []string{string(domain.EventRestock), string(domain.EventOrder), string(domain.EventChallenge), string(domain.EventAccount)},
	}
}

// Redacted 对外展示的副本：密钥打码，列表字段不为 null。
func (s Settings) Redacted() Settings {
	s.Telegram.BotToken = mask(s.Telegram.BotToken)
	s.Email.Password = mask(s.Email.Password)
	s.Bark.DeviceURLs = mask(s.Bark.DeviceURLs)
	s.Solver.ExternalToken = mask(s.Solver.ExternalToken)
	proxies := make([]egress.Proxy, len(s.Egress.Proxies))
	for i, proxy := range s.Egress.Proxies {
		proxy.URL = redactProxyURL(proxy.URL)
		proxies[i] = proxy
	}
	s.Egress.Proxies = proxies
	s.Egress.Default = append([]string{}, s.Egress.Default...)
	s.NotifyEvents = append([]string{}, s.NotifyEvents...)
	return s
}

// withSecretsFrom 把传回的打码字段还原为 current 中的原值（代理地址只还原密码）。
func (s Settings) withSecretsFrom(current Settings) Settings {
	s.Telegram.BotToken = keep(s.Telegram.BotToken, current.Telegram.BotToken)
	s.Email.Password = keep(s.Email.Password, current.Email.Password)
	s.Bark.DeviceURLs = keep(s.Bark.DeviceURLs, current.Bark.DeviceURLs)
	s.Solver.ExternalToken = keep(s.Solver.ExternalToken, current.Solver.ExternalToken)
	stored := map[string]string{}
	for _, proxy := range current.Egress.Proxies {
		stored[proxy.ID] = proxy.URL
	}
	proxies := make([]egress.Proxy, len(s.Egress.Proxies))
	for i, proxy := range s.Egress.Proxies {
		proxy.URL = mergeProxyURL(proxy.URL, stored[proxy.ID])
		proxies[i] = proxy
	}
	s.Egress.Proxies = proxies
	return s
}

// Validate 代理 ID 必填、唯一且不能是内置的 direct。
func (s Settings) Validate() error {
	seen := map[string]bool{}
	for _, proxy := range s.Egress.Proxies {
		id := strings.TrimSpace(proxy.ID)
		if id == "" || id == egress.Direct {
			return invalidf("proxy id is required and must not be %q", egress.Direct)
		}
		if seen[id] {
			return invalidf("duplicate proxy id %q", id)
		}
		seen[id] = true
	}
	return nil
}

func mask(value string) string {
	if value == "" {
		return ""
	}
	return SecretMask
}

func keep(incoming, current string) string {
	if incoming == SecretMask {
		return current
	}
	return incoming
}

// redactProxyURL 隐去代理地址里的密码：socks5://user:******@host:port。
func redactProxyURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return raw
	}
	if _, has := parsed.User.Password(); !has {
		return raw
	}
	return strings.Replace(raw, parsed.User.String()+"@", parsed.User.Username()+":"+SecretMask+"@", 1)
}

// mergeProxyURL 传回的地址密码为打码值时换回 stored 中的密码，其余部分（主机、端口、用户名）以传回的为准。
func mergeProxyURL(incoming, stored string) string {
	parsed, err := url.Parse(incoming)
	if err != nil || parsed.User == nil {
		return incoming
	}
	if password, has := parsed.User.Password(); !has || password != SecretMask {
		return incoming
	}
	parsed.User = url.User(parsed.User.Username())
	if old, err := url.Parse(stored); err == nil && old.User != nil {
		if password, has := old.User.Password(); has {
			parsed.User = url.UserPassword(parsed.User.Username(), password)
		}
	}
	return parsed.String()
}

// Settings 当前设置副本（含密钥，对外请用 Redacted）。
func (e *Engine) Settings() Settings {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.settings
}

// SaveSettings 还原打码密钥、校验、持久化并应用；删除的代理同时从各站点出口列表中移除。
func (e *Engine) SaveSettings(ctx context.Context, incoming Settings) error {
	settings := incoming.withSecretsFrom(e.Settings())
	if err := settings.Validate(); err != nil {
		return err
	}
	if err := e.store.PutSetting(ctx, SettingsKey, settings); err != nil {
		return err
	}
	e.ApplySettings(settings)
	return e.pruneSiteEgress(ctx, settings.Egress)
}

// pruneSiteEgress 去掉站点出口列表里已不存在的出口 ID（否则站点会一直报 ErrNoEgress）。
func (e *Engine) pruneSiteEgress(ctx context.Context, settings egress.Settings) error {
	known := map[string]bool{egress.Direct: true}
	for _, proxy := range settings.Proxies {
		known[proxy.ID] = true
	}
	e.docMu.Lock()
	defer e.docMu.Unlock()
	sites, err := e.store.ListSites(ctx)
	if err != nil {
		return err
	}
	for _, site := range sites {
		pruned := slices.DeleteFunc(slices.Clone(site.Egress), func(id string) bool { return !known[id] })
		if len(pruned) == len(site.Egress) {
			continue
		}
		site.Egress = pruned
		if err := e.store.SaveSite(ctx, site); err != nil {
			return err
		}
		e.bus.Publish("site", site)
	}
	return nil
}
