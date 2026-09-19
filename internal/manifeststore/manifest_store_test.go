package manifeststore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mexirica/strata/internal/cas"
	"github.com/mexirica/strata/internal/hasher"
	"github.com/mexirica/strata/internal/manifeststore"
	"github.com/mexirica/strata/internal/storage"
)

func setupManifestStore(t testing.TB) (*manifeststore.ManifestStore, storage.ObjectStorage) {
	t.Helper()
	db, err := storage.NewInMemoryBadger()
	if err != nil {
		t.Fatalf("failed to init in-memory badger: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := manifeststore.NewManifestStore(db, hasher.NewBlake3Hasher(), manifeststore.Config{
		MaxManifestSize: 1024 * 1024,
		MaxNameBytes:    1024,
		MaxChunks:       1024,
		MaxPageSize:     100,
	})
	if err != nil {
		t.Fatalf("failed to init manifest store: %v", err)
	}
	return store, db
}

func TestManifestStore_CRUD(t *testing.T) {
	store, _ := setupManifestStore(t)
	ctx := context.Background()

	var d1 [32]byte
	d1[0] = 1
	var d2 [32]byte
	d2[0] = 2
	cid1, err := cas.NewCID(cas.AlgBlake3, d1)
	if err != nil {
		t.Fatalf("NewCID failed: %v", err)
	}
	cid2, err := cas.NewCID(cas.AlgBlake3, d2)
	if err != nil {
		t.Fatalf("NewCID failed: %v", err)
	}

	manifest := manifeststore.FileManifest{
		Version: manifeststore.CurrentVersion,
		Name:    "test.pdf",
		Size:    1024,
		Chunks:  []cas.CID{cid1, cid2},
	}

	manifestCID, err := store.Put(ctx, manifest)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	duplicateCID, err := store.Put(ctx, manifest)
	if err != nil || duplicateCID != manifestCID {
		t.Fatalf("duplicate Put must return the same CID: cid=%v err=%v", duplicateCID, err)
	}

	got, err := store.Get(ctx, manifestCID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if got.Version != manifest.Version || got.Name != manifest.Name || got.Size != manifest.Size {
		t.Fatalf("mismatched manifest: got %+v, want %+v", got, manifest)
	}
	if len(got.Chunks) != 2 || got.Chunks[0] != manifest.Chunks[0] || got.Chunks[1] != manifest.Chunks[1] {
		t.Fatalf("mismatched chunks: got %+v, want %+v", got.Chunks, manifest.Chunks)
	}

	list, next, err := store.ListManifests(ctx, nil, 10)
	if err != nil {
		t.Fatalf("ListManifests failed: %v", err)
	}
	if len(list) != 1 || list[0].CID != manifestCID || next != nil {
		t.Fatalf("expected 1 manifest in list, got %+v", list)
	}

	if err := store.Delete(ctx, manifestCID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := store.Get(ctx, manifestCID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestManifestStore_ContentAddressedIdentity(t *testing.T) {
	store, _ := setupManifestStore(t)
	manifest := manifeststore.FileManifest{Version: manifeststore.CurrentVersion, Name: "a.txt", Size: 0}
	first, err := store.Put(context.Background(), manifest)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	manifest.Name = "b.txt"
	second, err := store.Put(context.Background(), manifest)
	if err != nil {
		t.Fatalf("renamed Put failed: %v", err)
	}
	if first == second {
		t.Fatal("renaming must change the manifest CID")
	}
	if got := first.String(); got != "1e205f18ae5d7af989d7d68c157bee10cc8bfe503bbb4f5deae151725aa3d68a9779" {
		t.Fatalf("canonical CID changed: got %s", got)
	}
}

func TestManifestStore_Pagination(t *testing.T) {
	store, _ := setupManifestStore(t)
	ctx := context.Background()
	for _, name := range []string{"one", "two", "three"} {
		if _, err := store.Put(ctx, manifeststore.FileManifest{Version: manifeststore.CurrentVersion, Name: name}); err != nil {
			t.Fatalf("Put(%s) failed: %v", name, err)
		}
	}
	first, cursor, err := store.ListManifests(ctx, nil, 2)
	if err != nil {
		t.Fatalf("first page failed: %v", err)
	}
	if len(first) != 2 || cursor == nil {
		t.Fatalf("expected full first page and cursor, got len=%d cursor=%v", len(first), cursor)
	}
	second, cursor, err := store.ListManifests(ctx, cursor, 2)
	if err != nil {
		t.Fatalf("second page failed: %v", err)
	}
	if len(second) != 1 || cursor != nil {
		t.Fatalf("expected final one-item page, got len=%d cursor=%v", len(second), cursor)
	}
}

func TestManifestStore_DetectsCorruption(t *testing.T) {
	store, raw := setupManifestStore(t)
	ctx := context.Background()
	valueCID, err := store.Put(ctx, manifeststore.FileManifest{Version: manifeststore.CurrentVersion, Name: "data"})
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	key := append([]byte("manifest:"), valueCID.Bytes()...)
	if err := raw.Put(ctx, key, []byte("tampered")); err != nil {
		t.Fatalf("tamper failed: %v", err)
	}
	if _, err := store.Get(ctx, valueCID); !errors.Is(err, manifeststore.ErrCorruptedManifest) {
		t.Fatalf("expected ErrCorruptedManifest, got %v", err)
	}
}

func FuzzManifestDecode(f *testing.F) {
	store, raw := setupManifestStore(f)
	manifestCID, err := store.Put(context.Background(), manifeststore.FileManifest{
		Version: manifeststore.CurrentVersion,
		Name:    "seed",
	})
	if err != nil {
		f.Fatalf("seed Put failed: %v", err)
	}
	valid, err := raw.Get(context.Background(), append([]byte("manifest:"), manifestCID.Bytes()...))
	if err != nil {
		f.Fatalf("seed Get failed: %v", err)
	}
	f.Add(valid)
	f.Add([]byte("strata-manifest\x00\x00\x01\xff\xff\xff\xff"))
	f.Add([]byte("strata-manifest\x00\x00\x01\x00\x00\x00\x01a\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff"))

	h := hasher.NewBlake3Hasher()
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 2*1024*1024 {
			t.Skip()
		}
		valueCID := h.Hash(data)
		key := append([]byte("manifest:"), valueCID.Bytes()...)
		if err := raw.Put(context.Background(), key, data); err != nil {
			t.Fatalf("raw Put failed: %v", err)
		}
		manifest, err := store.Get(context.Background(), valueCID)
		if err == nil && manifest.Version != manifeststore.CurrentVersion {
			t.Fatalf("decoded unsupported version %d", manifest.Version)
		}
	})
}
