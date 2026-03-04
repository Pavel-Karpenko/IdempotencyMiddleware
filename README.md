# idempotency-middleware

A Go library that guarantees **exactly-once processing** of HTTP and gRPC requests using a client-supplied `Idempotency-Key`. Drop-in middleware for any `net/http` handler or gRPC server — no framework lock-in.

Built for systems where duplicate execution causes real harm: payment processing, medical record writes, order creation, financial ledger mutations.

[![CI](https://github.com/Pavel-Karpenko/IdempotencyMiddleware/actions/workflows/ci.yml/badge.svg)](https://github.com/Pavel-Karpenko/IdempotencyMiddleware/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/Pavel-Karpenko/IdempotencyMiddleware.svg)](https://pkg.go.dev/github.com/Pavel-Karpenko/IdempotencyMiddleware)
[![Go 1.22+](https://img.shields.io/badge/go-1.22+-blue.svg)](https://golang.org/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

---

## The problem

A user clicks **"Pay"**. The request reaches your server, but the network drops before the response arrives. The client retries. Your server processes the payment **twice**.

`idempotency-middleware` solves this by storing the first response and replaying it on every subsequent request that carries the same key — without touching your business logic.

---

## How it works

| Scenario | Response |
|---|---|
| First request with key `K` | Handler executes; response stored |
| Duplicate request with key `K` | Stored response returned, handler **not** called (`Idempotent-Replayed: true`) |
| Concurrent duplicate while first is in-flight | `409 Conflict` |
| Same key, different request body (`CheckRequestHash: true`) | `422 Unprocessable Entity` |
| Handler returns 5xx | Entry deleted; client may safely retry |
| Request without a key | Passed through unchanged |

---

## Installation

```bash
go get github.com/Pavel-Karpenko/IdempotencyMiddleware
```

Pick a storage backend:

```bash
go get github.com/Pavel-Karpenko/IdempotencyMiddleware/store/redis    # Redis (recommended for production)
go get github.com/Pavel-Karpenko/IdempotencyMiddleware/store/postgres # PostgreSQL (audit trail)
```

---

## Examples

### Example 1 — Payment API with Redis

A minimal but production-ready payment endpoint. Two concurrent requests with the same key result in exactly one charge.

```go
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	idempotency "github.com/Pavel-Karpenko/IdempotencyMiddleware"
	idmredis "github.com/Pavel-Karpenko/IdempotencyMiddleware/store/redis"
	goredis "github.com/redis/go-redis/v9"
)

func main() {
	// Connect to Redis.
	rdb := goredis.NewClient(&goredis.Options{
		Addr: os.Getenv("REDIS_ADDR"), // e.g. "localhost:6379"
	})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("redis: %v", err)
	}

	// Build the middleware.
	// Keys are namespaced with prefix "pay:" to avoid collisions with other services.
	store := idmredis.New(rdb, idmredis.WithPrefix("pay:"))
	m := idempotency.New(idempotency.Config{
		Store:            store,
		TTL:              24 * time.Hour,
		CheckRequestHash: true, // 422 if the same key arrives with a different body
	})

	mux := http.NewServeMux()

	// Only POST /payments is idempotency-protected.
	// GET endpoints are passed through automatically (not in the default Methods list).
	mux.Handle("POST /api/v1/payments", m.Handler(http.HandlerFunc(createPayment)))
	mux.Handle("GET /api/v1/payments/{id}", http.HandlerFunc(getPayment))

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

type CreatePaymentRequest struct {
	Amount   int64  `json:"amount"`   // in cents
	Currency string `json:"currency"` // e.g. "USD"
	To       string `json:"to"`       // recipient account
}

func createPayment(w http.ResponseWriter, r *http.Request) {
	var req CreatePaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	// Charge the card. This will NOT be called on replayed requests.
	paymentID, err := chargeCard(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"id":     paymentID,
		"status": "created",
	})
}

func chargeCard(_ context.Context, req CreatePaymentRequest) (string, error) {
	// Call Stripe / PayPal / your payment provider here.
	return "pay_1234567890", nil
}

func getPayment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": id, "status": "created"})
}
```

**Client request:**

```bash
curl -X POST https://api.example.com/api/v1/payments \
  -H "Idempotency-Key: 550e8400-e29b-41d4-a716-446655440000" \
  -H "Content-Type: application/json" \
  -d '{"amount": 9900, "currency": "USD", "to": "acct_abc"}'
```

**First response** (`201 Created`):
```json
{"id": "pay_1234567890", "status": "created"}
```

**Any subsequent response** with the same key (`201 Created` + replayed header):
```
Idempotent-Replayed: true

{"id": "pay_1234567890", "status": "created"}
```

---

### Example 2 — gRPC server with PostgreSQL

A gRPC payment service backed by PostgreSQL. Useful when you need a full audit trail of idempotency entries alongside your transactional data.

```go
package main

import (
	"context"
	"log"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"github.com/jackc/pgx/v5/pgxpool"

	grpcidempotency "github.com/Pavel-Karpenko/IdempotencyMiddleware/grpc"
	idmpg "github.com/Pavel-Karpenko/IdempotencyMiddleware/store/postgres"

	// Your generated proto package:
	// pb "github.com/yourorg/yourrepo/gen/go/payments/v1"
)

func main() {
	// Connect to PostgreSQL.
	// Apply store/postgres/schema.sql once before starting.
	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()

	// Periodically remove expired entries so the table stays compact.
	pgStore := idmpg.New(pool, idmpg.WithTable("idempotency_keys"))
	go func() {
		ticker := time.NewTicker(time.Hour)
		for range ticker.C {
			n, err := pgStore.Cleanup(context.Background())
			if err != nil {
				log.Printf("idempotency cleanup error: %v", err)
			} else if n > 0 {
				log.Printf("idempotency cleanup: removed %d expired entries", n)
			}
		}
	}()

	// Build the gRPC interceptor.
	idm := grpcidempotency.New(grpcidempotency.Config{
		Store:       pgStore,
		MetadataKey: "idempotency-key",
		TTL:         24 * time.Hour,
	})

	srv := grpc.NewServer(
		grpc.UnaryInterceptor(idm.Unary()),
	)

	// pb.RegisterPaymentServiceServer(srv, &paymentServer{pool: pool})

	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Println("gRPC server listening on :50051")
	log.Fatal(srv.Serve(lis))
}
```

**Go client** — setting the idempotency key in metadata:

```go
import (
	"context"
	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
)

func callCreatePayment(ctx context.Context, client pb.PaymentServiceClient) {
	// Generate a stable key per logical operation.
	// Store it client-side so retries reuse the same key.
	idempotencyKey := uuid.New().String()

	ctx = metadata.AppendToOutgoingContext(ctx, "idempotency-key", idempotencyKey)
	resp, err := client.CreatePayment(ctx, &pb.CreatePaymentRequest{
		AmountCents: 9900,
		Currency:    "USD",
	})
	// If the network drops and you retry with the same ctx (same key),
	// the server replays the stored response — no double charge.
	_ = resp
	_ = err
}
```

---

## Store backends

### Memory (testing / single-instance)

No external dependencies. Data is lost on restart.

```go
import "github.com/Pavel-Karpenko/IdempotencyMiddleware/store/memory"

store := memory.New()
```

### Redis (recommended for production)

Atomic `SetNX` via a Lua script. Works with `*goredis.Client`, `*goredis.ClusterClient`, and `*goredis.Ring`.

```go
import (
	idmredis "github.com/Pavel-Karpenko/IdempotencyMiddleware/store/redis"
	goredis  "github.com/redis/go-redis/v9"
)

rdb := goredis.NewClient(&goredis.Options{Addr: "localhost:6379"})

// Optional: namespace keys to avoid collisions between services.
store := idmredis.New(rdb, idmredis.WithPrefix("payments:idm:"))
```

### PostgreSQL (audit trail)

Atomic `SetNX` via `INSERT … ON CONFLICT DO NOTHING`. Apply the schema once:

```bash
psql "$DATABASE_URL" -f store/postgres/schema.sql
```

```go
import (
	idmpg  "github.com/Pavel-Karpenko/IdempotencyMiddleware/store/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

pool, _ := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))

// Custom table name — useful if you run multiple services on one DB.
store := idmpg.New(pool, idmpg.WithTable("payments_idempotency_keys"))

// Periodically purge expired rows (call this in a background goroutine).
deleted, err := store.Cleanup(ctx)
```

---

## Configuration

```go
idempotency.Config{
    // Required.
    Store Store

    // Header that carries the client key. Default: "Idempotency-Key".
    KeyHeader string

    // How long responses are retained. Default: 24h.
    TTL time.Duration

    // HTTP methods to protect. Default: POST, PATCH.
    // GET, HEAD, OPTIONS are idempotent by definition — no need to list them.
    Methods []string

    // When true, a SHA-256 fingerprint of method+path+body is stored.
    // A second request with the same key but a different body receives 422.
    // Recommended for payment APIs. Default: false.
    CheckRequestHash bool

    // Decides whether a response is worth caching.
    // Default: cache all responses with status < 500.
    // Set to `func(code int) bool { return true }` to cache 5xx too.
    ShouldCache func(statusCode int) bool

    // Called when the store returns an unexpected error.
    // Default: respond 503 Service Unavailable.
    ErrorHandler func(http.ResponseWriter, *http.Request, error)
}
```

---

## HTTP response reference

| Situation | Status | Header |
|---|---|---|
| First request processed | Your handler's status | — |
| Replayed response | Your handler's original status | `Idempotent-Replayed: true` |
| Concurrent duplicate in-flight | `409 Conflict` | — |
| Key reused with different body | `422 Unprocessable Entity` | — |
| Store unavailable | `503 Service Unavailable` | — |

---

## Key scoping in multi-tenant systems

The middleware treats idempotency keys as **global** within a store. Two different users sending the same key string would collide.

**Recommended pattern:** prefix the key in an authentication middleware, before the idempotency middleware sees it.

```go
// authMiddleware runs before idempotency middleware in the chain.
func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := userIDFromToken(r) // extract from JWT / session
		if key := r.Header.Get("Idempotency-Key"); key != "" {
			// Rewrite the key so it is scoped to this user.
			r.Header.Set("Idempotency-Key", userID+":"+key)
		}
		next.ServeHTTP(w, r)
	})
}

// Wire them up: auth → idempotency → business handler.
mux.Handle("POST /api/payments",
	authMiddleware(
		m.Handler(http.HandlerFunc(createPayment)),
	),
)
```

---

## Implementing a custom store

Implement the `Store` interface to use any backend (DynamoDB, Memcached, etcd, …):

```go
type Store interface {
	// Get retrieves the entry. Returns ErrNotFound if absent or expired.
	Get(ctx context.Context, key string) (*Entry, error)

	// SetNX stores entry only if key does not exist.
	// Must be atomic. Returns (true, nil) on success, (false, nil) if key existed.
	SetNX(ctx context.Context, key string, entry *Entry, ttl time.Duration) (bool, error)

	// Set unconditionally stores or overwrites entry.
	Set(ctx context.Context, key string, entry *Entry, ttl time.Duration) error

	// Delete removes the entry. No-op if absent.
	Delete(ctx context.Context, key string) error

	// Close releases resources.
	Close() error
}
```

> **Critical:** `SetNX` must be **atomic**. If it is not, two concurrent first-requests can both observe "key not found" and both execute the handler — defeating the entire purpose of the library. See `store/memory` for a reference implementation using `sync.Mutex`.

---

## License

MIT — see [LICENSE](LICENSE).
