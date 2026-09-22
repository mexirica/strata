package maintenance_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mexirica/strata/internal/cas"
	"github.com/mexirica/strata/internal/cid"
	"github.com/mexirica/strata/internal/hasher"
	"github.com/mexirica/strata/internal/maintenance"
	"github.com/mexirica/strata/internal/manifeststore"
	"github.com/mexirica/strata/internal/storage"
)

func setupMaintenance(t testing.TB) (*cas.CAS, *manifeststore.ManifestStore, *storage.Badger) {
	t.Helper()
	db, err := storage.NewInMemoryBadger()
	if err != nil {
		t.Fatalf("open Badger: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := hasher.NewBlake3Hasher()
	casStore, err := cas.NewCAS(db, h, cas.Config{MaxObjectSize: 1024})
	if err != nil {
		t.Fatalf("create CAS: %v", err)
	}
	manifests, err := manifeststore.NewManifestStore(db, h, manifeststore.Config{
		MaxManifestSize: 4096,
		MaxNameBytes:    128,
		MaxChunks:       32,
		MaxPageSize:     10,
	})
	if err != nil {
		t.Fatalf("create manifest store: %v", err)
	}
	return casStore, manifests, db
}

func TestGarbageCollector_PreservesSharedChunksAndRemovesOrphans(t *testing.T) {
	casStore, manifests, _ := setupMaintenance(t)
	ctx := context.Background()
	shared, _ := casStore.Put(ctx, []byte("shared"))
	firstOnly, _ := casStore.Put(ctx, []byte("first"))
	orphan, _ := casStore.Put(ctx, []byte("orphan"))
	_, err := manifests.Put(ctx, manifeststore.FileManifest{Version: manifeststore.CurrentVersion, Name: "one", Size: 11, Chunks: []cid.CID{shared, firstOnly}})
	if err != nil {
		t.Fatalf("put first manifest: %v", err)
	}
	_, err = manifests.Put(ctx, manifeststore.FileManifest{Version: manifeststore.CurrentVersion, Name: "two", Size: 6, Chunks: []cid.CID{shared}})
	if err != nil {
		t.Fatalf("put second manifest: %v", err)
	}
	gc, err := maintenance.NewGarbageCollector(casStore, manifests, 2)
	if err != nil {
		t.Fatalf("create GC: %v", err)
	}

	dryReport, err := gc.Run(ctx, true)
	if err != nil {
		t.Fatalf("dry-run GC: %v", err)
	}
	if dryReport.ManifestsScanned != 2 || dryReport.ChunksReferenced != 2 || dryReport.ChunksScanned != 3 || dryReport.ChunksDeleted != 1 {
		t.Fatalf("unexpected dry-run report: %+v", dryReport)
	}
	if _, err := casStore.Get(ctx, orphan); err != nil {
		t.Fatalf("dry run removed orphan: %v", err)
	}
	report, err := gc.Run(ctx, false)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if report.ChunksDeleted != 1 {
		t.Fatalf("expected one deleted chunk, got %+v", report)
	}
	if _, err := casStore.Get(ctx, orphan); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected orphan removal, got %v", err)
	}
	for _, retained := range []cid.CID{shared, firstOnly} {
		if _, err := casStore.Get(ctx, retained); err != nil {
			t.Fatalf("referenced chunk removed: %v", err)
		}
	}
}

func TestScrubber_ReportsCorruptAndMissingChunks(t *testing.T) {
	casStore, manifests, raw := setupMaintenance(t)
	ctx := context.Background()
	corrupt, _ := casStore.Put(ctx, []byte("corrupt me"))
	missing, _ := casStore.Put(ctx, []byte("remove me"))
	manifestCID, err := manifests.Put(ctx, manifeststore.FileManifest{
		Version: manifeststore.CurrentVersion,
		Name:    "broken",
		Size:    19,
		Chunks:  []cid.CID{corrupt, missing},
	})
	if err != nil {
		t.Fatalf("put manifest: %v", err)
	}
	if err := raw.Put(ctx, append([]byte("chunk:"), corrupt.Bytes()...), []byte("tampered")); err != nil {
		t.Fatalf("corrupt chunk: %v", err)
	}
	if err := casStore.Delete(ctx, missing); err != nil {
		t.Fatalf("remove chunk: %v", err)
	}
	scrubber, err := maintenance.NewScrubber(casStore, manifests, 2)
	if err != nil {
		t.Fatalf("create scrubber: %v", err)
	}

	issues, err := scrubber.Run(ctx)
	if err != nil {
		t.Fatalf("scrub: %v", err)
	}
	wantKinds := map[maintenance.IssueKind]bool{
		maintenance.IssueCorruptedChunk:       false,
		maintenance.IssueMissingChunk:         false,
		maintenance.IssueManifestSizeMismatch: false,
	}
	for _, issue := range issues {
		if issue.ManifestCID == nil || *issue.ManifestCID != manifestCID {
			t.Fatalf("issue has wrong manifest CID: %+v", issue)
		}
		if _, ok := wantKinds[issue.Kind]; ok {
			wantKinds[issue.Kind] = true
		}
	}
	for kind, found := range wantKinds {
		if !found {
			t.Errorf("missing scrub issue kind %d in %+v", kind, issues)
		}
	}
	if _, err := raw.Get(ctx, append([]byte("chunk:"), corrupt.Bytes()...)); err != nil {
		t.Fatalf("scrubber modified corrupt chunk: %v", err)
	}
}

func TestScrubber_ReportsCorruptedManifestWithoutModifyingIt(t *testing.T) {
	casStore, manifests, raw := setupMaintenance(t)
	ctx := context.Background()
	manifestCID, err := manifests.Put(ctx, manifeststore.FileManifest{Version: manifeststore.CurrentVersion, Name: "corrupt"})
	if err != nil {
		t.Fatalf("put manifest: %v", err)
	}
	key := append([]byte("manifest:"), manifestCID.Bytes()...)
	corrupted := []byte("tampered manifest")
	if err := raw.Put(ctx, key, corrupted); err != nil {
		t.Fatalf("corrupt manifest: %v", err)
	}
	scrubber, _ := maintenance.NewScrubber(casStore, manifests, 2)
	issues, err := scrubber.Run(ctx)
	if err != nil {
		t.Fatalf("scrub: %v", err)
	}
	if len(issues) != 1 || issues[0].Kind != maintenance.IssueCorruptedManifest {
		t.Fatalf("expected one corrupted manifest issue, got %+v", issues)
	}
	stored, err := raw.Get(ctx, key)
	if err != nil || !bytes.Equal(stored, corrupted) {
		t.Fatalf("scrubber modified manifest: data=%q err=%v", stored, err)
	}
}

func TestGarbageCollector_RemovesChunkPersistedBeforeRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "badger")
	db, err := storage.NewBadger(path)
	if err != nil {
		t.Fatalf("open Badger: %v", err)
	}
	h := hasher.NewBlake3Hasher()
	casStore, err := cas.NewCAS(db, h, cas.Config{MaxObjectSize: 1024})
	if err != nil {
		t.Fatalf("create CAS: %v", err)
	}
	orphan, err := casStore.Put(ctx, []byte("persisted before manifest"))
	if err != nil {
		t.Fatalf("put orphan: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}

	db, err = storage.NewBadger(path)
	if err != nil {
		t.Fatalf("reopen Badger: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	casStore, _ = cas.NewCAS(db, h, cas.Config{MaxObjectSize: 1024})
	manifests, _ := manifeststore.NewManifestStore(db, h, manifeststore.Config{
		MaxManifestSize: 4096,
		MaxNameBytes:    128,
		MaxChunks:       32,
		MaxPageSize:     10,
	})
	gc, _ := maintenance.NewGarbageCollector(casStore, manifests, 2)
	report, err := gc.Run(ctx, false)
	if err != nil {
		t.Fatalf("GC after restart: %v", err)
	}
	if report.ChunksDeleted != 1 {
		t.Fatalf("expected persisted orphan deletion, got %+v", report)
	}
	if _, err := casStore.Get(ctx, orphan); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("orphan remains after GC: %v", err)
	}
}

func TestMaintenance_Cancellation(t *testing.T) {
	casStore, manifests, _ := setupMaintenance(t)
	gc, _ := maintenance.NewGarbageCollector(casStore, manifests, 2)
	scrubber, _ := maintenance.NewScrubber(casStore, manifests, 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gc.Run(ctx, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("GC returned %v, want context.Canceled", err)
	}
	if _, err := scrubber.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("scrubber returned %v, want context.Canceled", err)
	}
}
