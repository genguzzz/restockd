package engine

import (
	"errors"
	"fmt"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
)

var (
	// ErrInvalid 请求参数校验失败（API 映射为 400）。
	ErrInvalid = errors.New("invalid input")
	// ErrUpstream 站点侧失败：Provider、网络、登录（API 映射为 502）。
	ErrUpstream = errors.New("upstream failure")
	// ErrNoAccount 关注规则没有绑定账号。
	ErrNoAccount = errors.New("watch has no account")
	// ErrOrderInFlight 同一关注已有锁单在执行。
	ErrOrderInFlight = errors.New("order already in flight for this watch")
	// ErrNoOrderer Provider 不支持下单。
	ErrNoOrderer = errors.New("provider does not support ordering")
	// ErrNoAuthenticator 站点类型不支持账号密码登录。
	ErrNoAuthenticator = errors.New("provider does not support password login")
	// ErrNoCredentials 账号没有托管完整的用户名与密码。
	ErrNoCredentials = errors.New("account has no username/password")
)

// ErrDuplicateOrder 同一站点商品 + 账号 24 小时内已成功锁单（手动下单需 force 才能再下）。
var ErrDuplicateOrder = errors.New("order already placed recently")

// DuplicateOrderError 带上已存在的订单，errors.Is(err, ErrDuplicateOrder) 为真。
type DuplicateOrderError struct{ Existing domain.Order }

func (e *DuplicateOrderError) Error() string {
	return fmt.Sprintf("%s: order %s at %s (pass force to order again)", ErrDuplicateOrder, e.Existing.ID, e.Existing.CreatedAt.Format(time.RFC3339))
}

func (e *DuplicateOrderError) Is(target error) bool { return target == ErrDuplicateOrder }

// tagged 给错误打上分类标记（errors.Is 可识别），不改变错误信息。
type tagged struct{ err, tag error }

func (t tagged) Error() string   { return t.err.Error() }
func (t tagged) Unwrap() []error { return []error{t.err, t.tag} }

// invalidf 构造 ErrInvalid 类错误。
func invalidf(format string, args ...any) error {
	return tagged{err: fmt.Errorf(format, args...), tag: ErrInvalid}
}

// upstream 把 Provider 调用的失败标记为 ErrUpstream；nil 原样返回。
func upstream(err error) error {
	if err == nil || errors.Is(err, ErrUpstream) {
		return err
	}
	return tagged{err: err, tag: ErrUpstream}
}
