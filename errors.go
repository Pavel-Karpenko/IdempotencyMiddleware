package idempotency

import "errors"

// Sentinel errors returned by Store implementations.
var (
	// ErrNotFound is returned by Get when the key does not exist or has expired.
	ErrNotFound = errors.New("idempotency: key not found")

	// ErrKeyExists is returned by SetNX when the key already exists.
	ErrKeyExists = errors.New("idempotency: key already exists")
)
