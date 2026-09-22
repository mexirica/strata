// Package cas provides content-addressed chunk storage.
package cas

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/mexirica/strata/internal/cid"
	"github.com/mexirica/strata/internal/hasher"
	"github.com/mexirica/strata/internal/storage"
)

var (
	ErrCorruptedChunk = errors.New("corrupted chunk")
	ErrInvalidConfig  = errors.New("invalid CAS config")
	ErrInvalidCID     = errors.New("invalid CID")
	ErrNilReader      = errors.New("reader is nil")
	ErrObjectTooLarge = errors.New("object exceeds maximum size")
)

const maxMaintenancePageSize = 10000

var chunkPrefix = []byte("chunk:")

type Config struct {
	MaxObjectSize int64
}

type CAS struct {
	storage storage.ObjectStorage
	hasher  hasher.Hasher
	config  Config
}

func getKey(cid cid.CID) []byte {
	return append(append([]byte(nil), chunkPrefix...), cid.Bytes()...)
}

func NewCAS(store storage.ObjectStorage, defaultHasher hasher.Hasher, config Config) (*CAS, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: storage is nil", ErrInvalidConfig)
	}
	if defaultHasher == nil {
		return nil, fmt.Errorf("%w: hasher is nil", ErrInvalidConfig)
	}
	if config.MaxObjectSize <= 0 {
		return nil, fmt.Errorf("%w: max object size must be greater than zero", ErrInvalidConfig)
	}
	return &CAS{
		storage: store,
		hasher:  defaultHasher,
		config:  config,
	}, nil
}

// Put stores data by its content hash. Uses PutIfNotExists to be atomic and eliminate check-then-act races.
func (c *CAS) Put(ctx context.Context, data []byte) (cid.CID, error) {
	if err := ctx.Err(); err != nil {
		return cid.CID{}, err
	}
	if int64(len(data)) > c.config.MaxObjectSize {
		return cid.CID{}, ErrObjectTooLarge
	}

	chunkCID := c.hasher.Hash(data)
	key := getKey(chunkCID)

	if _, err := c.storage.PutIfNotExists(ctx, key, data); err != nil {
		if errors.Is(err, storage.ErrContentMismatch) {
			return cid.CID{}, fmt.Errorf("%w: %w", ErrCorruptedChunk, err)
		}
		return cid.CID{}, err
	}

	return chunkCID, nil
}

// Get retrieves data for a given CID and verifies its cryptographic integrity against bit-rot.
func (c *CAS) Get(ctx context.Context, cid cid.CID) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !cid.IsValid() {
		return nil, ErrInvalidCID
	}

	data, err := c.storage.Get(ctx, getKey(cid))
	if err != nil {
		return nil, err
	}

	verifier, err := hasher.ForAlgorithm(cid.Algorithm())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidCID, err)
	}
	if verifier.Hash(data) != cid {
		return nil, ErrCorruptedChunk
	}

	return data, nil
}

func (c *CAS) ListChunks(ctx context.Context, after *cid.CID, limit int) ([]cid.CID, *cid.CID, error) {
	if limit <= 0 || limit > maxMaintenancePageSize {
		return nil, nil, fmt.Errorf("%w: page size must be between 1 and %d", ErrInvalidConfig, maxMaintenancePageSize)
	}
	var afterKey []byte
	if after != nil {
		if !after.IsValid() {
			return nil, nil, ErrInvalidCID
		}
		afterKey = getKey(*after)
	}
	keys, nextKey, err := c.storage.ListKeysPage(ctx, chunkPrefix, afterKey, limit)
	if err != nil {
		return nil, nil, err
	}
	chunks := make([]cid.CID, 0, len(keys))
	for _, key := range keys {
		chunkCID, err := cidFromKey(key)
		if err != nil {
			return nil, nil, err
		}
		chunks = append(chunks, chunkCID)
	}
	if len(nextKey) == 0 {
		return chunks, nil, nil
	}
	next, err := cidFromKey(nextKey)
	if err != nil {
		return nil, nil, err
	}
	return chunks, &next, nil
}

func (c *CAS) Delete(ctx context.Context, valueCID cid.CID) error {
	if !valueCID.IsValid() {
		return ErrInvalidCID
	}
	return c.storage.Delete(ctx, getKey(valueCID))
}

func (c *CAS) Verify(ctx context.Context, valueCID cid.CID) error {
	_, err := c.Get(ctx, valueCID)
	return err
}

func cidFromKey(key []byte) (cid.CID, error) {
	if len(key) != len(chunkPrefix)+34 || string(key[:len(chunkPrefix)]) != string(chunkPrefix) {
		return cid.CID{}, fmt.Errorf("%w: invalid chunk storage key", ErrCorruptedChunk)
	}
	valueCID, err := cid.ParseCIDBytes(key[len(chunkPrefix):])
	if err != nil {
		return cid.CID{}, fmt.Errorf("%w: %w", ErrCorruptedChunk, err)
	}
	return valueCID, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
