package cas_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/mexirica/strata/internal/cas"
	"github.com/mexirica/strata/internal/cid"
	"github.com/mexirica/strata/internal/hasher"
	"github.com/mexirica/strata/internal/storage"
)

func setupCAS(t *testing.T) (*cas.CAS, storage.ObjectStorage) {
	t.Helper()
	db, err := storage.NewInMemoryBadger()
	if err != nil {
		t.Fatalf("failed to init in-memory badger: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	h := hasher.NewBlake3Hasher()
	c, err := cas.NewCAS(db, h, cas.Config{
		MaxObjectSize: 4 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("failed to init CAS: %v", err)
	}
	return c, db
}

func TestCAS_PutAndGet(t *testing.T) {
	c, _ := setupCAS(t)
	ctx := context.Background()

	payload := []byte("content addressable storage in Go")
	chunkCID, err := c.Put(ctx, payload)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	if chunkCID.Algorithm() != cid.AlgBlake3 {
		t.Errorf("expected algorithm %v, got %v", cid.AlgBlake3, chunkCID.Algorithm())
	}

	got, err := c.Get(ctx, chunkCID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if !bytes.Equal(got, payload) {
		t.Errorf("expected %s, got %s", payload, got)
	}
}

func TestCAS_GetUsesCIDAlgorithm(t *testing.T) {
	db, err := storage.NewInMemoryBadger()
	if err != nil {
		t.Fatalf("failed to init in-memory badger: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	config := cas.Config{MaxObjectSize: 1024}
	shaCAS, err := cas.NewCAS(db, hasher.NewSha256Hasher(), config)
	if err != nil {
		t.Fatalf("failed to init SHA-256 CAS: %v", err)
	}
	blakeCAS, err := cas.NewCAS(db, hasher.NewBlake3Hasher(), config)
	if err != nil {
		t.Fatalf("failed to init BLAKE3 CAS: %v", err)
	}

	payload := []byte("cross-algorithm content")
	shaCID, err := shaCAS.Put(context.Background(), payload)
	if err != nil {
		t.Fatalf("SHA-256 Put failed: %v", err)
	}
	got, err := blakeCAS.Get(context.Background(), shaCID)
	if err != nil {
		t.Fatalf("cross-algorithm Get failed: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("expected %q, got %q", payload, got)
	}
}

func TestCAS_Put_ConcurrentRace(t *testing.T) {
	c, _ := setupCAS(t)
	ctx := context.Background()

	payload := []byte("concurrent deduplication chunk")

	const goroutines = 25
	var wg sync.WaitGroup
	wg.Add(goroutines)

	cids := make([]cid.CID, goroutines)
	errorsList := make([]error, goroutines)

	for i := 0; i < goroutines; i++ {
		idx := i
		go func() {
			defer wg.Done()
			cid, err := c.Put(ctx, payload)
			cids[idx] = cid
			errorsList[idx] = err
		}()
	}

	wg.Wait()

	for i, err := range errorsList {
		if err != nil {
			t.Fatalf("goroutine %d failed: %v", i, err)
		}
		if cids[i] != cids[0] {
			t.Fatalf("goroutine %d got different CID: %v vs %v", i, cids[i], cids[0])
		}
	}
}

func TestCAS_IntegrityCorruptionDetection(t *testing.T) {
	c, store := setupCAS(t)
	ctx := context.Background()

	payload := []byte("tamper proof chunk")
	cid, err := c.Put(ctx, payload)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Corrupt chunk directly in underlying storage
	key := append([]byte("chunk:"), cid.Bytes()...)
	if err := store.Put(ctx, key, []byte("tampered content!")); err != nil {
		t.Fatalf("direct storage tampering failed: %v", err)
	}

	// Read via CAS should detect hash mismatch
	_, err = c.Get(ctx, cid)
	if !errors.Is(err, cas.ErrCorruptedChunk) {
		t.Fatalf("expected ErrCorruptedChunk, got: %v", err)
	}
}
