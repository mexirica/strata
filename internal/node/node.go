// Package node composes Strata's storage services into a single-node facade.
package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"

	"github.com/mexirica/strata/internal/cas"
	"github.com/mexirica/strata/internal/chunker"
	"github.com/mexirica/strata/internal/cid"
	"github.com/mexirica/strata/internal/fileservice"
	"github.com/mexirica/strata/internal/hasher"
	"github.com/mexirica/strata/internal/maintenance"
	"github.com/mexirica/strata/internal/manifeststore"
	"github.com/mexirica/strata/internal/storage"
)

const maintenancePageSize = 1000

var ErrInvalidConfig = errors.New("invalid node config")

type Config struct {
	DataDir         string
	HashAlgorithm   cid.Algorithm
	MinChunkSize    int
	NormalChunkSize int
	MaxChunkSize    int
	MaxFileSize     int64
	MaxChunks       int
	MaxNameBytes    int
}

type Node struct {
	mu        sync.RWMutex
	storage   *storage.Badger
	files     *fileservice.FileService
	gc        *maintenance.GarbageCollector
	scrubber  *maintenance.Scrubber
	closeOnce sync.Once
	closeErr  error
}

func New(config Config) (*Node, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}

	fileChunker, err := chunker.NewCDC(chunker.Config{
		Algorithm:  chunker.FastCDC,
		MinSize:    config.MinChunkSize,
		NormalSize: config.NormalChunkSize,
		MaxSize:    config.MaxChunkSize,
	})
	if err != nil {
		return nil, fmt.Errorf("create chunker: %w", err)
	}
	contentHasher, err := hasher.ForAlgorithm(config.HashAlgorithm)
	if err != nil {
		return nil, fmt.Errorf("create hasher: %w", err)
	}

	db, err := storage.NewBadger(config.DataDir)
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}
	closeOnError := func(err error) (*Node, error) {
		_ = db.Close()
		return nil, err
	}

	casStore, err := cas.NewCAS(db, contentHasher, cas.Config{MaxObjectSize: int64(config.MaxChunkSize)})
	if err != nil {
		return closeOnError(fmt.Errorf("create CAS: %w", err))
	}
	maxManifestSize := 34 + config.MaxNameBytes + 34*config.MaxChunks
	manifestStore, err := manifeststore.NewManifestStore(db, contentHasher, manifeststore.Config{
		MaxManifestSize: maxManifestSize,
		MaxNameBytes:    config.MaxNameBytes,
		MaxChunks:       config.MaxChunks,
		MaxPageSize:     maintenancePageSize,
	})
	if err != nil {
		return closeOnError(fmt.Errorf("create manifest store: %w", err))
	}
	files, err := fileservice.NewFileService(fileChunker, casStore, manifestStore, fileservice.Config{
		MaxFileSize:  config.MaxFileSize,
		MaxChunks:    config.MaxChunks,
		MaxNameBytes: config.MaxNameBytes,
	})
	if err != nil {
		return closeOnError(fmt.Errorf("create file service: %w", err))
	}
	gc, err := maintenance.NewGarbageCollector(casStore, manifestStore, maintenancePageSize)
	if err != nil {
		return closeOnError(fmt.Errorf("create garbage collector: %w", err))
	}
	scrubber, err := maintenance.NewScrubber(casStore, manifestStore, maintenancePageSize)
	if err != nil {
		return closeOnError(fmt.Errorf("create scrubber: %w", err))
	}

	return &Node{storage: db, files: files, gc: gc, scrubber: scrubber}, nil
}

func validateConfig(config Config) error {
	if strings.TrimSpace(config.DataDir) == "" {
		return fmt.Errorf("%w: data directory must not be empty", ErrInvalidConfig)
	}
	if !config.HashAlgorithm.IsValid() {
		return fmt.Errorf("%w: unsupported hash algorithm", ErrInvalidConfig)
	}
	if config.MinChunkSize <= 0 || config.NormalChunkSize <= config.MinChunkSize || config.MaxChunkSize <= config.NormalChunkSize {
		return fmt.Errorf("%w: chunk sizes must satisfy 0 < min < normal < max", ErrInvalidConfig)
	}
	if config.MaxChunkSize > chunker.MaxSupportedChunkSize {
		return fmt.Errorf("%w: maximum chunk size exceeds %d", ErrInvalidConfig, chunker.MaxSupportedChunkSize)
	}
	if config.NormalChunkSize&(config.NormalChunkSize-1) != 0 || config.MinChunkSize < 64 {
		return fmt.Errorf("%w: FastCDC requires a minimum of 64 bytes and a power-of-two normal size", ErrInvalidConfig)
	}
	if config.MaxFileSize <= 0 || config.MaxChunks <= 0 || config.MaxNameBytes <= 0 {
		return fmt.Errorf("%w: file, chunk count, and name limits must be greater than zero", ErrInvalidConfig)
	}
	if uint64(config.MaxChunks) > math.MaxUint32 || uint64(config.MaxNameBytes) > math.MaxUint32 {
		return fmt.Errorf("%w: manifest limits exceed encoded representation", ErrInvalidConfig)
	}
	maxRepresentableFileSize := int64(config.MaxChunks) * int64(config.MinChunkSize)
	if config.MaxFileSize > maxRepresentableFileSize {
		return fmt.Errorf("%w: max chunks cannot represent max file size", ErrInvalidConfig)
	}
	maxInt := int(^uint(0) >> 1)
	if config.MaxChunks > (maxInt-34-config.MaxNameBytes)/34 {
		return fmt.Errorf("%w: manifest size overflows int", ErrInvalidConfig)
	}
	return nil
}

func (n *Node) Store(ctx context.Context, name string, reader io.Reader) (cid.CID, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.files.Store(ctx, name, reader)
}

func (n *Node) Retrieve(ctx context.Context, manifestCID cid.CID) (io.ReadCloser, *manifeststore.FileManifest, error) {
	n.mu.RLock()
	reader, manifest, err := n.files.Retrieve(ctx, manifestCID)
	if err != nil {
		n.mu.RUnlock()
		return nil, nil, err
	}
	return &lockedReader{ReadCloser: reader, unlock: n.mu.RUnlock}, manifest, nil
}

func (n *Node) Delete(ctx context.Context, manifestCID cid.CID) error {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.files.Delete(ctx, manifestCID)
}

func (n *Node) List(ctx context.Context, after *cid.CID, limit int) ([]manifeststore.StoredManifest, *cid.CID, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.files.List(ctx, after, limit)
}

func (n *Node) RunGC(ctx context.Context, dryRun bool) (maintenance.GCReport, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.gc.Run(ctx, dryRun)
}

func (n *Node) Scrub(ctx context.Context) ([]maintenance.ScrubIssue, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.scrubber.Run(ctx)
}

func (n *Node) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.closeOnce.Do(func() {
		n.closeErr = n.storage.Close()
	})
	return n.closeErr
}

type lockedReader struct {
	io.ReadCloser
	unlock func()
	once   sync.Once
}

func (r *lockedReader) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.unlock)
	return err
}
