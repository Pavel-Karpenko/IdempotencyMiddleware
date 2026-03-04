// Package postgres provides a PostgreSQL-backed Store implementation using pgx.
// Apply store/postgres/schema.sql once before use.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	idempotency "github.com/Pavel-Karpenko/IdempotencyMiddleware"
)

// Store is a PostgreSQL-backed implementation of idempotency.Store.
type Store struct {
	db    *pgxpool.Pool
	table string
}

// Option configures a Store.
type Option func(*Store)

// WithTable overrides the table name. Default: "idempotency_keys".
func WithTable(t string) Option { return func(s *Store) { s.table = t } }

// New creates a Store backed by the given connection pool.
// The table must already exist (see schema.sql).
func New(db *pgxpool.Pool, opts ...Option) *Store {
	s := &Store{db: db, table: "idempotency_keys"}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Get implements idempotency.Store.
func (s *Store) Get(ctx context.Context, key string) (*idempotency.Entry, error) {
	const q = `
		SELECT status, status_code, headers, body, request_hash, created_at
		FROM %s
		WHERE key = $1 AND (expires_at IS NULL OR expires_at > NOW())`

	var (
		status      int16
		statusCode  int
		headersJSON []byte
		body        []byte
		requestHash string
		createdAt   time.Time
	)

	err := s.db.QueryRow(ctx, format(q, s.table), key).
		Scan(&status, &statusCode, &headersJSON, &body, &requestHash, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, idempotency.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	var headers map[string][]string
	if len(headersJSON) > 0 {
		if err := json.Unmarshal(headersJSON, &headers); err != nil {
			return nil, err
		}
	}

	return &idempotency.Entry{
		Status:      idempotency.EntryStatus(status),
		StatusCode:  statusCode,
		Headers:     headers,
		Body:        body,
		RequestHash: requestHash,
		CreatedAt:   createdAt,
	}, nil
}

// SetNX implements idempotency.Store using INSERT … ON CONFLICT DO NOTHING.
func (s *Store) SetNX(ctx context.Context, key string, entry *idempotency.Entry, ttl time.Duration) (bool, error) {
	headersJSON, err := json.Marshal(entry.Headers)
	if err != nil {
		return false, err
	}
	expiresAt := expiryTime(ttl)

	const q = `
		INSERT INTO %s (key, status, status_code, headers, body, request_hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (key) DO NOTHING`

	ct, err := s.db.Exec(ctx, format(q, s.table),
		key, int16(entry.Status), entry.StatusCode,
		headersJSON, entry.Body, entry.RequestHash, entry.CreatedAt, expiresAt,
	)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// Set implements idempotency.Store using INSERT … ON CONFLICT DO UPDATE.
func (s *Store) Set(ctx context.Context, key string, entry *idempotency.Entry, ttl time.Duration) error {
	headersJSON, err := json.Marshal(entry.Headers)
	if err != nil {
		return err
	}
	expiresAt := expiryTime(ttl)

	const q = `
		INSERT INTO %s (key, status, status_code, headers, body, request_hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (key) DO UPDATE
		SET status       = EXCLUDED.status,
		    status_code  = EXCLUDED.status_code,
		    headers      = EXCLUDED.headers,
		    body         = EXCLUDED.body,
		    request_hash = EXCLUDED.request_hash,
		    expires_at   = EXCLUDED.expires_at`

	_, err = s.db.Exec(ctx, format(q, s.table),
		key, int16(entry.Status), entry.StatusCode,
		headersJSON, entry.Body, entry.RequestHash, entry.CreatedAt, expiresAt,
	)
	return err
}

// Delete implements idempotency.Store.
func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.db.Exec(ctx, "DELETE FROM "+s.table+" WHERE key = $1", key)
	return err
}

// Close implements idempotency.Store.
func (s *Store) Close() error {
	s.db.Close()
	return nil
}

// Cleanup deletes expired entries and returns the number of rows removed.
// Schedule this periodically (e.g. once per hour) to keep the table compact.
func (s *Store) Cleanup(ctx context.Context) (int64, error) {
	ct, err := s.db.Exec(ctx, "DELETE FROM "+s.table+" WHERE expires_at < NOW()")
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

func expiryTime(ttl time.Duration) *time.Time {
	if ttl <= 0 {
		return nil
	}
	t := time.Now().Add(ttl)
	return &t
}

func format(tmpl, table string) string {
	const placeholder = "%s"
	for i := 0; i < len(tmpl)-len(placeholder)+1; i++ {
		if tmpl[i:i+len(placeholder)] == placeholder {
			return tmpl[:i] + table + tmpl[i+len(placeholder):]
		}
	}
	return tmpl
}
