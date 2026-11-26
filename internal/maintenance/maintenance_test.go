package maintenance_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mexirica/strata/internal/content/cas"
	"github.com/mexirica/strata/internal/content/cid"
	"github.com/mexirica/strata/internal/content/hasher"
	"github.com/mexirica/strata/internal/file"
	"github.com/mexirica/strata/internal/maintenance"
	"github.com/mexirica/strata/internal/storage"
)

func setupMaintenance(t testing.TB) (*cas.CAS, *file.ManifestStore, *storage.Badger) {
	t.Helper()
	db, err := storage.NewInMemoryBadger()
	if err != nil {
		t.Fatalf("open Badger: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	casStore, manifests := newMaintenanceStores(t, db)
	return casStore, manifests, db
}

func newMaintenanceStores(t testing.TB, db storage.ObjectStorage) (*cas.CAS, *file.ManifestStore) {
	t.Helper()
	h := hasher.NewBlake3Hasher()
	casStore, err := cas.NewCAS(db, h, cas.Config{MaxObjectSize: 1024})
	if err != nil {
		t.Fatalf("create CAS: %v", err)
	}
	manifests, err := file.NewManifestStore(db, h, file.ManifestConfig{
		MaxManifestSize: 4096,
		MaxNameBytes:    128,
		MaxChunks:       32,
		MaxPageSize:     10,
	})
	if err != nil {
		t.Fatalf("create manifest store: %v", err)
	}
	return casStore, manifests
}

func TestGarbageCollector_PreservesSharedChunksAndRemovesOrphans(t *testing.T) {
	casStore, manifests, _ := setupMaintenance(t)
	ctx := context.Background()
	shared, _ := casStore.Put(ctx, []byte("shared"))
	firstOnly, _ := casStore.Put(ctx, []byte("first"))
	orphan, _ := casStore.Put(ctx, []byte("orphan"))
	_, err := manifests.Put(ctx, file.FileManifest{
		Version: file.CurrentVersion,
		Name:    "one",
		Size:    11,
		Chunks: []file.ChunkReference{
			{CID: shared, Size: 6},
			{CID: firstOnly, Size: 5}},
	})
	if err != nil {
		t.Fatalf("put first manifest: %v", err)
	}
	_, err = manifests.Put(ctx, file.FileManifest{Version: file.CurrentVersion,
		Name:   "two",
		Size:   6,
		Chunks: []file.ChunkReference{{CID: shared, Size: 6}}})

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
	manifestCID, err := manifests.Put(ctx, file.FileManifest{
		Version: file.CurrentVersion,
		Name:    "broken",
		Size:    19,
		Chunks: []file.ChunkReference{{CID: corrupt, Size: 10},
			{CID: missing, Size: 9}},
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
	manifestCID, err := manifests.Put(ctx, file.FileManifest{Version: file.CurrentVersion, Name: "corrupt"})
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
	manifests, _ := file.NewManifestStore(db, h, file.ManifestConfig{
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
	gc, err := maintenance.NewGarbageCollector(casStore, manifests, 2)
	if err != nil {
		t.Fatalf("NewGarbageCollector: %v", err)
	}
	scrubber, err := maintenance.NewScrubber(casStore, manifests, 2)
	if err != nil {
		t.Fatalf("NewScrubber: %v", err)
	}
	tests := []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "GC", run: func(ctx context.Context) error {
			_, err := gc.Run(ctx, false)
			return err
		}},
		{name: "scrub", run: func(ctx context.Context) error {
			_, err := scrubber.Run(ctx)
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := tc.run(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("Run returned %v, want context.Canceled", err)
			}
		})
	}
}

func TestMaintenance_RejectsInvalidManifestSizes(t *testing.T) {
	tests := []struct {
		name string
		size uint64
	}{
		{name: "zero reference size", size: 0},
		{name: "reference sum differs from total", size: 4},
		{name: "reference size overflows total", size: math.MaxUint64},
	}
	operations := []struct {
		name string
		gc   bool
	}{
		{name: "GC", gc: true},
		{name: "scrub"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, operation := range operations {
				t.Run(operation.name, func(t *testing.T) {
					casStore, manifests, raw := setupMaintenance(t)
					ctx := context.Background()
					retained, err := casStore.Put(ctx, []byte("valid"))
					if err != nil {
						t.Fatalf("put referenced chunk: %v", err)
					}
					orphan, err := casStore.Put(ctx, []byte("orphan"))
					if err != nil {
						t.Fatalf("put orphan: %v", err)
					}
					validCID, err := manifests.Put(ctx, file.FileManifest{
						Version: file.CurrentVersion, Name: "valid.bin", Size: 5,
						Chunks: []file.ChunkReference{{CID: retained, Size: 5}},
					})
					if err != nil {
						t.Fatalf("put valid manifest: %v", err)
					}
					encoded, err := raw.Get(ctx, append([]byte("manifest:"), validCID.Bytes()...))
					if err != nil {
						t.Fatalf("get encoded manifest: %v", err)
					}
					binary.BigEndian.PutUint64(encoded[len(encoded)-8:], tc.size)
					invalidCID := hasher.NewBlake3Hasher().Hash(encoded)
					invalidKey := append([]byte("manifest:"), invalidCID.Bytes()...)
					if err := raw.Put(ctx, invalidKey, encoded); err != nil {
						t.Fatalf("put rehashed invalid manifest: %v", err)
					}
					if operation.gc {
						gc, err := maintenance.NewGarbageCollector(casStore, manifests, 1)
						if err != nil {
							t.Fatalf("create GC: %v", err)
						}
						report, err := gc.Run(ctx, false)
						if !errors.Is(err, file.ErrInvalidManifest) {
							t.Fatalf("GC returned %v, want ErrInvalidManifest", err)
						}
						if report.ChunksScanned != 0 || report.ChunksDeleted != 0 {
							t.Fatalf("GC swept before validating every manifest: %+v", report)
						}
					} else {
						scrubber, err := maintenance.NewScrubber(casStore, manifests, 1)
						if err != nil {
							t.Fatalf("create scrubber: %v", err)
						}
						issues, err := scrubber.Run(ctx)
						if err != nil {
							t.Fatalf("scrub: %v", err)
						}
						if len(issues) != 1 || issues[0].Kind != maintenance.IssueCorruptedManifest {
							t.Fatalf("want exactly one invalid-manifest issue, got %+v", issues)
						}
						if issues[0].ManifestCID == nil || *issues[0].ManifestCID != invalidCID || !errors.Is(issues[0].Err, file.ErrInvalidManifest) {
							t.Fatalf("wrong invalid-manifest diagnostic: %+v", issues[0])
						}
					}
					for _, chunk := range []struct {
						cid  cid.CID
						data string
					}{{cid: retained, data: "valid"}, {cid: orphan, data: "orphan"}} {
						data, err := casStore.Get(ctx, chunk.cid)
						if err != nil || string(data) != chunk.data {
							t.Fatalf("maintenance modified chunk %s: data=%q err=%v", chunk.cid, data, err)
						}
					}
					stored, err := raw.Get(ctx, invalidKey)
					if err != nil || !bytes.Equal(stored, encoded) {
						t.Fatalf("maintenance modified invalid manifest: data=%x err=%v", stored, err)
					}
				})
			}
		})
	}
}

type maintenanceRead struct {
	operation string
	key       string
	limit     int
	count     int
	size      int
}

type recordingStorage struct {
	storage.ObjectStorage
	reads []maintenanceRead
}

func (s *recordingStorage) Get(ctx context.Context, key []byte) ([]byte, error) {
	value, err := s.ObjectStorage.Get(ctx, key)
	s.reads = append(s.reads, maintenanceRead{operation: "get", key: string(key), size: len(value)})
	return value, err
}

func (s *recordingStorage) ListPage(ctx context.Context, prefix, after []byte, limit int) ([]storage.Object, []byte, error) {
	objects, next, err := s.ObjectStorage.ListPage(ctx, prefix, after, limit)
	s.reads = append(s.reads, maintenanceRead{operation: "list values", key: string(prefix), limit: limit, count: len(objects)})
	return objects, next, err
}

func (s *recordingStorage) ListKeysPage(ctx context.Context, prefix, after []byte, limit int) ([][]byte, []byte, error) {
	keys, next, err := s.ObjectStorage.ListKeysPage(ctx, prefix, after, limit)
	s.reads = append(s.reads, maintenanceRead{operation: "list keys", key: string(prefix), limit: limit, count: len(keys)})
	return keys, next, err
}

func TestMaintenance_PagedTraversal(t *testing.T) {
	tests := []struct {
		name          string
		gc            bool
		manifestReads []string
		chunkPages    int
	}{
		{name: "GC marks all pages before key-only sweep", gc: true, manifestReads: []string{"list values manifest:"}, chunkPages: 7},
		{name: "scrub checks each page before fetching the next", manifestReads: []string{"list keys manifest:", "get manifest:", "get chunk:", "get chunk:"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, raw := setupMaintenance(t)
			tracked := &recordingStorage{ObjectStorage: raw}
			casStore, manifests := newMaintenanceStores(t, tracked)
			ctx := context.Background()
			for index := 0; index < 3; index++ {
				manifest := file.FileManifest{Version: file.CurrentVersion, Name: fmt.Sprintf("file-%d", index), Size: 1024}
				for chunk := 0; chunk < 2; chunk++ {
					chunkCID, err := casStore.Put(ctx, bytes.Repeat([]byte{byte(index*2 + chunk + 1)}, 512))
					if err != nil {
						t.Fatalf("put chunk: %v", err)
					}
					manifest.Chunks = append(manifest.Chunks, file.ChunkReference{CID: chunkCID, Size: 512})
				}
				if _, err := manifests.Put(ctx, manifest); err != nil {
					t.Fatalf("put manifest: %v", err)
				}
			}
			orphan, err := casStore.Put(ctx, bytes.Repeat([]byte{7}, 512))
			if err != nil {
				t.Fatalf("put orphan: %v", err)
			}
			tracked.reads = nil
			if tc.gc {
				gc, err := maintenance.NewGarbageCollector(casStore, manifests, 1)
				if err != nil {
					t.Fatalf("create GC: %v", err)
				}
				report, err := gc.Run(ctx, false)
				if err != nil {
					t.Fatalf("GC: %v", err)
				}
				if report.ManifestsScanned != 3 || report.ChunksReferenced != 6 || report.ChunksScanned != 7 || report.ChunksDeleted != 1 {
					t.Fatalf("unexpected paged GC report: %+v", report)
				}
			} else {
				scrubber, err := maintenance.NewScrubber(casStore, manifests, 1)
				if err != nil {
					t.Fatalf("create scrubber: %v", err)
				}
				issues, err := scrubber.Run(ctx)
				if err != nil || len(issues) != 0 {
					t.Fatalf("scrub returned issues=%+v err=%v", issues, err)
				}
			}
			var gotReads []string
			for _, read := range tracked.reads {
				prefix, _, _ := bytes.Cut([]byte(read.key), []byte(":"))
				gotReads = append(gotReads, read.operation+" "+string(prefix)+":")
				if read.operation != "get" && (read.limit != 1 || read.count != 1) {
					t.Fatalf("expected one object per bounded page, got %+v", read)
				}
				if read.operation == "get" && string(prefix) == "chunk" && read.size != 512 {
					t.Fatalf("expected individual 512-byte chunk reads, got %d bytes", read.size)
				}
			}
			var wantReads []string
			for index := 0; index < 3; index++ {
				wantReads = append(wantReads, tc.manifestReads...)
			}
			for index := 0; index < tc.chunkPages; index++ {
				wantReads = append(wantReads, "list keys chunk:")
			}
			if !reflect.DeepEqual(gotReads, wantReads) {
				t.Fatalf("unexpected traversal order:\n got %v\nwant %v", gotReads, wantReads)
			}
			_, err = raw.Get(ctx, append([]byte("chunk:"), orphan.Bytes()...))
			if tc.gc && !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("GC left orphan behind: %v", err)
			}
			if !tc.gc && err != nil {
				t.Fatalf("scrub removed orphan: %v", err)
			}
		})
	}
}

func TestScrubber_ChunkSizeMismatch(t *testing.T) {
	const firstChunk = "first chunk"
	const secondChunk = "second chunk"
	tests := []struct {
		name              string
		sizes             [2]uint64
		wantChunkIndexes  []int
		wantTotalMismatch bool
	}{
		{name: "correct sizes", sizes: [2]uint64{11, 12}},
		{name: "mismatched sizes with correct total", sizes: [2]uint64{12, 11}, wantChunkIndexes: []int{0, 1}},
		{name: "reference larger than chunk and wrong total", sizes: [2]uint64{12, 12}, wantChunkIndexes: []int{0}, wantTotalMismatch: true},
		{name: "reference smaller than chunk and wrong total", sizes: [2]uint64{10, 12}, wantChunkIndexes: []int{0}, wantTotalMismatch: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			casStore, manifests, _ := setupMaintenance(t)
			ctx := context.Background()
			first, err := casStore.Put(ctx, []byte(firstChunk))
			if err != nil {
				t.Fatalf("put first chunk: %v", err)
			}
			second, err := casStore.Put(ctx, []byte(secondChunk))
			if err != nil {
				t.Fatalf("put second chunk: %v", err)
			}
			manifestCID, err := manifests.Put(ctx, file.FileManifest{
				Version: file.CurrentVersion,
				Name:    "testChunk",
				Size:    int64(tc.sizes[0] + tc.sizes[1]),
				Chunks: []file.ChunkReference{
					{CID: first, Size: tc.sizes[0]},
					{CID: second, Size: tc.sizes[1]},
				},
			})
			if err != nil {
				t.Fatalf("put manifest: %v", err)
			}
			scrubber, err := maintenance.NewScrubber(casStore, manifests, 2)
			if err != nil {
				t.Fatalf("create scrubber: %v", err)
			}
			issues, err := scrubber.Run(ctx)
			if err != nil {
				t.Fatalf("scrub: %v", err)
			}
			wantCount := len(tc.wantChunkIndexes)
			if tc.wantTotalMismatch {
				wantCount++
			}
			if len(issues) != wantCount {
				t.Fatalf("got %d issues, want %d: %+v", len(issues), wantCount, issues)
			}
			chunkCIDs := []cid.CID{first, second}
			for index, issue := range issues {
				if issue.ManifestCID == nil || *issue.ManifestCID != manifestCID {
					t.Fatalf("issue %d has wrong manifest CID: %+v", index, issue)
				}
				if index == len(tc.wantChunkIndexes) {
					if issue.Kind != maintenance.IssueManifestSizeMismatch || issue.ChunkCID != nil {
						t.Fatalf("expected total mismatch without chunk CID: %+v", issue)
					}
					continue
				}
				if issue.Kind != maintenance.IssueChunkSizeMismatch {
					t.Fatalf("issue %d has unexpected kind: %+v", index, issue)
				}
				if !errors.Is(issue.Err, file.ErrChunkSizeMismatch) {
					t.Fatalf("issue %d returned %v, want ErrChunkSizeMismatch", index, issue.Err)
				}
				if issue.ChunkCID == nil || *issue.ChunkCID != chunkCIDs[tc.wantChunkIndexes[index]] {
					t.Fatalf("issue %d has wrong chunk CID: %+v", index, issue)
				}
			}
		})
	}
}
