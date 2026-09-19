package cas

import (
	"context"
	"errors"
	"fmt"
	"io"

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

type Config struct {
	MaxObjectSize int64
	TempDir       string
}

type CAS struct {
	storage storage.ObjectStorage
	hasher  hasher.Hasher
	config  Config
}

func getKey(cid CID) []byte {
	return append([]byte("chunk:"), cid.Bytes()...)
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
func (c *CAS) Put(ctx context.Context, data []byte) (CID, error) {
	if err := ctx.Err(); err != nil {
		return CID{}, err
	}
	if int64(len(data)) > c.config.MaxObjectSize {
		return CID{}, ErrObjectTooLarge
	}

	cid := c.hasher.Hash(data)
	key := getKey(cid)

	if _, err := c.storage.PutIfNotExists(ctx, key, data); err != nil {
		if errors.Is(err, storage.ErrContentMismatch) {
			return CID{}, fmt.Errorf("%w: %w", ErrCorruptedChunk, err)
		}
		return CID{}, err
	}

	return cid, nil
}

// Get retrieves data for a given CID and verifies its cryptographic integrity against bit-rot.
func (c *CAS) Get(ctx context.Context, cid CID) ([]byte, error) {
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
