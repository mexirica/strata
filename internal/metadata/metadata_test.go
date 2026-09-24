package metadata_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mexirica/strata/internal/buildinfo"
	"github.com/mexirica/strata/internal/chunker"
	"github.com/mexirica/strata/internal/metadata"
	"github.com/mexirica/strata/internal/storage"
)

func validMetadata() metadata.Metadata {
	return metadata.NewMetadata(
		time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC),
		buildinfo.Version,
		metadata.Chunking{
			Algorithm:  chunker.FastCDC,
			MinSize:    64,
			NormalSize: 128,
			MaxSize:    256,
		},
	)
}

func newStore(t *testing.T) (*metadata.Store, *storage.Badger) {
	t.Helper()
	db, err := storage.NewInMemoryBadger()
	if err != nil {
		t.Fatalf("NewInMemoryBadger: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := metadata.NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store, db
}

func TestStore_LoadOrCreateCreatesMetadataInEmptyRepository(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	candidate := validMetadata()

	got, err := store.LoadOrCreate(ctx, candidate)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if got != candidate {
		t.Fatalf("LoadOrCreate returned %+v, want %+v", got, candidate)
	}

	loaded, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded != candidate {
		t.Fatalf("Load returned %+v, want %+v", loaded, candidate)
	}
}

func TestStore_LoadOrCreateRejectsLegacyRepository(t *testing.T) {
	store, db := newStore(t)
	ctx := context.Background()
	if err := db.Put(ctx, []byte("chunk:legacy"), []byte("data")); err != nil {
		t.Fatalf("seed legacy object: %v", err)
	}

	if _, err := store.LoadOrCreate(ctx, validMetadata()); !errors.Is(err, metadata.ErrLegacyRepository) {
		t.Fatalf("LoadOrCreate returned %v, want ErrLegacyRepository", err)
	}
	if _, err := store.Load(ctx); !errors.Is(err, metadata.ErrRepositoryMetadataMissing) {
		t.Fatalf("Load after rejection returned %v, want ErrRepositoryMetadataMissing", err)
	}
}

func TestStore_CreateDoesNotOverwriteMetadata(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	original := validMetadata()
	if err := store.Create(ctx, original); err != nil {
		t.Fatalf("Create original: %v", err)
	}

	changed := original
	changed.AppVersion = "different-version"
	if err := store.Create(ctx, changed); !errors.Is(err, storage.ErrContentMismatch) {
		t.Fatalf("Create changed metadata returned %v, want ErrContentMismatch", err)
	}
}

func TestStore_LoadRejectsUnsupportedVersion(t *testing.T) {
	store, db := newStore(t)
	ctx := context.Background()
	if err := db.Put(ctx, []byte("repository:metadata"), []byte(`{"format_version":2}`)); err != nil {
		t.Fatalf("seed metadata: %v", err)
	}

	if _, err := store.Load(ctx); !errors.Is(err, metadata.ErrUnsupportedRepositoryVersion) {
		t.Fatalf("Load returned %v, want ErrUnsupportedRepositoryVersion", err)
	}
}

func TestStore_LoadRejectsCorruptedMetadata(t *testing.T) {
	store, db := newStore(t)
	ctx := context.Background()
	if err := db.Put(ctx, []byte("repository:metadata"), []byte("{")); err != nil {
		t.Fatalf("seed metadata: %v", err)
	}

	if _, err := store.Load(ctx); !errors.Is(err, metadata.ErrCorruptedRepositoryMetadata) {
		t.Fatalf("Load returned %v, want ErrCorruptedRepositoryMetadata", err)
	}
}
