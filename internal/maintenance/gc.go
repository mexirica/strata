// Package maintenance provides garbage collection and integrity checking for stored files.
package maintenance

import (
	"context"
	"errors"
	"fmt"

	"github.com/mexirica/strata/internal/cas"
	"github.com/mexirica/strata/internal/cid"
	"github.com/mexirica/strata/internal/manifeststore"
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
	manifestStore *manifeststore.ManifestStore
	pageSize      int
}

func NewGarbageCollector(casStore *cas.CAS, manifestStore *manifeststore.ManifestStore, pageSize int) (*GarbageCollector, error) {
	if casStore == nil || manifestStore == nil || pageSize <= 0 {
		return nil, fmt.Errorf("%w: dependencies must not be nil and page size must be greater than zero", ErrInvalidConfig)
	}
	return &GarbageCollector{cas: casStore, manifestStore: manifestStore, pageSize: pageSize}, nil
}

func (g *GarbageCollector) Run(ctx context.Context, dryRun bool) (GCReport, error) {
	var report GCReport
	if ctx == nil {
		return report, errors.New("context is nil")
	}

	referenced := make(map[cid.CID]struct{})
	var manifestCursor *cid.CID
	for {
		manifests, next, err := g.manifestStore.ListManifests(ctx, manifestCursor, g.pageSize)
		if err != nil {
			return report, err
		}
		for _, stored := range manifests {
			report.ManifestsScanned++
			for _, chunkCID := range stored.Manifest.Chunks {
				referenced[chunkCID] = struct{}{}
			}
		}
		if next == nil {
			break
		}
		manifestCursor = next
	}
	report.ChunksReferenced = len(referenced)

	var chunkCursor *cid.CID
	for {
		chunks, next, err := g.cas.ListChunks(ctx, chunkCursor, g.pageSize)
		if err != nil {
			return report, err
		}
		for _, chunkCID := range chunks {
			report.ChunksScanned++
			if _, ok := referenced[chunkCID]; ok {
				continue
			}
			if !dryRun {
				if err := g.cas.Delete(ctx, chunkCID); err != nil {
					return report, err
				}
			}
			report.ChunksDeleted++
		}
		if next == nil {
			break
		}
		chunkCursor = next
	}
	return report, nil
}
