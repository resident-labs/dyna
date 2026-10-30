// Package memo provides bounded revision-keyed caches.
package memo

import "sync"

type entry[R, V any] struct {
	rev   R
	value V
}

// Memo keeps one value per key with the revision it was built from.
type Memo[K comparable, R, V any] struct {
	mu      sync.Mutex
	entries map[K]entry[R, V]
	max     int
	same    func(R, R) bool
	loads   int
}

func New[K, R comparable, V any](max int) *Memo[K, R, V] {
	return NewFunc[K, R, V](max, func(a, b R) bool { return a == b })
}
func NewFunc[K comparable, R, V any](max int, same func(a, b R) bool) *Memo[K, R, V] {
	return &Memo[K, R, V]{max: max, same: same}
}
func (m *Memo[K, R, V]) Lookup(key K, rev R) (V, bool) {
	m.mu.Lock()
	e, ok := m.entries[key]
	if ok {
		ok = m.same(e.rev, rev)
	}
	m.mu.Unlock()
	return e.value, ok
}
func (m *Memo[K, R, V]) Put(key K, rev R, v V) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loads++
	if m.max <= 0 {
		return
	}
	if m.entries == nil {
		m.entries = make(map[K]entry[R, V])
	}
	if _, ok := m.entries[key]; !ok && len(m.entries) >= m.max {
		for old := range m.entries {
			delete(m.entries, old)
			break
		}
	}
	m.entries[key] = entry[R, V]{rev, v}
}
func (m *Memo[K, R, V]) Get(key K, rev R, load func() (V, error)) (V, error) {
	if v, ok := m.Lookup(key, rev); ok {
		return v, nil
	}
	v, err := load()
	if err != nil {
		m.mu.Lock()
		m.loads++
		m.mu.Unlock()
		return v, err
	}
	m.Put(key, rev, v)
	return v, nil
}
func (m *Memo[K, R, V]) Delete(key K) { m.mu.Lock(); delete(m.entries, key); m.mu.Unlock() }
func (m *Memo[K, R, V]) Loads() int   { m.mu.Lock(); defer m.mu.Unlock(); return m.loads }
