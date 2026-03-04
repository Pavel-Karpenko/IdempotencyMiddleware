package idempotency

import (
	"net/http"
	"time"
)

// Config holds configuration for the HTTP idempotency middleware.
type Config struct {
	// Store is the backend for persisting entries. Required.
	Store Store

	// KeyHeader is the request header containing the idempotency key.
	// Default: "Idempotency-Key".
	KeyHeader string

	// TTL controls how long entries are retained in the store.
	// Default: 24h.
	TTL time.Duration

	// Methods lists the HTTP methods for which idempotency is enforced.
	// Default: POST, PATCH.
	Methods []string

	// CheckRequestHash enables detection of key reuse with a different request body.
	// When true, a 422 is returned if the same key is submitted with a different
	// method+path+body combination.
	CheckRequestHash bool

	// ShouldCache decides whether a response should be stored based on HTTP status code.
	// Default: cache all responses with status < 500.
	ShouldCache func(statusCode int) bool

	// ErrorHandler is called when the store returns an unexpected error.
	// Default: responds with 503 Service Unavailable.
	ErrorHandler func(w http.ResponseWriter, r *http.Request, err error)
}

func (c *Config) applyDefaults() Config {
	out := *c
	if out.KeyHeader == "" {
		out.KeyHeader = "Idempotency-Key"
	}
	if out.TTL == 0 {
		out.TTL = 24 * time.Hour
	}
	if len(out.Methods) == 0 {
		out.Methods = []string{http.MethodPost, http.MethodPatch}
	}
	if out.ShouldCache == nil {
		out.ShouldCache = func(code int) bool { return code < 500 }
	}
	if out.ErrorHandler == nil {
		out.ErrorHandler = func(w http.ResponseWriter, r *http.Request, _ error) {
			http.Error(w, "idempotency store unavailable", http.StatusServiceUnavailable)
		}
	}
	return out
}
