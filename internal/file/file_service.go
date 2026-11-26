// Package file stores and retrieves files and their manifests.
package file

import (
	"context"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/mexirica/strata/internal/content/cas"
	"github.com/mexirica/strata/internal/content/chunker"
	"github.com/mexirica/strata/internal/content/cid"
)

var (
	ErrInvalidConfig        = errors.New("invalid file service config")
	ErrInvalidName          = errors.New("invalid file name")
	ErrNilReader            = errors.New("reader is nil")
	ErrFileTooLarge         = errors.New("file exceeds maximum size")
	ErrTooManyChunks        = errors.New("file exceeds maximum chunk count")
	ErrChunkSizeMismatch    = errors.New("chunk size does not match reference")
	ErrManifestSizeMismatch = errors.New("manifest size does not match chunks")
)

type Config struct {
	MaxFileSize  int64
	MaxChunks    int
	MaxNameBytes int
}

type FileService struct {
	chunker       *chunker.CDC
	cas           *cas.CAS
	manifestStore *ManifestStore
	config        Config
}

func NewFileService(fileChunker *chunker.CDC, casStore *cas.CAS, manifestStore *ManifestStore, config Config) (*FileService, error) {
	if fileChunker == nil || casStore == nil || manifestStore == nil {
		return nil, fmt.Errorf("%w: dependencies must not be nil", ErrInvalidConfig)
	}
	if config.MaxFileSize <= 0 || config.MaxChunks <= 0 || config.MaxNameBytes <= 0 {
		return nil, fmt.Errorf("%w: all limits must be greater than zero", ErrInvalidConfig)
	}
	return &FileService{
		chunker:       fileChunker,
		cas:           casStore,
		manifestStore: manifestStore,
		config:        config,
	}, nil
}

func (s *FileService) Store(
	ctx context.Context,
	name string,
	r io.Reader,
) (cid.CID, error) {
	if ctx == nil {
		return cid.CID{}, errors.New("context is nil")
	}
	if r == nil {
		return cid.CID{}, ErrNilReader
	}
	if len(name) == 0 || len(name) > s.config.MaxNameBytes || !utf8.ValidString(name) {
		return cid.CID{}, ErrInvalidName
	}

	var (
		chunks []ChunkReference
		size   int64
	)
	if err := ctx.Err(); err != nil {
		return cid.CID{}, err
	}
	err := s.chunker.Split(ctx, r, func(chunk []byte) error {
		if len(chunks) == s.config.MaxChunks {
			return ErrTooManyChunks
		}
		if int64(len(chunk)) > s.config.MaxFileSize-size {
			return ErrFileTooLarge
		}
		chunkCID, err := s.cas.Put(ctx, chunk)
		if err != nil {
			return err
		}

		chunks = append(chunks, ChunkReference{CID: chunkCID, Size: uint64(len(chunk))})
		size += int64(len(chunk))

		return nil
	})
	if err != nil {
		return cid.CID{}, err
	}

	manifestCID, err := s.manifestStore.Put(ctx, FileManifest{
		Version: CurrentVersion,
		Name:    name,
		Size:    size,
		Chunks:  chunks,
	})
	if err != nil {
		return cid.CID{}, err
	}
	return manifestCID, nil
}

// Retrieve verifies the manifest before returning a stream. Chunk integrity and
// length errors surface during Read; Close cancels the producer even after EOF.
func (s *FileService) Retrieve(
	ctx context.Context,
	manifestCID cid.CID,
) (io.ReadCloser, *FileManifest, error) {
	if ctx == nil {
		return nil, nil, errors.New("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	manifest, err := s.manifestStore.Get(ctx, manifestCID)
	if err != nil {
		return nil, nil, err
	}

	streamCtx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()

	go func() {
		defer cancel()
		_ = pw.CloseWithError(s.writeChunks(streamCtx, pw, manifest))
	}()

	return &retrievalReader{PipeReader: pr, cancel: cancel}, &manifest, nil
}

// writeChunks emits only verified chunks whose individual and cumulative sizes
// match the manifest. A later failure leaves the already verified prefix intact.
func (s *FileService) writeChunks(ctx context.Context, writer *io.PipeWriter, manifest FileManifest) error {
	var written int64
	for _, chunkRef := range manifest.Chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk, err := s.cas.Get(ctx, chunkRef.CID)
		if err != nil {
			return err
		}
		if uint64(len(chunk)) != chunkRef.Size {
			return ErrChunkSizeMismatch
		}
		if int64(len(chunk)) > manifest.Size-written {
			return ErrManifestSizeMismatch
		}
		if _, err := writer.Write(chunk); err != nil {
			return err
		}
		written += int64(len(chunk))
	}
	if written != manifest.Size {
		return ErrManifestSizeMismatch
	}
	return nil
}

func (s *FileService) Delete(ctx context.Context, manifestCID cid.CID) error {
	if ctx == nil {
		return errors.New("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.manifestStore.Delete(ctx, manifestCID)
}

func (s *FileService) List(ctx context.Context, after *cid.CID, limit int) ([]StoredManifest, *cid.CID, error) {
	if ctx == nil {
		return nil, nil, errors.New("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return s.manifestStore.ListManifests(ctx, after, limit)
}

type retrievalReader struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (r *retrievalReader) Close() error {
	r.cancel()
	return r.PipeReader.Close()
}
