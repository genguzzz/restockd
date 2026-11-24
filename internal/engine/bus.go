package engine

import "sync"

// Message 总线消息，对应 SSE 的 event 名与 data。
type Message struct {
	Name    string
	Payload any
}

// Bus 进程内发布订阅，供 SSE 推送使用；慢订阅者丢弃消息而不是阻塞发布方。
type Bus struct {
	mu   sync.RWMutex
	subs map[chan Message]struct{}
}

// NewBus 创建总线。
func NewBus() *Bus {
	return &Bus{subs: map[chan Message]struct{}{}}
}

// Subscribe 返回只读通道与取消函数。
func (b *Bus) Subscribe() (<-chan Message, func()) {
	ch := make(chan Message, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

// Publish 非阻塞广播。
func (b *Bus) Publish(name string, payload any) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- Message{Name: name, Payload: payload}:
		default:
		}
	}
}
