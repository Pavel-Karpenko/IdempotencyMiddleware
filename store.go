package idempotency

import (
	"context"
	"time"
)

// EntryStatus represents the lifecycle state of an idempotency entry.
type EntryStatus int8

const (
	// EntryStatusInProgress means the original request is still being processed.
	EntryStatusInProgress EntryStatus = iota
	// EntryStatusCompleted means the original request has finished and the response is stored.
	EntryStatusCompleted
)

// Entry holds the recorded response for an idempotency key.
type Entry struct {
	Status      EntryStatus         `json:"status"`
	StatusCode  int                 `json:"status_code,omitempty"`
	Headers     map[string][]string `json:"headers,omitempty"`
	Body        []byte              `json:"body,omitempty"`
	RequestHash string              `json:"request_hash,omitempty"`
	CreatedAt   time.Time           `json:"created_at"`
}

// Store is the backend interface for persisting idempotency entries.
// All implementations must be safe for concurrent use.
type Store interface {
	// Get retrieves the entry for key.
	// Returns ErrNotFound if the key does not exist or has expired.
	Get(ctx context.Context, key string) (*Entry, error)

	// SetNX stores entry only if key does not already exist.
	// Returns (true, nil) if the entry was stored.
	// Returns (false, nil) if the key already existed (no-op).
	SetNX(ctx context.Context, key string, entry *Entry, ttl time.Duration) (bool, error)

	// Set unconditionally stores or updates entry.
	Set(ctx context.Context, key string, entry *Entry, ttl time.Duration) error

	// Delete removes the entry for key. No-op if the key does not exist.
	Delete(ctx context.Context, key string) error

	// Close releases any resources held by the store.
	Close() error
}
