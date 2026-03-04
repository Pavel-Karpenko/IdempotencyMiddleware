// Example gRPC server demonstrating the idempotency unary interceptor.
package main

import (
	"log"
	"net"

	"google.golang.org/grpc"

	grpcidempotency "github.com/Pavel-Karpenko/IdempotencyMiddleware/grpc"
	"github.com/Pavel-Karpenko/IdempotencyMiddleware/store/memory"
)

func main() {
	store := memory.New()

	idm := grpcidempotency.New(grpcidempotency.Config{
		Store:       store,
		MetadataKey: "idempotency-key",
	})

	srv := grpc.NewServer(
		grpc.UnaryInterceptor(idm.Unary()),
	)

	// Register your generated service here:
	// pb.RegisterPaymentServiceServer(srv, &paymentServer{})

	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Println("gRPC server listening on :50051")
	log.Fatal(srv.Serve(lis))
}
