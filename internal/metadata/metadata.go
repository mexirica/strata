// Package metadata persists and validates repository-level metadata.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mexirica/strata/internal/chunker"
	"github.com/mexirica/strata/internal/manifeststore"
	"github.com/mexirica/strata/internal/storage"
)

const CurrentFormatVersion uint16 = 1

var metadataKey = []byte("repository:metadata")

var (
	ErrRepositoryMetadataMissing    = errors.New("repository metadata missing")
	ErrLegacyRepository             = errors.New("repository contains data but has no metadata")
	ErrUnsupportedRepositoryVersion = errors.New("unsupported repository version")
	ErrCorruptedRepositoryMetadata  = errors.New("corrupted repository metadata")
	ErrInvalidRepositoryMetadata    = errors.New("invalid repository metadata")
	ErrIncompatibleRepository       = errors.New("incompatible repository")
)

type Metadata struct {
	FormatVersion         uint16    `json:"format_version"`
	CreatedAt             time.Time `json:"created_at"`
	AppVersion            string    `json:"app_version"`
	Chunking              Chunking  `json:"chunking"`
	ManifestFormatVersion uint16    `json:"manifest_format_version"`
}

type Chunking struct {
	Algorithm  chunker.CdcAlgorithm `json:"algorithm"`
	MinSize    int                  `json:"min_size"`
	NormalSize int                  `json:"normal_size"`
	MaxSize    int                  `json:"max_size"`
}

type Store struct {
	storage storage.ObjectStorage
}

func NewMetadata(createdAt time.Time, version string, chunking Chunking) Metadata {
	return Metadata{
		FormatVersion:         CurrentFormatVersion,
		CreatedAt:             createdAt.UTC(),
		AppVersion:            version,
		Chunking:              chunking,
		ManifestFormatVersion: manifeststore.CurrentVersion,
	}
}

func NewStore(objectStorage storage.ObjectStorage) (*Store, error) {
	if objectStorage == nil {
		return nil, errors.New("metadata storage is nil")
	}
	return &Store{storage: objectStorage}, nil
}

func (s *Store) Create(ctx context.Context, metadata Metadata) error {
	if err := validate(metadata); err != nil {
		return err
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode repository metadata: %w", err)
	}
	if _, err := s.storage.PutIfNotExists(ctx, metadataKey, data); err != nil {
		return fmt.Errorf("create repository metadata: %w", err)
	}
	return nil
}

func (s *Store) Load(ctx context.Context) (Metadata, error) {
	data, err := s.storage.Get(ctx, metadataKey)
	if errors.Is(err, storage.ErrNotFound) {
		return Metadata{}, ErrRepositoryMetadataMissing
	}
	if err != nil {
		return Metadata{}, fmt.Errorf("read repository metadata: %w", err)
	}

	var metadata Metadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return Metadata{}, fmt.Errorf("%w: %v", ErrCorruptedRepositoryMetadata, err)
	}
	if metadata.FormatVersion != CurrentFormatVersion {
		return Metadata{}, fmt.Errorf("%w: got %d, support %d", ErrUnsupportedRepositoryVersion, metadata.FormatVersion, CurrentFormatVersion)
	}
	if err := validate(metadata); err != nil {
		return Metadata{}, fmt.Errorf("%w: %v", ErrCorruptedRepositoryMetadata, err)
	}
	return metadata, nil
}

func (s *Store) LoadOrCreate(ctx context.Context, candidate Metadata) (Metadata, error) {
	metadata, err := s.Load(ctx)
	if err == nil {
		return metadata, nil
	}
	if !errors.Is(err, ErrRepositoryMetadataMissing) {
		return Metadata{}, err
	}

	keys, _, err := s.storage.ListKeysPage(ctx, nil, nil, 1)
	if err != nil {
		return Metadata{}, fmt.Errorf("inspect repository contents: %w", err)
	}
	if len(keys) != 0 {
		return Metadata{}, ErrLegacyRepository
	}
	if err := s.Create(ctx, candidate); err != nil {
		return Metadata{}, err
	}
	return candidate, nil
}

func (m Metadata) ValidateCompatibility(candidate Metadata) error {
	if m.FormatVersion != candidate.FormatVersion {
		return fmt.Errorf("%w: format version: repository=%d configured=%d",
			ErrIncompatibleRepository, m.FormatVersion, candidate.FormatVersion)
	}
	if m.ManifestFormatVersion != candidate.ManifestFormatVersion {
		return fmt.Errorf("%w: manifest version: repository=%d configured=%d",
			ErrIncompatibleRepository, m.ManifestFormatVersion, candidate.ManifestFormatVersion)
	}
	if m.Chunking != candidate.Chunking {
		return fmt.Errorf("%w: repository=%+v configured=%+v",
			ErrIncompatibleRepository, m.Chunking, candidate.Chunking)
	}
	return nil
}

func validate(metadata Metadata) error {
	if metadata.FormatVersion != CurrentFormatVersion {
		return fmt.Errorf("%w: repository format version must be %d", ErrInvalidRepositoryMetadata, CurrentFormatVersion)
	}
	if metadata.CreatedAt.IsZero() {
		return fmt.Errorf("%w: creation time must not be zero", ErrInvalidRepositoryMetadata)
	}
	if strings.TrimSpace(metadata.AppVersion) == "" {
		return fmt.Errorf("%w: creator version must not be empty", ErrInvalidRepositoryMetadata)
	}
	if metadata.ManifestFormatVersion != manifeststore.CurrentVersion {
		return fmt.Errorf("%w: manifest format version must be %d", ErrInvalidRepositoryMetadata, manifeststore.CurrentVersion)
	}
	if _, err := chunker.NewCDC(chunker.Config{
		Algorithm:  metadata.Chunking.Algorithm,
		MinSize:    metadata.Chunking.MinSize,
		NormalSize: metadata.Chunking.NormalSize,
		MaxSize:    metadata.Chunking.MaxSize,
	}); err != nil {
		return fmt.Errorf("%w: chunking configuration: %v", ErrInvalidRepositoryMetadata, err)
	}
	return nil
}
