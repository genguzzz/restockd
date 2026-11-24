package solver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

// ErrChallengeNotFound 挑战不存在或已结束。
var ErrChallengeNotFound = errors.New("challenge not found")

// ErrChallengeCancelled 用户取消。
var ErrChallengeCancelled = errors.New("challenge cancelled")

// Pending 一条等待人工处理的挑战。
type Pending struct {
	ID        string    `json:"id"`
	Challenge Challenge `json:"challenge"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type pendingEntry struct {
	info   Pending
	result chan manualResult
}

type manualResult struct {
	solution Solution
	err      error
}

// ManualBroker 人工兜底：挂起挑战、通知用户，等待界面提交 token / Cookie。
type ManualBroker struct {
	mu      sync.Mutex
	pending map[string]*pendingEntry
	timeout time.Duration
	// OnNew 新挑战挂起时回调（用于推送通知、SSE 广播），不得阻塞。
	OnNew func(Pending)
	// OnDone 挑战结束（已解决、取消或超时）时回调。
	OnDone func(id string)
}

// NewManualBroker timeout 为单个挑战最长等待时间。
func NewManualBroker(timeout time.Duration) *ManualBroker {
	return &ManualBroker{pending: map[string]*pendingEntry{}, timeout: timeout}
}

// Name 实现 Solver。
func (b *ManualBroker) Name() string { return "manual" }

// Supports 人工可处理任意类型。
func (b *ManualBroker) Supports(Kind) bool { return true }

// SetTimeout 运行时调整等待时长。
func (b *ManualBroker) SetTimeout(timeout time.Duration) {
	b.mu.Lock()
	b.timeout = timeout
	b.mu.Unlock()
}

// Solve 阻塞直到用户提交、取消、超时或 ctx 结束。
func (b *ManualBroker) Solve(ctx context.Context, challenge Challenge) (Solution, error) {
	b.mu.Lock()
	timeout := b.timeout
	now := time.Now()
	entry := &pendingEntry{
		info:   Pending{ID: newID(), Challenge: challenge, CreatedAt: now, ExpiresAt: now.Add(timeout)},
		result: make(chan manualResult, 1),
	}
	b.pending[entry.info.ID] = entry
	onNew := b.OnNew
	b.mu.Unlock()
	if onNew != nil {
		onNew(entry.info)
	}
	defer b.remove(entry.info.ID)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-entry.result:
		if result.err == nil && result.solution.SolvedBy == "" {
			result.solution.SolvedBy = b.Name()
		}
		return result.solution, result.err
	case <-timer.C:
		return Solution{}, context.DeadlineExceeded
	case <-ctx.Done():
		return Solution{}, ctx.Err()
	}
}

func (b *ManualBroker) remove(id string) {
	b.mu.Lock()
	_, existed := b.pending[id]
	delete(b.pending, id)
	onDone := b.OnDone
	b.mu.Unlock()
	if existed && onDone != nil {
		onDone(id)
	}
}

// Submit 用户提交结果。
func (b *ManualBroker) Submit(id string, solution Solution) error {
	return b.finish(id, manualResult{solution: solution})
}

// Cancel 用户放弃。
func (b *ManualBroker) Cancel(id string) error {
	return b.finish(id, manualResult{err: ErrChallengeCancelled})
}

func (b *ManualBroker) finish(id string, result manualResult) error {
	b.mu.Lock()
	entry, ok := b.pending[id]
	b.mu.Unlock()
	if !ok {
		return ErrChallengeNotFound
	}
	select {
	case entry.result <- result:
		return nil
	default:
		return ErrChallengeNotFound
	}
}

// List 当前挂起的挑战，按创建时间排序。
func (b *ManualBroker) List() []Pending {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Pending, 0, len(b.pending))
	for _, entry := range b.pending {
		out = append(out, entry.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func newID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
