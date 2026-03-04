package memory_test

import (
	"context"
	"testing"
	"time"

	idempotency "github.com/Pavel-Karpenko/IdempotencyMiddleware"
	"github.com/Pavel-Karpenko/IdempotencyMiddleware/store/memory"
)

var ctx = context.Background()

func entry(status idempotency.EntryStatus) *idempotency.Entry {
	return &idempotency.Entry{
		Status:     status,
		StatusCode: 201,
		Body:       []byte(`{"id":"1"}`),
		CreatedAt:  time.Now(),
	}
}

func TestGet_NotFound(t *testing.T) {
	s := memory.New()
	_, err := s.Get(ctx, "missing")
	if err != idempotency.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSetNX_StoresEntry(t *testing.T) {
	s := memory.New()
	ok, err := s.SetNX(ctx, "k1", entry(idempotency.EntryStatusCompleted), time.Hour)
	if err != nil || !ok {
		t.Fatalf("SetNX failed: ok=%v err=%v", ok, err)
	}

	got, err := s.Get(ctx, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if got.StatusCode != 201 {
		t.Fatalf("want 201, got %d", got.StatusCode)
	}
}

func TestSetNX_ReturnsFalseIfExists(t *testing.T) {
	s := memory.New()
	s.SetNX(ctx, "k1", entry(idempotency.EntryStatusCompleted), time.Hour)

	ok, err := s.SetNX(ctx, "k1", entry(idempotency.EntryStatusInProgress), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("SetNX must return false when key already exists")
	}

	got, _ := s.Get(ctx, "k1")
	if got.Status != idempotency.EntryStatusCompleted {
		t.Fatal("SetNX must not overwrite existing entry")
	}
}

func TestSet_Overwrites(t *testing.T) {
	s := memory.New()
	s.SetNX(ctx, "k1", entry(idempotency.EntryStatusInProgress), time.Hour)
	s.Set(ctx, "k1", entry(idempotency.EntryStatusCompleted), time.Hour)

	got, _ := s.Get(ctx, "k1")
	if got.Status != idempotency.EntryStatusCompleted {
		t.Fatal("Set must overwrite existing entry")
	}
}

func TestDelete_RemovesEntry(t *testing.T) {
	s := memory.New()
	s.SetNX(ctx, "k1", entry(idempotency.EntryStatusInProgress), time.Hour)
	s.Delete(ctx, "k1")

	_, err := s.Get(ctx, "k1")
	if err != idempotency.ErrNotFound {
		t.Fatal("expected ErrNotFound after Delete")
	}
}

func TestExpiry(t *testing.T) {
	s := memory.New()
	s.SetNX(ctx, "k1", entry(idempotency.EntryStatusCompleted), 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	_, err := s.Get(ctx, "k1")
	if err != idempotency.ErrNotFound {
		t.Fatal("expected ErrNotFound for expired entry")
	}

	ok, err := s.SetNX(ctx, "k1", entry(idempotency.EntryStatusCompleted), time.Hour)
	if err != nil || !ok {
		t.Fatalf("SetNX after expiry failed: ok=%v err=%v", ok, err)
	}
}
