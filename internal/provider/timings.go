package provider

import (
	"fmt"
	"strings"
	"time"
)

// Timings 下单各步骤耗时，附在结果说明里，便于定位慢在哪一步（站点响应、验证码、提交）。
type Timings struct {
	last  time.Time
	parts []string
}

// NewTimings 从现在开始计时。
func NewTimings() *Timings { return &Timings{last: time.Now()} }

// Lap 记录自上次以来的一段。
func (t *Timings) Lap(name string) {
	now := time.Now()
	t.parts = append(t.parts, fmt.Sprintf("%s=%.1fs", name, now.Sub(t.last).Seconds()))
	t.last = now
}

// Note 记录一个不计入顺序耗时的值（如并行完成的验证码求解）。
func (t *Timings) Note(name string, d time.Duration) {
	if d > 0 {
		t.parts = append(t.parts, fmt.Sprintf("%s=%.1fs", name, d.Seconds()))
	}
}

// String 形如 "timings: cart=9.8s captcha_wait=0.0s captcha_parallel=8.4s submit=3.1s"。
func (t *Timings) String() string { return "timings: " + strings.Join(t.parts, " ") }

// Append 拼到已有说明后面。
func (t *Timings) Append(message string) string {
	if message == "" {
		return t.String()
	}
	return message + "; " + t.String()
}
