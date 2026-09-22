// Package storage defines and implements persistent object storage.
package storage

import (
	"context"
	"errors"
)

var (
	ErrNotFound        = errors.New("object not found")
	ErrClosed          = errors.New("object storage closed")
	ErrContentMismatch = errors.New("stored object content mismatch")
)

type ObjectStorage interface {
	Put(ctx context.Context, key, value []byte) error
	PutIfNotExists(ctx context.Context, key, value []byte) (inserted bool, err error)
	Get(ctx context.Context, key []byte) ([]byte, error)
	Exists(ctx context.Context, key []byte) (bool, error)
	Delete(ctx context.Context, key []byte) error
	ListPage(ctx context.Context, prefix, after []byte, limit int) ([]Object, []byte, error)
	ListKeysPage(ctx context.Context, prefix, after []byte, limit int) ([][]byte, []byte, error)
	Close() error
}

type Object struct {
	Key   []byte
	Value []byte
}
