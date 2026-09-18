package storage

import (
	"context"
)

type ObjectStorage interface {
	Put(ctx context.Context, key, value []byte) error
	Get(ctx context.Context, key []byte) ([]byte, error)
	Exists(ctx context.Context, key []byte) (bool, error)
	Delete(ctx context.Context, key []byte) error
}
