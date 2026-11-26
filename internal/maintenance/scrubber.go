package maintenance

import (
	"context"
	"errors"
	"fmt"

	"github.com/mexirica/strata/internal/content/cas"
	"github.com/mexirica/strata/internal/content/cid"
	"github.com/mexirica/strata/internal/file"
	"github.com/mexirica/strata/internal/storage"
)

type IssueKind uint8

const (
	IssueCorruptedManifest IssueKind = iota + 1
	IssueMissingChunk
	IssueCorruptedChunk
	IssueManifestSizeMismatch
	IssueChunkSizeMismatch
)

type ScrubIssue struct {
	Kind        IssueKind
	ManifestCID *cid.CID
	ChunkCID    *cid.CID
	Err         error
}

type Scrubber struct {
	cas           *cas.CAS
	manifestStore *file.ManifestStore
	pageSize      int
}

func NewScrubber(casStore *cas.CAS, manifestStore *file.ManifestStore, pageSize int) (*Scrubber, error) {
	if casStore == nil || manifestStore == nil || pageSize <= 0 {
		return nil, fmt.Errorf("%w: dependencies must not be nil and page size must be greater than zero", ErrInvalidConfig)
	}
	return &Scrubber{cas: casStore, manifestStore: manifestStore, pageSize: pageSize}, nil
}

// Run records integrity diagnostics and continues scanning. Operational errors
// abort the scan while retaining prior issues; pagination is not a snapshot.
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
			manifestIssues, err := s.inspectManifest(ctx, manifestCID)
			issues = append(issues, manifestIssues...)
			if err != nil {
				return issues, err
			}
		}
		if next == nil {
			break
		}
		cursor = next
	}
	return issues, nil
}

// inspectManifest compares verified chunk lengths individually and in total.
// Missing or corrupt chunks do not contribute bytes to the reconstructed size.
func (s *Scrubber) inspectManifest(ctx context.Context, manifestCID cid.CID) ([]ScrubIssue, error) {
	manifest, err := s.manifestStore.Get(ctx, manifestCID)
	if err != nil {
		if errors.Is(err, file.ErrCorruptedManifest) || errors.Is(err, file.ErrInvalidManifest) || errors.Is(err, file.ErrUnsupportedVersion) || errors.Is(err, storage.ErrNotFound) {
			manifestValue := manifestCID
			return []ScrubIssue{{Kind: IssueCorruptedManifest, ManifestCID: &manifestValue, Err: err}}, nil
		}
		return nil, err
	}

	var issues []ScrubIssue
	var size int64
	for _, chunkReference := range manifest.Chunks {
		chunk, err := s.cas.Get(ctx, chunkReference.CID)
		if err != nil {
			kind := IssueCorruptedChunk
			if errors.Is(err, storage.ErrNotFound) {
				kind = IssueMissingChunk
			} else if !errors.Is(err, cas.ErrCorruptedChunk) {
				return issues, err
			}
			manifestValue, chunkValue := manifestCID, chunkReference.CID
			issues = append(issues, ScrubIssue{Kind: kind, ManifestCID: &manifestValue, ChunkCID: &chunkValue, Err: err})
			continue
		}
		if uint64(len(chunk)) != chunkReference.Size {
			manifestValue, chunkValue := manifestCID, chunkReference.CID
			issues = append(issues, ScrubIssue{
				Kind:        IssueChunkSizeMismatch,
				ManifestCID: &manifestValue,
				ChunkCID:    &chunkValue,
				Err:         fmt.Errorf("%w: chunk size is %d, reference declares %d", file.ErrChunkSizeMismatch, len(chunk), chunkReference.Size),
			})
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
	return issues, nil
}
