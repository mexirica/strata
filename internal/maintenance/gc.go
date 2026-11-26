// Package maintenance provides garbage collection and integrity checking for stored files.
package maintenance

import (
	"context"
	"errors"
	"fmt"

	"github.com/mexirica/strata/internal/content/cas"
	"github.com/mexirica/strata/internal/content/cid"
	"github.com/mexirica/strata/internal/file"
)

var ErrInvalidConfig = errors.New("invalid maintenance config")

type GCReport struct {
	ManifestsScanned int
	ChunksReferenced int
	ChunksScanned    int
	ChunksDeleted    int
}

type GarbageCollector struct {
	cas           *cas.CAS
	manifestStore *file.ManifestStore
	pageSize      int
}

func NewGarbageCollector(casStore *cas.CAS, manifestStore *file.ManifestStore, pageSize int) (*GarbageCollector, error) {
	if casStore == nil || manifestStore == nil || pageSize <= 0 {
		return nil, fmt.Errorf("%w: dependencies must not be nil and page size must be greater than zero", ErrInvalidConfig)
	}
	return &GarbageCollector{cas: casStore, manifestStore: manifestStore, pageSize: pageSize}, nil
}

// Run completes verified marking before deleting anything. Reachability is held
// in memory; callers must exclude concurrent mutations. On failure, the report
// retains completed work. In dry-run, ChunksDeleted counts deletion candidates.
func (g *GarbageCollector) Run(ctx context.Context, dryRun bool) (GCReport, error) {
	var report GCReport
	if ctx == nil {
		return report, errors.New("context is nil")
	}

	referenced, err := g.mark(ctx, &report)
	if err != nil {
		return report, err
	}
	report.ChunksReferenced = len(referenced)
	err = g.sweep(ctx, referenced, dryRun, &report)
	return report, err
}

func (g *GarbageCollector) mark(ctx context.Context, report *GCReport) (map[cid.CID]struct{}, error) {
	referenced := make(map[cid.CID]struct{})
	var manifestCursor *cid.CID
	for {
		manifests, next, err := g.manifestStore.ListManifests(ctx, manifestCursor, g.pageSize)
		if err != nil {
			return nil, err
		}
		for _, stored := range manifests {
			report.ManifestsScanned++
			for _, chunkReference := range stored.Manifest.Chunks {
				referenced[chunkReference.CID] = struct{}{}
			}
		}
		if next == nil {
			break
		}
		manifestCursor = next
	}
	return referenced, nil
}

func (g *GarbageCollector) sweep(ctx context.Context, referenced map[cid.CID]struct{}, dryRun bool, report *GCReport) error {
	var chunkCursor *cid.CID
	for {
		chunks, next, err := g.cas.ListChunks(ctx, chunkCursor, g.pageSize)
		if err != nil {
			return err
		}
		for _, chunkCID := range chunks {
			report.ChunksScanned++
			if _, ok := referenced[chunkCID]; ok {
				continue
			}
			if !dryRun {
				if err := g.cas.Delete(ctx, chunkCID); err != nil {
					return err
				}
			}
			report.ChunksDeleted++
		}
		if next == nil {
			break
		}
		chunkCursor = next
	}
	return nil
}
