// Example HTTP server demonstrating idempotency middleware with the in-memory store.
// For production, replace memory.New() with redis.New(rdb) or postgres.New(pool).
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	idempotency "github.com/Pavel-Karpenko/IdempotencyMiddleware"
	"github.com/Pavel-Karpenko/IdempotencyMiddleware/store/memory"
)

func main() {
	store := memory.New()

	m := idempotency.New(idempotency.Config{
		Store:            store,
		TTL:              24 * time.Hour,
		CheckRequestHash: true,
	})

	mux := http.NewServeMux()

	// Wrap only the endpoints that need idempotency protection.
	mux.Handle("POST /api/payments", m.Handler(http.HandlerFunc(handlePayment)))
	mux.Handle("POST /api/orders", m.Handler(http.HandlerFunc(handleOrder)))

	// Health check – no idempotency needed.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	log.Println("listening on :8080")
	log.Println("send: curl -X POST http://localhost:8080/api/payments \\")
	log.Println("        -H 'Idempotency-Key: my-unique-key-001' \\")
	log.Println("        -H 'Content-Type: application/json' \\")
	log.Println("        -d '{\"amount\":100}'")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

var paymentCounter atomic.Int64

func handlePayment(w http.ResponseWriter, r *http.Request) {
	// Simulate work (e.g. charge a card via Stripe/PayPal).
	time.Sleep(150 * time.Millisecond)

	id := paymentCounter.Add(1)
	log.Printf("processing payment #%d", id)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":     id,
		"status": "created",
	})
}

func handleOrder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"order": "ok"})
}
