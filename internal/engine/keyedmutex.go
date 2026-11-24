package engine

import "sync"

// keyedMutex 按键懒建的互斥锁（每个关注 / 账号一把）；零值可用。
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// Get 返回 key 对应的锁，不存在则新建。
func (k *keyedMutex) Get(key string) *sync.Mutex {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.locks == nil {
		k.locks = map[string]*sync.Mutex{}
	}
	lock, ok := k.locks[key]
	if !ok {
		lock = &sync.Mutex{}
		k.locks[key] = lock
	}
	return lock
}

// Delete 实体删除后丢弃其锁。
func (k *keyedMutex) Delete(key string) {
	k.mu.Lock()
	delete(k.locks, key)
	k.mu.Unlock()
}
