package fileservice_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"testing"

	"github.com/mexirica/strata/internal/cas"
	"github.com/mexirica/strata/internal/chunker"
	"github.com/mexirica/strata/internal/fileservice"
	"github.com/mexirica/strata/internal/hasher"
	"github.com/mexirica/strata/internal/manifeststore"
	"github.com/mexirica/strata/internal/storage"
)

func setupFileService(t *testing.T) (*fileservice.FileService, *manifeststore.ManifestStore) {
	t.Helper()
	db, err := storage.NewInMemoryBadger()
	if err != nil {
		t.Fatalf("failed to init in-memory badger: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	h := hasher.NewBlake3Hasher()
	casStore, err := cas.NewCAS(db, h, cas.Config{MaxObjectSize: 256 * 1024})
	if err != nil {
		t.Fatalf("failed to create CAS: %v", err)
	}
	manifestStore, err := manifeststore.NewManifestStore(db, h, manifeststore.Config{
		MaxManifestSize: 1024 * 1024,
		MaxNameBytes:    1024,
		MaxChunks:       1024,
		MaxPageSize:     100,
	})
	if err != nil {
		t.Fatalf("failed to create manifest store: %v", err)
	}

	cdc, err := chunker.NewCDC(chunker.Config{
		Algorithm:  chunker.FastCDC,
		MinSize:    64 * 1024,
		NormalSize: 128 * 1024,
		MaxSize:    256 * 1024,
	})
	if err != nil {
		t.Fatalf("failed to create cdc: %v", err)
	}

	fs, err := fileservice.NewFileService(cdc, casStore, manifestStore, fileservice.Config{
		MaxFileSize:  2 * 1024 * 1024,
		MaxChunks:    1024,
		MaxNameBytes: 1024,
	})
	if err != nil {
		t.Fatalf("failed to create file service: %v", err)
	}
	return fs, manifestStore
}

func TestFileService_StoreAndRetrieve_Roundtrip(t *testing.T) {
	fs, _ := setupFileService(t)
	ctx := context.Background()

	// 512 KB payload with repeating patterns to test CDC and deduplication
	pattern := make([]byte, 64*1024)
	_, _ = rand.Read(pattern)
	var original bytes.Buffer
	for i := 0; i < 8; i++ {
		original.Write(pattern)
	}

	fileName := "report.pdf"
	manifestID, err := fs.Store(ctx, fileName, bytes.NewReader(original.Bytes()))
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	rc, manifest, err := fs.Retrieve(ctx, manifestID)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}
	defer rc.Close()

	if manifest.Name != fileName {
		t.Errorf("expected file name %s, got %s", fileName, manifest.Name)
	}
	if manifest.Size != int64(original.Len()) {
		t.Errorf("expected file size %d, got %d", original.Len(), manifest.Size)
	}
	if len(manifest.Chunks) == 0 {
		t.Fatal("expected chunks in manifest, got 0")
	}

	retrieved, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll from retrieve pipe failed: %v", err)
	}

	if !bytes.Equal(original.Bytes(), retrieved) {
		t.Fatalf("retrieved bytes do not match original! got len %d, want %d", len(retrieved), original.Len())
	}

	// Storing the same logical file is content-addressed and idempotent.
	manifestID2, err := fs.Store(ctx, fileName, bytes.NewReader(original.Bytes()))
	if err != nil {
		t.Fatalf("second Store failed: %v", err)
	}
	if manifestID2 != manifestID {
		t.Error("identical manifests must have the same CID")
	}
}

func TestFileService_StoreRejectsLimits(t *testing.T) {
	fs, _ := setupFileService(t)
	ctx := context.Background()

	if _, err := fs.Store(ctx, "", bytes.NewReader(nil)); !errors.Is(err, fileservice.ErrInvalidName) {
		t.Fatalf("expected ErrInvalidName, got %v", err)
	}
	tooLarge := bytes.NewReader(make([]byte, 3*1024*1024))
	if _, err := fs.Store(ctx, "large.bin", tooLarge); !errors.Is(err, fileservice.ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}
}

func TestFileService_Retrieve_EarlyClose(t *testing.T) {
	fs, _ := setupFileService(t)
	ctx := context.Background()

	payload := bytes.Repeat([]byte("test payload for early close verification "), 10000)
	manifestID, err := fs.Store(ctx, "large.txt", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	rc, _, err := fs.Retrieve(ctx, manifestID)
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}

	// Read only 100 bytes and close early (must not deadlock or hang)
	buf := make([]byte, 100)
	n, err := io.ReadFull(rc, buf)
	if err != nil || n != 100 {
		t.Fatalf("read failed: %v", err)
	}

	if err := rc.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
}
