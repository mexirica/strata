// Package chunker provides a Content-Defined Chunking (CDC) implementation with support for multiple algorithms.
package chunker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/PlakarKorp/go-cdc-chunkers"
)

type CdcAlgorithm string

const (
	FastCDC  CdcAlgorithm = "fastcdc"
	UltraCDC CdcAlgorithm = "ultracdc"
	JC       CdcAlgorithm = "jc"
)

var (
	ErrNilReader      = errors.New("reader is nil")
	ErrNilCallback    = errors.New("callback is nil")
	ErrBufferTooSmall = errors.New("buffer size is too small")
	ErrEmptyChunk     = errors.New("chunker returned empty chunk unexpectedly")
)

type Config struct {
	algorithm  CdcAlgorithm
	minSize    int
	normalSize int
	maxSize    int
}

func (c Config) validate() error {
	if c.minSize <= 0 {
		return errors.New("min size must be greater than zero")
	}
	if c.normalSize < c.minSize {
		return errors.New("normal size must be >= min size")
	}
	if c.maxSize < c.normalSize {
		return errors.New("max size must be >= normal size")
	}

	switch c.algorithm {
	case FastCDC, UltraCDC, JC:
		return nil
	default:
		return fmt.Errorf("unsupported algorithm: %q", c.algorithm)
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

// bufferSize returns the minimum required size of the internal/external buffer.
// CDC requires extra capacity beyond MaxSize for the sliding search window.
func (c *CDC) bufferSize() int {
	return c.config.maxSize * 2
}

func (c *CDC) Split(
	ctx context.Context,
	r io.Reader,
	fn func([]byte) error,
) error {
	if err := validateInput(ctx, r, fn); err != nil {
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
		MinSize:    c.config.minSize,
		NormalSize: c.config.normalSize,
		MaxSize:    c.config.maxSize,
	}

	chunker, err := chunkers.NewChunkerBuffer(
		string(c.config.algorithm),
		r,
		opts,
		buf,
	)
	if err != nil {
		return fmt.Errorf("create %s chunker: %w", c.config.algorithm, err)
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
