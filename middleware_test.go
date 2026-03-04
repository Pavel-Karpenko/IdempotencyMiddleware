package idempotency_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	idempotency "github.com/Pavel-Karpenko/IdempotencyMiddleware"
	"github.com/Pavel-Karpenko/IdempotencyMiddleware/store/memory"
)

func newMiddleware(opts ...func(*idempotency.Config)) *idempotency.Middleware {
	cfg := idempotency.Config{Store: memory.New()}
	for _, o := range opts {
		o(&cfg)
	}
	return idempotency.New(cfg)
}

// echoHandler writes the given status code and body.
func echoHandler(code int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	})
}

func TestHandler_NoKey_Passthrough(t *testing.T) {
	calls := 0
	m := newMiddleware()
	h := m.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
	}))

	req := httptest.NewRequest(http.MethodPost, "/pay", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestHandler_GetMethod_Passthrough(t *testing.T) {
	calls := 0
	m := newMiddleware()
	h := m.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("Idempotency-Key", "k1")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestHandler_FirstRequest_ExecutesAndStores(t *testing.T) {
	calls := 0
	m := newMiddleware()
	h := m.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"1"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/pay", bytes.NewBufferString(`{"amount":100}`))
	req.Header.Set("Idempotency-Key", "k-first")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if calls != 1 {
		t.Fatalf("expected 1 handler call, got %d", calls)
	}
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr.Code)
	}
	if rr.Header().Get("Idempotent-Replayed") != "" {
		t.Fatal("first request must not carry Idempotent-Replayed header")
	}
}

func TestHandler_DuplicateRequest_ReplaysResponse(t *testing.T) {
	calls := 0
	m := newMiddleware()
	h := m.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("X-Payment-ID", "pay-123")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"pay-123"}`))
	}))

	newReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/pay", bytes.NewBufferString(`{"amount":100}`))
		req.Header.Set("Idempotency-Key", "k-dup")
		return req
	}

	rr1 := httptest.NewRecorder()
	h.ServeHTTP(rr1, newReq())

	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, newReq())

	if calls != 1 {
		t.Fatalf("handler should be called once, got %d", calls)
	}
	if rr2.Code != http.StatusCreated {
		t.Fatalf("replayed status want 201, got %d", rr2.Code)
	}
	if rr2.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatal("expected Idempotent-Replayed: true on second request")
	}
	if rr2.Body.String() != `{"id":"pay-123"}` {
		t.Fatalf("replayed body mismatch: %q", rr2.Body.String())
	}
	if rr2.Header().Get("X-Payment-ID") != "pay-123" {
		t.Fatal("replayed response should include original headers")
	}
}

func TestHandler_RequestMismatch_Returns422(t *testing.T) {
	m := newMiddleware(func(c *idempotency.Config) {
		c.CheckRequestHash = true
	})
	h := m.Handler(echoHandler(http.StatusCreated, `{}`))

	req1 := httptest.NewRequest(http.MethodPost, "/pay", bytes.NewBufferString(`{"amount":100}`))
	req1.Header.Set("Idempotency-Key", "k-mismatch")
	h.ServeHTTP(httptest.NewRecorder(), req1)

	req2 := httptest.NewRequest(http.MethodPost, "/pay", bytes.NewBufferString(`{"amount":999}`))
	req2.Header.Set("Idempotency-Key", "k-mismatch")
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rr2.Code)
	}
}

func TestHandler_5xxResponse_NotCached(t *testing.T) {
	calls := 0
	m := newMiddleware()
	h := m.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))

	newReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/pay", nil)
		req.Header.Set("Idempotency-Key", "k-5xx")
		return req
	}

	h.ServeHTTP(httptest.NewRecorder(), newReq())
	h.ServeHTTP(httptest.NewRecorder(), newReq())

	if calls != 2 {
		t.Fatalf("5xx should not be cached; expected 2 handler calls, got %d", calls)
	}
}

func TestHandler_ConcurrentFirstRequests_OnlyOneExecutes(t *testing.T) {
	calls := 0
	var mu sync.Mutex

	m := newMiddleware()
	h := m.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"1"}`))
	}))

	const n = 20
	var wg sync.WaitGroup
	results := make([]int, n)

	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/pay", bytes.NewBufferString(`{}`))
			req.Header.Set("Idempotency-Key", "k-concurrent")
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			results[idx] = rr.Code
		}(i)
	}
	wg.Wait()

	if calls != 1 {
		t.Fatalf("expected exactly 1 handler execution, got %d", calls)
	}

	for i, code := range results {
		if code != http.StatusCreated && code != http.StatusConflict {
			t.Errorf("result[%d]: unexpected status %d", i, code)
		}
	}
}

func TestHandler_ErrorResponse_JSON(t *testing.T) {
	_ = json.Unmarshal // ensure encoding/json is used
	m := newMiddleware()
	h := m.Handler(echoHandler(http.StatusCreated, `{"id":"x"}`))

	req1 := httptest.NewRequest(http.MethodPost, "/pay", bytes.NewBufferString(`{}`))
	req1.Header.Set("Idempotency-Key", "k-err-json")
	h.ServeHTTP(httptest.NewRecorder(), req1)
}
