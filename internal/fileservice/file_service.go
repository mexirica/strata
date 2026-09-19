// Package fileservice stores and retrieves files through chunked content-addressed storage.
package fileservice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/mexirica/strata/internal/cas"
	"github.com/mexirica/strata/internal/chunker"
	"github.com/mexirica/strata/internal/manifeststore"
)

var (
	ErrInvalidConfig        = errors.New("invalid file service config")
	ErrInvalidName          = errors.New("invalid file name")
	ErrNilReader            = errors.New("reader is nil")
	ErrFileTooLarge         = errors.New("file exceeds maximum size")
	ErrTooManyChunks        = errors.New("file exceeds maximum chunk count")
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
	manifestStore *manifeststore.ManifestStore
	config        Config
}

func NewFileService(fileChunker *chunker.CDC, casStore *cas.CAS, manifestStore *manifeststore.ManifestStore, config Config) (*FileService, error) {
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
) (cas.CID, error) {
	if ctx == nil {
		return cas.CID{}, errors.New("context is nil")
	}
	if r == nil {
		return cas.CID{}, ErrNilReader
	}
	if len(name) == 0 || len(name) > s.config.MaxNameBytes || !utf8.ValidString(name) {
		return cas.CID{}, ErrInvalidName
	}

	var (
		cids []cas.CID
		size int64
	)
	if err := ctx.Err(); err != nil {
		return cas.CID{}, err
	}
	err := s.chunker.Split(ctx, r, func(chunk []byte) error {
		if len(cids) == s.config.MaxChunks {
			return ErrTooManyChunks
		}
		if int64(len(chunk)) > s.config.MaxFileSize-size {
			return ErrFileTooLarge
		}
		chunkCID, err := s.cas.Put(ctx, chunk)
		if err != nil {
			return err
		}

		cids = append(cids, chunkCID)
		size += int64(len(chunk))

		return nil
	})
	if err != nil {
		return cas.CID{}, err
	}

	manifestCID, err := s.manifestStore.Put(ctx, manifeststore.FileManifest{
		Version: manifeststore.CurrentVersion,
		Name:    name,
		Size:    size,
		Chunks:  cids,
	})
	if err != nil {
		return cas.CID{}, err
	}
	return manifestCID, nil
}

func (s *FileService) Retrieve(
	ctx context.Context,
	manifestCID cas.CID,
) (io.ReadCloser, *manifeststore.FileManifest, error) {
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
		var written int64
		for _, chunkCID := range manifest.Chunks {
			if err := streamCtx.Err(); err != nil {
				_ = pw.CloseWithError(err)
				return
			}

			chunk, err := s.cas.Get(streamCtx, chunkCID)
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			if int64(len(chunk)) > manifest.Size-written {
				_ = pw.CloseWithError(ErrManifestSizeMismatch)
				return
			}
			if _, err := pw.Write(chunk); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			written += int64(len(chunk))
		}
		if written != manifest.Size {
			_ = pw.CloseWithError(ErrManifestSizeMismatch)
			return
		}
		_ = pw.Close()
	}()

	return &retrievalReader{PipeReader: pr, cancel: cancel}, &manifest, nil
}

type retrievalReader struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (r *retrievalReader) Close() error {
	r.cancel()
	return r.PipeReader.Close()
}
