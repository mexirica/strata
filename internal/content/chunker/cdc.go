// Package chunker provides a Content-Defined Chunking (CDC) implementation with support for multiple algorithms.
package chunker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	chunkers "github.com/PlakarKorp/go-cdc-chunkers"
	_ "github.com/PlakarKorp/go-cdc-chunkers/chunkers/fastcdc"
	_ "github.com/PlakarKorp/go-cdc-chunkers/chunkers/jc"
	_ "github.com/PlakarKorp/go-cdc-chunkers/chunkers/ultracdc"
)

type CdcAlgorithm string

const (
	FastCDC  CdcAlgorithm = "fastcdc-v1.0.0"
	UltraCDC CdcAlgorithm = "ultracdc-v1.0.0"
	JC       CdcAlgorithm = "jc"

	MaxSupportedChunkSize = 64 * 1024 * 1024
)

var (
	ErrNilReader      = errors.New("reader is nil")
	ErrNilCallback    = errors.New("callback is nil")
	ErrBufferTooSmall = errors.New("buffer size is too small")
	ErrEmptyChunk     = errors.New("chunker returned empty chunk unexpectedly")
	ErrInvalidConfig  = errors.New("invalid chunker config")
)

type Config struct {
	Algorithm  CdcAlgorithm
	MinSize    int
	NormalSize int
	MaxSize    int
}

func DefaultConfig() Config {
	return Config{
		Algorithm:  FastCDC,
		MinSize:    256 * 1024,      // 256 KiB
		NormalSize: 1024 * 1024,     // 1 MiB
		MaxSize:    4 * 1024 * 1024, // 4 MiB
	}
}

func (c Config) validate() error {
	if c.MinSize <= 0 {
		return fmt.Errorf("%w: min size must be greater than zero", ErrInvalidConfig)
	}
	if c.NormalSize <= c.MinSize {
		return fmt.Errorf("%w: normal size must be greater than min size", ErrInvalidConfig)
	}
	if c.MaxSize <= c.NormalSize {
		return fmt.Errorf("%w: max size must be greater than normal size", ErrInvalidConfig)
	}
	if c.MaxSize > MaxSupportedChunkSize {
		return fmt.Errorf("%w: max size exceeds %d bytes", ErrInvalidConfig, MaxSupportedChunkSize)
	}

	switch c.Algorithm {
	case FastCDC:
		if c.MinSize < 64 {
			return fmt.Errorf("%w: FastCDC min size must be at least 64 bytes", ErrInvalidConfig)
		}
		if c.NormalSize&(c.NormalSize-1) != 0 {
			return fmt.Errorf("%w: FastCDC normal size must be a power of two", ErrInvalidConfig)
		}
		return nil
	case UltraCDC, JC:
		return nil
	default:
		return fmt.Errorf("%w: unsupported algorithm %q", ErrInvalidConfig, c.Algorithm)
	}
}

type CDC struct {
	config Config
	pool   sync.Pool
}

func NewCDC(config Config) (*CDC, error) {
	if err := config.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	cdc := &CDC{
		config: config,
	}

	minBufSize := cdc.bufferSize()
	cdc.pool.New = func() any {
		return make([]byte, minBufSize)
	}

	return cdc, nil
}

func (c *CDC) bufferSize() int {
	return c.config.MaxSize
}

func (c *CDC) Split(
	ctx context.Context,
	r io.Reader,
	fn func([]byte) error,
) error {
	if err := validateInput(ctx, r, fn); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	buf := c.pool.Get().([]byte)
	defer c.pool.Put(buf)

	return c.split(ctx, r, buf, fn)
}

func (c *CDC) split(
	ctx context.Context,
	r io.Reader,
	buf []byte,
	fn func(chunk []byte) error,
) error {
	minRequired := c.bufferSize()
	if len(buf) < minRequired {
		return fmt.Errorf("%w: got %d bytes, need at least %d", ErrBufferTooSmall, len(buf), minRequired)
	}

	opts := &chunkers.ChunkerOpts{
		MinSize:    c.config.MinSize,
		NormalSize: c.config.NormalSize,
		MaxSize:    c.config.MaxSize,
	}

	chunker, err := chunkers.NewChunkerBuffer(
		string(c.config.Algorithm),
		r,
		opts,
		buf,
	)
	if err != nil {
		return fmt.Errorf("create %s chunker: %w", c.config.Algorithm, err)
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		chunk, err := chunker.Next()
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read next chunk: %w", err)
		}

		if chunk != nil && len(chunk) > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			// chunk is backed by the reusable internal buffer and is valid only until fn returns.
			if errFn := fn(chunk); errFn != nil {
				return errFn
			}
		} else if !errors.Is(err, io.EOF) {
			return ErrEmptyChunk
		}

		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func validateInput(
	ctx context.Context,
	r io.Reader,
	fn func([]byte) error,
) error {
	if ctx == nil {
		return errors.New("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil {
		return ErrNilReader
	}
	if fn == nil {
		return ErrNilCallback
	}

	return nil
}
