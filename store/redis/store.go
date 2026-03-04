// Package redis provides a Redis-backed Store implementation using go-redis.
// Keys are stored as JSON with an optional prefix.
// SetNX uses a Lua script to guarantee atomic compare-and-set semantics.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"

	idempotency "github.com/Pavel-Karpenko/IdempotencyMiddleware"
)

// Store is a Redis-backed implementation of idempotency.Store.
type Store struct {
	client goredis.Cmdable
	prefix string
}

// Option configures a Store.
type Option func(*Store)

// WithPrefix sets the Redis key prefix. Default: "idm:".
func WithPrefix(p string) Option { return func(s *Store) { s.prefix = p } }

// New creates a Store backed by the given Redis client.
// client can be *goredis.Client, *goredis.ClusterClient, or any goredis.Cmdable.
func New(client goredis.Cmdable, opts ...Option) *Store {
	s := &Store{client: client, prefix: "idm:"}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *Store) rk(key string) string { return s.prefix + key }

// Get implements idempotency.Store.
func (s *Store) Get(ctx context.Context, key string) (*idempotency.Entry, error) {
	b, err := s.client.Get(ctx, s.rk(key)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, idempotency.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var e idempotency.Entry
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// setNXScript atomically sets key to value with EX ttl only if it does not exist.
// Returns 1 if the key was set, 0 if it already existed.
var setNXScript = goredis.NewScript(`
local result = redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2], 'NX')
if result then return 1 end
return 0
`)

// SetNX implements idempotency.Store.
func (s *Store) SetNX(ctx context.Context, key string, entry *idempotency.Entry, ttl time.Duration) (bool, error) {
	b, err := json.Marshal(entry)
	if err != nil {
		return false, err
	}
	ttlSec := int64(ttl.Seconds())
	if ttlSec <= 0 {
		ttlSec = 86400
	}
	result, err := setNXScript.Run(ctx, s.client, []string{s.rk(key)}, string(b), ttlSec).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// Set implements idempotency.Store.
func (s *Store) Set(ctx context.Context, key string, entry *idempotency.Entry, ttl time.Duration) error {
	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return s.client.Set(ctx, s.rk(key), b, ttl).Err()
}

// Delete implements idempotency.Store.
func (s *Store) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, s.rk(key)).Err()
}

// Close implements idempotency.Store (no-op; client lifecycle is managed by caller).
func (s *Store) Close() error { return nil }
