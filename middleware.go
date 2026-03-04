// Package idempotency provides HTTP and gRPC middleware that ensures requests
// are processed exactly once using a client-supplied idempotency key.
//
// Typical usage:
//
//	store := memory.New() // or redis.New(rdb) / postgres.New(pool)
//	m := idempotency.New(idempotency.Config{Store: store})
//	http.Handle("/api/pay", m.Handler(payHandler))
package idempotency

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"time"
)

// Middleware enforces idempotency for HTTP requests.
type Middleware struct {
	cfg Config
}

// New creates a Middleware with the given configuration.
// Panics if cfg.Store is nil.
func New(cfg Config) *Middleware {
	if cfg.Store == nil {
		panic("idempotency: Config.Store must not be nil")
	}
	return &Middleware{cfg: cfg.applyDefaults()}
}

// Handler returns an http.Handler that wraps next with idempotency enforcement.
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only apply to configured methods.
		if !slices.Contains(m.cfg.Methods, r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		key := r.Header.Get(m.cfg.KeyHeader)
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Read body now so we can hash it and restore it for downstream handlers.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			m.cfg.ErrorHandler(w, r, err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		reqHash := ""
		if m.cfg.CheckRequestHash {
			h := sha256.New()
			h.Write([]byte(r.Method))
			h.Write([]byte(r.URL.RequestURI()))
			h.Write(body)
			reqHash = hex.EncodeToString(h.Sum(nil))
		}

		// Atomically acquire the key. If another request already holds it,
		// SetNX returns false and we fall through to replay or 409.
		inProgress := &Entry{
			Status:      EntryStatusInProgress,
			RequestHash: reqHash,
			CreatedAt:   time.Now(),
		}
		acquired, err := m.cfg.Store.SetNX(r.Context(), key, inProgress, m.cfg.TTL)
		if err != nil {
			m.cfg.ErrorHandler(w, r, err)
			return
		}

		if !acquired {
			m.handleExisting(w, r, key, reqHash)
			return
		}

		// Release the in-progress lock if we panic mid-handler.
		defer func() {
			if pv := recover(); pv != nil {
				_ = m.cfg.Store.Delete(r.Context(), key)
				panic(pv)
			}
		}()

		rec := newResponseRecorder(w)
		next.ServeHTTP(rec, r)

		if m.cfg.ShouldCache(rec.code) {
			completed := &Entry{
				Status:      EntryStatusCompleted,
				StatusCode:  rec.code,
				Headers:     cloneHeaders(rec.Header()),
				Body:        rec.body.Bytes(),
				RequestHash: reqHash,
				CreatedAt:   time.Now(),
			}
			_ = m.cfg.Store.Set(r.Context(), key, completed, m.cfg.TTL)
		} else {
			// Non-cacheable response (e.g. 5xx): remove in-progress so client can retry.
			_ = m.cfg.Store.Delete(r.Context(), key)
		}
	})
}

func (m *Middleware) handleExisting(w http.ResponseWriter, r *http.Request, key, reqHash string) {
	existing, err := m.cfg.Store.Get(r.Context(), key)
	if err != nil {
		m.cfg.ErrorHandler(w, r, err)
		return
	}

	if existing.Status == EntryStatusInProgress {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(errorBody{
			Error:          "duplicate request in progress",
			IdempotencyKey: key,
		})
		return
	}

	// Detect key reuse with a different request payload.
	if m.cfg.CheckRequestHash &&
		existing.RequestHash != "" &&
		existing.RequestHash != reqHash {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(errorBody{
			Error:          "idempotency key reused with different request",
			IdempotencyKey: key,
		})
		return
	}

	m.replayResponse(w, existing)
}

func (m *Middleware) replayResponse(w http.ResponseWriter, e *Entry) {
	for k, vs := range e.Headers {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Idempotent-Replayed", "true")
	code := e.StatusCode
	if code == 0 {
		code = http.StatusOK
	}
	w.WriteHeader(code)
	_, _ = w.Write(e.Body)
}

// cloneHeaders copies h, omitting hop-by-hop headers that must not be replayed.
func cloneHeaders(h http.Header) map[string][]string {
	skip := map[string]bool{
		"Connection":          true,
		"Keep-Alive":          true,
		"Transfer-Encoding":   true,
		"Upgrade":             true,
		"Proxy-Authorization": true,
		"Te":                  true,
		"Trailer":             true,
	}
	out := make(map[string][]string, len(h))
	for k, vs := range h {
		if skip[k] {
			continue
		}
		out[k] = append([]string(nil), vs...)
	}
	return out
}

type errorBody struct {
	Error          string `json:"error"`
	IdempotencyKey string `json:"idempotency_key"`
}
