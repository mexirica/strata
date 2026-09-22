package maintenance

import (
	"context"
	"errors"
	"fmt"

	"github.com/mexirica/strata/internal/cas"
	"github.com/mexirica/strata/internal/cid"
	"github.com/mexirica/strata/internal/manifeststore"
	"github.com/mexirica/strata/internal/storage"
)

type IssueKind uint8

const (
	IssueCorruptedManifest IssueKind = iota + 1
	IssueMissingChunk
	IssueCorruptedChunk
	IssueManifestSizeMismatch
)

type ScrubIssue struct {
	Kind        IssueKind
	ManifestCID *cid.CID
	ChunkCID    *cid.CID
	Err         error
}

type Scrubber struct {
	cas           *cas.CAS
	manifestStore *manifeststore.ManifestStore
	pageSize      int
}

func NewScrubber(casStore *cas.CAS, manifestStore *manifeststore.ManifestStore, pageSize int) (*Scrubber, error) {
	if casStore == nil || manifestStore == nil || pageSize <= 0 {
		return nil, fmt.Errorf("%w: dependencies must not be nil and page size must be greater than zero", ErrInvalidConfig)
	}
	return &Scrubber{cas: casStore, manifestStore: manifestStore, pageSize: pageSize}, nil
}

func (s *Scrubber) Run(ctx context.Context) ([]ScrubIssue, error) {
	if ctx == nil {
		return nil, errors.New("context is nil")
	}

	var issues []ScrubIssue
	var cursor *cid.CID
	for {
		manifestCIDs, next, err := s.manifestStore.ListManifestCIDs(ctx, cursor, s.pageSize)
		if err != nil {
			return issues, err
		}
		for _, manifestCID := range manifestCIDs {
			manifest, err := s.manifestStore.Get(ctx, manifestCID)
			if err != nil {
				if errors.Is(err, manifeststore.ErrCorruptedManifest) || errors.Is(err, manifeststore.ErrInvalidManifest) || errors.Is(err, manifeststore.ErrUnsupportedVersion) || errors.Is(err, storage.ErrNotFound) {
					manifestValue := manifestCID
					issues = append(issues, ScrubIssue{Kind: IssueCorruptedManifest, ManifestCID: &manifestValue, Err: err})
					continue
				}
				return issues, err
			}

			var size int64
			for _, chunkCID := range manifest.Chunks {
				chunk, err := s.cas.Get(ctx, chunkCID)
				if err != nil {
					kind := IssueCorruptedChunk
					if errors.Is(err, storage.ErrNotFound) {
						kind = IssueMissingChunk
					} else if !errors.Is(err, cas.ErrCorruptedChunk) {
						return issues, err
					}
					manifestValue, chunkValue := manifestCID, chunkCID
					issues = append(issues, ScrubIssue{Kind: kind, ManifestCID: &manifestValue, ChunkCID: &chunkValue, Err: err})
					continue
				}
				size += int64(len(chunk))
			}
			if size != manifest.Size {
				manifestValue := manifestCID
				issues = append(issues, ScrubIssue{
					Kind:        IssueManifestSizeMismatch,
					ManifestCID: &manifestValue,
					Err:         fmt.Errorf("manifest size is %d, chunks total %d", manifest.Size, size),
				})
			}
		}
		if next == nil {
			break
		}
		cursor = next
	}
	return issues, nil
}
