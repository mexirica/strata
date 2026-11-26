// Package repository opens and composes a Strata repository.
package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/mexirica/strata/internal/buildinfo"
	"github.com/mexirica/strata/internal/content/cas"
	"github.com/mexirica/strata/internal/content/chunker"
	"github.com/mexirica/strata/internal/content/cid"
	"github.com/mexirica/strata/internal/content/hasher"
	"github.com/mexirica/strata/internal/file"
	"github.com/mexirica/strata/internal/maintenance"
	"github.com/mexirica/strata/internal/repository/metadata"
	"github.com/mexirica/strata/internal/storage"
)

const (
	maintenancePageSize     = 1000
	manifestFixedSize       = 34
	manifestReferenceSize   = 42
	manifestOverflowReserve = manifestFixedSize
)

var ErrInvalidConfig = errors.New("invalid node config")

type Config struct {
	DataDir       string
	HashAlgorithm cid.Algorithm
	MaxFileSize   int64
	MaxChunks     int
	MaxNameBytes  int
}

type InitConfig struct {
	Config
	Chunking chunker.Config
}

type Node struct {
	mu        sync.RWMutex
	storage   *storage.Badger
	files     *file.FileService
	gc        *maintenance.GarbageCollector
	scrubber  *maintenance.Scrubber
	closeOnce sync.Once
	closeErr  error
}

// Init creates metadata only in an empty repository, or checks compatibility
// with existing metadata. Persisted chunking is immutable across repeated init.
func Init(ctx context.Context, config InitConfig) (*Node, error) {
	if err := validateConfig(config.Config); err != nil {
		return nil, err
	}
	if _, err := chunker.NewCDC(config.Chunking); err != nil {
		return nil, fmt.Errorf("%w: chunking: %v", ErrInvalidConfig, err)
	}
	repoMetadata := metadata.NewMetadata(time.Now(), buildinfo.Version, metadata.Chunking{
		Algorithm:  config.Chunking.Algorithm,
		MinSize:    config.Chunking.MinSize,
		NormalSize: config.Chunking.NormalSize,
		MaxSize:    config.Chunking.MaxSize,
	})
	return open(ctx, config.Config, &repoMetadata)
}

// Open requires existing metadata and uses its chunking policy, not process
// defaults. The returned Node owns storage and must be closed by the caller.
func Open(ctx context.Context, config Config) (*Node, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	return open(ctx, config, nil)
}

func open(ctx context.Context, config Config, candidate *metadata.Metadata) (*Node, error) {
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

	metadataStore, err := metadata.NewStore(db)
	if err != nil {
		return closeOnError(fmt.Errorf("create metadata store: %w", err))
	}
	storedMetadata, err := loadMetadata(ctx, metadataStore, config, candidate)
	if err != nil {
		return closeOnError(err)
	}
	if err := validateWriteLimits(config, storedMetadata.Chunking); err != nil {
		return closeOnError(err)
	}

	fileChunker, err := chunker.NewCDC(chunker.Config{
		Algorithm:  storedMetadata.Chunking.Algorithm,
		MinSize:    storedMetadata.Chunking.MinSize,
		NormalSize: storedMetadata.Chunking.NormalSize,
		MaxSize:    storedMetadata.Chunking.MaxSize,
	})
	if err != nil {
		return closeOnError(fmt.Errorf("create chunker from repository metadata: %w", err))
	}
	casStore, err := cas.NewCAS(db, contentHasher, cas.Config{MaxObjectSize: int64(storedMetadata.Chunking.MaxSize)})
	if err != nil {
		return closeOnError(fmt.Errorf("create CAS: %w", err))
	}
	maxManifestSize := manifestFixedSize + config.MaxNameBytes + manifestReferenceSize*config.MaxChunks
	manifestStore, err := file.NewManifestStore(db, contentHasher, file.ManifestConfig{
		MaxManifestSize: maxManifestSize,
		MaxNameBytes:    config.MaxNameBytes,
		MaxChunks:       config.MaxChunks,
		MaxPageSize:     maintenancePageSize,
	})
	if err != nil {
		return closeOnError(fmt.Errorf("create manifest store: %w", err))
	}
	files, err := file.NewFileService(fileChunker, casStore, manifestStore, file.Config{
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

// loadMetadata validates write limits before creating new metadata. For an
// existing repository, compatibility takes precedence over requested limits.
func loadMetadata(ctx context.Context, store *metadata.Store, config Config, candidate *metadata.Metadata) (metadata.Metadata, error) {
	stored, err := store.Load(ctx)
	if candidate != nil {
		if errors.Is(err, metadata.ErrRepositoryMetadataMissing) {
			if err := validateWriteLimits(config, candidate.Chunking); err != nil {
				return metadata.Metadata{}, err
			}
			stored, err = store.LoadOrCreate(ctx, *candidate)
		}
		if err == nil {
			err = stored.ValidateCompatibility(*candidate)
		}
	}
	if err != nil {
		return metadata.Metadata{}, fmt.Errorf("initialize repository metadata: %w", err)
	}
	return stored, nil
}

func validateConfig(config Config) error {
	if strings.TrimSpace(config.DataDir) == "" {
		return fmt.Errorf("%w: data directory must not be empty", ErrInvalidConfig)
	}
	if !config.HashAlgorithm.IsValid() {
		return fmt.Errorf("%w: unsupported hash algorithm", ErrInvalidConfig)
	}
	if config.MaxFileSize <= 0 || config.MaxChunks <= 0 || config.MaxNameBytes <= 0 {
		return fmt.Errorf("%w: file, chunk count, and name limits must be greater than zero", ErrInvalidConfig)
	}
	if uint64(config.MaxChunks) > math.MaxUint32 || uint64(config.MaxNameBytes) > math.MaxUint32 {
		return fmt.Errorf("%w: manifest limits exceed encoded representation", ErrInvalidConfig)
	}
	maxInt := int(^uint(0) >> 1)
	if config.MaxChunks > (maxInt-manifestOverflowReserve-config.MaxNameBytes)/manifestReferenceSize {
		return fmt.Errorf("%w: manifest size overflows int", ErrInvalidConfig)
	}
	return nil
}

func validateWriteLimits(config Config, chunking metadata.Chunking) error {
	maxRepresentableFileSize := int64(config.MaxChunks) * int64(chunking.MinSize)
	if config.MaxFileSize > maxRepresentableFileSize {
		return fmt.Errorf(
			"%w: max_file_size=%d exceeds max_chunks=%d * repository min_chunk_size=%d",
			ErrInvalidConfig,
			config.MaxFileSize,
			config.MaxChunks,
			chunking.MinSize,
		)
	}
	return nil
}

func (n *Node) Store(ctx context.Context, name string, reader io.Reader) (cid.CID, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.files.Store(ctx, name, reader)
}

// Retrieve holds a shared repository lock for the stream's lifetime. Callers
// must Close even after EOF to release it and allow GC or repository shutdown.
func (n *Node) Retrieve(ctx context.Context, manifestCID cid.CID) (io.ReadCloser, *file.FileManifest, error) {
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

func (n *Node) List(ctx context.Context, after *cid.CID, limit int) ([]file.StoredManifest, *cid.CID, error) {
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
