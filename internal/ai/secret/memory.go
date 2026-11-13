package secret

import (
	"context"
	"sync"
)

// Memory is the test hook required by SPEC-02 §5.4. It exists only for
// injection in tests and acceptance fixtures; release wiring must use NewOS.
// Setting Unavailable makes every operation fail with
// ErrSecretStoreUnavailable to exercise the key-storage outage paths.
type Memory struct {
	mu          sync.Mutex
	entries     map[string][]byte
	Unavailable bool
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{entries: map[string][]byte{}}
}

var _ Store = (*Memory)(nil)

// Set copies the secret into the map.
func (m *Memory) Set(_ context.Context, ref string, secret []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Unavailable {
		return ErrSecretStoreUnavailable
	}
	if m.entries == nil {
		m.entries = map[string][]byte{}
	}
	buf := make([]byte, len(secret))
	copy(buf, secret)
	m.entries[ref] = buf
	return nil
}

// Get returns a copy of the stored secret.
func (m *Memory) Get(_ context.Context, ref string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Unavailable {
		return nil, ErrSecretStoreUnavailable
	}
	buf, ok := m.entries[ref]
	if !ok {
		return nil, ErrSecretNotFound
	}
	out := make([]byte, len(buf))
	copy(out, buf)
	return out, nil
}

// Delete removes the entry; deleting an absent ref is an idempotent success.
func (m *Memory) Delete(_ context.Context, ref string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Unavailable {
		return ErrSecretStoreUnavailable
	}
	delete(m.entries, ref)
	return nil
}

// Len reports the number of stored entries (test assertions only; the OS
// stores expose no enumeration by design).
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// Has reports whether a ref currently holds a value (test assertions only).
func (m *Memory) Has(ref string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.entries[ref]
	return ok
}
