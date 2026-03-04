// Package grpc provides a gRPC unary server interceptor for idempotency.
// Idempotency keys are read from incoming gRPC metadata.
//
// Proto responses are serialised with proto.Marshal; the message type name is
// recorded so the response can be faithfully reconstructed on replay.
// gRPC status errors from the application layer are also cached (unless they
// are transient: Canceled, DeadlineExceeded, Internal, Unavailable).
package grpc

import (
	"context"
	"encoding/json"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	idempotency "github.com/Pavel-Karpenko/IdempotencyMiddleware"
)

// Config holds configuration for the gRPC idempotency interceptor.
type Config struct {
	// Store is the backend for persisting entries. Required.
	Store idempotency.Store

	// MetadataKey is the gRPC metadata key that carries the idempotency key.
	// Default: "idempotency-key".
	MetadataKey string

	// TTL controls how long entries are retained. Default: 24h.
	TTL time.Duration
}

func (c *Config) applyDefaults() Config {
	out := *c
	if out.MetadataKey == "" {
		out.MetadataKey = "idempotency-key"
	}
	if out.TTL == 0 {
		out.TTL = 24 * time.Hour
	}
	return out
}

// Interceptor enforces idempotency for gRPC unary RPCs.
type Interceptor struct {
	cfg Config
}

// New creates an Interceptor. Panics if cfg.Store is nil.
func New(cfg Config) *Interceptor {
	if cfg.Store == nil {
		panic("idempotency/grpc: Config.Store must not be nil")
	}
	return &Interceptor{cfg: cfg.applyDefaults()}
}

// Unary returns a gRPC unary server interceptor.
func (i *Interceptor) Unary() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req interface{},
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (interface{}, error) {
		key, ok := metaKey(ctx, i.cfg.MetadataKey)
		if !ok {
			return handler(ctx, req)
		}

		inProgress := &idempotency.Entry{
			Status:    idempotency.EntryStatusInProgress,
			CreatedAt: time.Now(),
		}
		acquired, err := i.cfg.Store.SetNX(ctx, key, inProgress, i.cfg.TTL)
		if err != nil {
			return nil, grpcstatus.Errorf(codes.Unavailable, "idempotency store: %v", err)
		}

		if !acquired {
			return i.handleExisting(ctx, key)
		}

		resp, handlerErr := handler(ctx, req)

		if handlerErr != nil {
			if shouldCacheGRPCError(handlerErr) {
				_ = i.cfg.Store.Set(ctx, key, errorEntry(handlerErr), i.cfg.TTL)
			} else {
				_ = i.cfg.Store.Delete(ctx, key)
			}
			return nil, handlerErr
		}

		body, err := marshalProto(resp)
		if err == nil {
			completed := &idempotency.Entry{
				Status:    idempotency.EntryStatusCompleted,
				Body:      body,
				CreatedAt: time.Now(),
			}
			_ = i.cfg.Store.Set(ctx, key, completed, i.cfg.TTL)
		}

		return resp, nil
	}
}

func (i *Interceptor) handleExisting(ctx context.Context, key string) (interface{}, error) {
	existing, err := i.cfg.Store.Get(ctx, key)
	if err != nil {
		return nil, grpcstatus.Errorf(codes.Unavailable, "idempotency store: %v", err)
	}

	if existing.Status == idempotency.EntryStatusInProgress {
		return nil, grpcstatus.Errorf(codes.AlreadyExists, "duplicate request in progress; retry after the original completes")
	}

	if existing.StatusCode != 0 {
		return nil, replayGRPCError(existing)
	}

	return unmarshalProto(existing.Body)
}

// ─── serialisation ────────────────────────────────────────────────────────────

type wireEnvelope struct {
	Type string `json:"t"`
	Data []byte `json:"d"`
}

type grpcErrorPayload struct {
	Code    uint32 `json:"code"`
	Message string `json:"msg"`
}

func marshalProto(resp interface{}) ([]byte, error) {
	msg, ok := resp.(proto.Message)
	if !ok {
		return nil, nil
	}
	data, err := proto.Marshal(msg)
	if err != nil {
		return nil, err
	}
	env := wireEnvelope{
		Type: string(msg.ProtoReflect().Descriptor().FullName()),
		Data: data,
	}
	return json.Marshal(env)
}

func unmarshalProto(b []byte) (interface{}, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var env wireEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, grpcstatus.Errorf(codes.Internal, "idempotency: corrupt stored response")
	}
	mt, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(env.Type))
	if err != nil {
		return nil, grpcstatus.Errorf(codes.Internal, "idempotency: unknown proto type %q", env.Type)
	}
	msg := mt.New().Interface()
	if err := proto.Unmarshal(env.Data, msg); err != nil {
		return nil, grpcstatus.Errorf(codes.Internal, "idempotency: failed to unmarshal stored response")
	}
	return msg, nil
}

func errorEntry(err error) *idempotency.Entry {
	st, _ := grpcstatus.FromError(err)
	payload, _ := json.Marshal(grpcErrorPayload{
		Code:    uint32(st.Code()),
		Message: st.Message(),
	})
	env := wireEnvelope{Type: "grpc_err", Data: payload}
	body, _ := json.Marshal(env)
	return &idempotency.Entry{
		Status:     idempotency.EntryStatusCompleted,
		StatusCode: int(st.Code()),
		Body:       body,
		CreatedAt:  time.Now(),
	}
}

func replayGRPCError(e *idempotency.Entry) error {
	var env wireEnvelope
	if err := json.Unmarshal(e.Body, &env); err != nil {
		return grpcstatus.Errorf(codes.Internal, "idempotency: corrupt stored error")
	}
	var p grpcErrorPayload
	if err := json.Unmarshal(env.Data, &p); err != nil {
		return grpcstatus.Errorf(codes.Internal, "idempotency: corrupt stored error payload")
	}
	return grpcstatus.Errorf(codes.Code(p.Code), p.Message)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func metaKey(ctx context.Context, mdKey string) (string, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}
	vals := md.Get(mdKey)
	if len(vals) == 0 || vals[0] == "" {
		return "", false
	}
	return vals[0], true
}

var transientCodes = map[codes.Code]bool{
	codes.Canceled:          true,
	codes.DeadlineExceeded:  true,
	codes.Internal:          true,
	codes.Unavailable:       true,
	codes.ResourceExhausted: true,
}

func shouldCacheGRPCError(err error) bool {
	st, ok := grpcstatus.FromError(err)
	if !ok {
		return false
	}
	return !transientCodes[st.Code()]
}
