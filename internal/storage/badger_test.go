package storage_test

import (
	"bytes"
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

func TestBadger_ListKeysPage(t *testing.T) {
	db, err := storage.NewInMemoryBadger()
	if err != nil {
		t.Fatalf("failed to open in-memory badger: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	for _, key := range []string{"chunk:a", "chunk:b", "chunk:c", "other:a"} {
		if err := db.Put(ctx, []byte(key), []byte("large value that must not affect key enumeration")); err != nil {
			t.Fatalf("Put(%q) failed: %v", key, err)
		}
	}

	first, next, err := db.ListKeysPage(ctx, []byte("chunk:"), nil, 2)
	if err != nil {
		t.Fatalf("first page failed: %v", err)
	}
	if len(first) != 2 || string(first[0]) != "chunk:a" || string(first[1]) != "chunk:b" || string(next) != "chunk:b" {
		t.Fatalf("unexpected first page: keys=%q next=%q", first, next)
	}
	second, next, err := db.ListKeysPage(ctx, []byte("chunk:"), next, 2)
	if err != nil {
		t.Fatalf("second page failed: %v", err)
	}
	if len(second) != 1 || string(second[0]) != "chunk:c" || next != nil {
		t.Fatalf("unexpected second page: keys=%q next=%q", second, next)
	}
}

func FuzzBadgerListKeysPageCursor(f *testing.F) {
	db, err := storage.NewInMemoryBadger()
	if err != nil {
		f.Fatalf("failed to open in-memory badger: %v", err)
	}
	f.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	for _, key := range [][]byte{[]byte("chunk:a"), []byte("chunk:b"), []byte("chunk:\xff")} {
		if err := db.Put(ctx, key, []byte("value")); err != nil {
			f.Fatalf("Put failed: %v", err)
		}
	}
	f.Add([]byte(nil), uint8(1))
	f.Add([]byte("chunk:a"), uint8(2))
	f.Add([]byte("unrelated"), uint8(3))

	f.Fuzz(func(t *testing.T, after []byte, rawLimit uint8) {
		limit := int(rawLimit%8) + 1
		keys, _, err := db.ListKeysPage(ctx, []byte("chunk:"), after, limit)
		if err != nil {
			t.Fatalf("ListKeysPage failed: %v", err)
		}
		for index, key := range keys {
			if !bytes.HasPrefix(key, []byte("chunk:")) {
				t.Fatalf("key %q does not have requested prefix", key)
			}
			if index > 0 && bytes.Compare(keys[index-1], key) >= 0 {
				t.Fatalf("keys are not strictly ordered: %q", keys)
			}
		}
	})
}
