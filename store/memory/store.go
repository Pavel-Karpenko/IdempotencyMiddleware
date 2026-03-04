// Package memory provides an in-memory Store implementation.
// It is safe for concurrent use and suitable for testing and single-instance deployments.
// It does NOT survive process restarts; use Redis or Postgres for production.
package memory

import (
	"context"
	"sync"
	"time"

	idempotency "github.com/Pavel-Karpenko/IdempotencyMiddleware"
)

// Store is a thread-safe in-memory implementation of idempotency.Store.
type Store struct {
	mu      sync.Mutex
	entries map[string]*item
}

type item struct {
	entry     *idempotency.Entry
	expiresAt time.Time // zero means no expiry
}

// New returns an empty in-memory Store.
func New() *Store {
	return &Store{entries: make(map[string]*item)}
}

// Get implements idempotency.Store.
func (s *Store) Get(_ context.Context, key string) (*idempotency.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	it, ok := s.entries[key]
	if !ok || s.expired(it) {
		return nil, idempotency.ErrNotFound
	}
	return it.entry, nil
}

// SetNX implements idempotency.Store.
func (s *Store) SetNX(_ context.Context, key string, entry *idempotency.Entry, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if it, ok := s.entries[key]; ok && !s.expired(it) {
		return false, nil
	}
	s.entries[key] = s.newItem(entry, ttl)
	return true, nil
}

// Set implements idempotency.Store.
func (s *Store) Set(_ context.Context, key string, entry *idempotency.Entry, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries[key] = s.newItem(entry, ttl)
	return nil
}

// Delete implements idempotency.Store.
func (s *Store) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.entries, key)
	return nil
}

// Close implements idempotency.Store (no-op for in-memory store).
func (s *Store) Close() error { return nil }

// Len returns the number of non-expired entries. Useful in tests.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := 0
	for _, it := range s.entries {
		if !s.expired(it) {
			n++
		}
	}
	return n
}

func (s *Store) expired(it *item) bool {
	return !it.expiresAt.IsZero() && time.Now().After(it.expiresAt)
}

func (s *Store) newItem(entry *idempotency.Entry, ttl time.Duration) *item {
	it := &item{entry: entry}
	if ttl > 0 {
		it.expiresAt = time.Now().Add(ttl)
	}
	return it
}
