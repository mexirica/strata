package storage_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mexirica/strata/internal/storage"
)

func TestBadger_PutIfNotExists_Concurrent(t *testing.T) {
	db, err := storage.NewInMemoryBadger()
	if err != nil {
		t.Fatalf("failed to open in-memory badger: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	key := []byte("test-key")
	val := []byte("test-value")

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	insertedCount := 0
	var mu sync.Mutex

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			inserted, err := db.PutIfNotExists(ctx, key, val)
			if err != nil {
				t.Errorf("PutIfNotExists error: %v", err)
				return
			}
			if inserted {
				mu.Lock()
				insertedCount++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if insertedCount != 1 {
		t.Fatalf("expected exactly 1 insert, got %d", insertedCount)
	}

	got, err := db.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(got) != string(val) {
		t.Fatalf("expected %s, got %s", val, got)
	}
}

func TestBadger_GetNotFound(t *testing.T) {
	db, err := storage.NewInMemoryBadger()
	if err != nil {
		t.Fatalf("failed to open in-memory badger: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Get(context.Background(), []byte("missing"))
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestBadger_PutIfNotExistsRejectsDifferentContent(t *testing.T) {
	db, err := storage.NewInMemoryBadger()
	if err != nil {
		t.Fatalf("failed to open in-memory badger: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if _, err := db.PutIfNotExists(ctx, []byte("key"), []byte("first")); err != nil {
		t.Fatalf("first PutIfNotExists failed: %v", err)
	}
	inserted, err := db.PutIfNotExists(ctx, []byte("key"), []byte("second"))
	if inserted || !errors.Is(err, storage.ErrContentMismatch) {
		t.Fatalf("expected non-inserted ErrContentMismatch, got inserted=%v err=%v", inserted, err)
	}
}

func TestBadger_OperationsAfterClose(t *testing.T) {
	db, err := storage.NewInMemoryBadger()
	if err != nil {
		t.Fatalf("failed to open in-memory badger: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	_, err = db.Get(context.Background(), []byte("key"))
	if !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

func TestBadger_ReopenPreservesData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "badger")
	ctx := context.Background()
	db, err := storage.NewBadger(path)
	if err != nil {
		t.Fatalf("failed to open badger: %v", err)
	}
	if err := db.Put(ctx, []byte("key"), []byte("value")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	db, err = storage.NewBadger(path)
	if err != nil {
		t.Fatalf("failed to reopen badger: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	value, err := db.Get(ctx, []byte("key"))
	if err != nil {
		t.Fatalf("Get after reopen failed: %v", err)
	}
	if string(value) != "value" {
		t.Fatalf("expected persisted value, got %q", value)
	}
}
