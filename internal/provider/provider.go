// Package provider 定义主机商适配器的统一接口与注册表。
//
// 新增一个主机商 = 新建子包实现 Provider（必选）与 Orderer / SessionChecker / Authenticator（可选），并在 init 中 Register。
// 同一种站点系统（如 WHMCS）只需实现一次，不同商家通过 Site.Config 区分（如商品组、主题差异）。
package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/fetch"
	"github.com/genguzzz/restockd/internal/solver"
)

// ErrSessionExpired 账号会话失效：有托管凭据时引擎会自动重新登录，否则需要重新粘贴 Cookie。
var ErrSessionExpired = errors.New("session expired")

// ErrOrderRejected 站点明确拒绝了这笔订单（邀请制、配置校验不过、结算页报错等），重试也不会成功。
var ErrOrderRejected = errors.New("order rejected by site")

// ErrLoginFailed 站点明确拒绝了登录（账号或密码错误、账号被锁等），不应重试。
var ErrLoginFailed = errors.New("login failed")

// ErrOutOfStock 下单时站点返回无货。
var ErrOutOfStock = errors.New("out of stock")

// ErrCouponInvalid 站点明确拒绝了优惠码；为避免原价成交，锁单中止。
var ErrCouponInvalid = errors.New("coupon rejected")

// ErrSiteMaintenance 站点本身停机维护（如 WHMCS「Down for Maintenance」），只能等站点恢复。
var ErrSiteMaintenance = errors.New("site under maintenance")

// ErrNotSupported 该 Provider 不支持此能力。
var ErrNotSupported = errors.New("not supported by provider")

// FieldType 配置字段类型，前端据此渲染表单。
type FieldType string

const (
	FieldText     FieldType = "text"
	FieldList     FieldType = "list"
	FieldBool     FieldType = "bool"
	FieldPassword FieldType = "password"
)

// Field Site.Config 的一个配置项描述。
type Field struct {
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	Type        FieldType `json:"type"`
	Required    bool      `json:"required"`
	Default     string    `json:"default,omitempty"`
	Placeholder string    `json:"placeholder,omitempty"`
	Help        string    `json:"help,omitempty"`
}

// Capabilities Provider 能力声明。
type Capabilities struct {
	Catalog      bool `json:"catalog"`
	Coupon       bool `json:"coupon"`
	Order        bool `json:"order"`
	SessionCheck bool `json:"session_check"`
	// Login 支持用托管的账号密码自动登录（实现 Authenticator）。
	Login bool `json:"login"`
	// Cancel 支持取消未付款订单（实现 Canceler；具体订单仍可能被站点拒绝）。
	Cancel bool `json:"cancel"`
	// StockNeedsLogin 查询库存是否必须登录（决定监控时是否带账号会话）。
	StockNeedsLogin bool `json:"stock_needs_login"`
}

// Meta Provider 元信息。
type Meta struct {
	Kind        string       `json:"kind"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Caps        Capabilities `json:"capabilities"`
	// MinInterval 站点能承受的最短轮询间隔；用户设置更短会被抬高到这里。
	MinInterval     time.Duration    `json:"-"`
	DefaultInterval time.Duration    `json:"-"`
	DefaultFetch    domain.FetchMode `json:"default_fetch_mode"`
	ConfigFields    []Field          `json:"config_fields"`
	// Captcha 下单环节可能出现的验证类型，前端提示用户配置对应 Solver。
	Captcha []solver.Kind `json:"captcha,omitempty"`
	// RateLimitCooldown 站点 429 未带 Retry-After 时出口的冷却时间，0 为出口层默认（1 小时）。
	RateLimitCooldown time.Duration `json:"-"`
}

// Env 一次调用的运行环境，由引擎组装。
type Env struct {
	Site    domain.Site
	Client  fetch.Client
	Session *fetch.Session
	Account *domain.Account
	Solver  solver.Solver
	Log     *slog.Logger
}

// AccountID 当前账号 ID，匿名为空。
func (env Env) AccountID() string {
	if env.Account == nil {
		return ""
	}
	return env.Account.ID
}

// proxyHinter 能报告当前出口代理的 Client（egress.Client）：验证码要在提交表单的同一出口上求解。
type proxyHinter interface {
	ProxyFor(rawURL string) string
}

// Solve 交给 env.Solver 求解验证码：补上站点、账号与当前出口代理，求解结果附带的 Cookie / UA 写回会话。
func (env Env) Solve(ctx context.Context, challenge solver.Challenge) (solver.Solution, error) {
	if env.Solver == nil {
		return solver.Solution{}, fmt.Errorf("solve %s: no solver configured", challenge.Kind)
	}
	challenge.SiteID = env.Site.ID
	challenge.AccountID = env.AccountID()
	if hint, ok := env.Client.(proxyHinter); ok && challenge.Proxy == "" {
		challenge.Proxy = hint.ProxyFor(challenge.PageURL)
	}
	solution, err := env.Solver.Solve(ctx, challenge)
	if err != nil {
		return solver.Solution{}, fmt.Errorf("solve %s: %w", challenge.Kind, err)
	}
	if env.Session != nil {
		if len(solution.Cookies) > 0 {
			env.Session.SetCookies(solution.Cookies)
		}
		env.Session.SetUserAgent(solution.UserAgent)
	}
	return solution, nil
}

// SplitList 解析 FieldList 配置值：逗号、分号、空白（含换行）分隔，忽略空项。
func SplitList(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || unicode.IsSpace(r) })
}

// Provider 必选能力：拉取商品目录（含库存、价格、配置项）。
type Provider interface {
	Meta() Meta
	Catalog(ctx context.Context, env Env) (domain.Catalog, error)
}

// Orderer 可选能力：算价（含优惠码校验）与下单锁单。
type Orderer interface {
	Quote(ctx context.Context, env Env, spec domain.OrderSpec) (domain.Quote, error)
	PlaceOrder(ctx context.Context, env Env, spec domain.OrderSpec) (domain.OrderResult, error)
}

// Canceler 可选能力：取消已生成的未付款订单 / 账单。站点不允许客户自助取消时返回包装了
// ErrNotSupported 的错误并说明原因（如 WHMCS 待开通服务通常由商家按规则自动作废）。
type Canceler interface {
	CancelOrder(ctx context.Context, env Env, result domain.OrderResult) error
}

// SessionInfo 会话检查结果。
type SessionInfo struct {
	Identity string        `json:"identity"`
	Balance  *domain.Money `json:"balance,omitempty"`
}

// SessionChecker 可选能力：校验账号 Cookie 是否仍有效。
type SessionChecker interface {
	CheckSession(ctx context.Context, env Env) (SessionInfo, error)
}

// Authenticator 可选能力：用托管凭据登录。
//
// 实现约定：所有请求经 env.Client / env.Session，登录成功后 Cookie 留在 env.Session（引擎负责落库）；
// 遇到验证码交给 env.Solver；账号密码被拒返回 ErrLoginFailed（包装站点原文）；
// 返回的 SessionInfo 尽量带上身份与余额。引擎保证同一账号的 Login 串行执行。
type Authenticator interface {
	Login(ctx context.Context, env Env, creds domain.Credentials) (SessionInfo, error)
}

// Factory 依据站点配置构造 Provider 实例。
type Factory func(site domain.Site) (Provider, error)

type registration struct {
	meta    Meta
	factory Factory
}

var (
	registryMu sync.RWMutex
	registry   = map[string]registration{}
)

// Register 在子包 init 中调用。
func Register(meta Meta, factory Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[meta.Kind]; exists {
		panic("provider: duplicate kind " + meta.Kind)
	}
	registry[meta.Kind] = registration{meta: meta, factory: factory}
}

// New 根据 Site.Kind 构造实例。
func New(site domain.Site) (Provider, error) {
	registryMu.RLock()
	reg, ok := registry[site.Kind]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("provider: unknown kind %q", site.Kind)
	}
	return reg.factory(site)
}

// Lookup 查询已注册 Kind 的元信息。
func Lookup(kind string) (Meta, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	reg, ok := registry[kind]
	return reg.meta, ok
}

// List 所有已注册 Provider 元信息，按 Kind 排序。
func List() []Meta {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Meta, 0, len(registry))
	for _, reg := range registry {
		out = append(out, reg.meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// EffectiveInterval 合并用户设置与 Provider 限制。
func EffectiveInterval(meta Meta, site domain.Site) time.Duration {
	interval := time.Duration(site.IntervalSec) * time.Second
	if interval <= 0 {
		interval = meta.DefaultInterval
	}
	if interval < meta.MinInterval {
		interval = meta.MinInterval
	}
	return interval
}
