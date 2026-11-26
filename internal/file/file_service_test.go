package file_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"testing"

	"github.com/mexirica/strata/internal/content/cas"
	"github.com/mexirica/strata/internal/content/chunker"
	"github.com/mexirica/strata/internal/content/hasher"
	"github.com/mexirica/strata/internal/file"
)

func setupFileService(t *testing.T) (*file.FileService, *file.ManifestStore) {
	t.Helper()
	manifestStore, db := setupManifestStore(t)
	h := hasher.NewBlake3Hasher()
	casStore, err := cas.NewCAS(db, h, cas.Config{MaxObjectSize: 256 * 1024})
	if err != nil {
		t.Fatalf("failed to create CAS: %v", err)
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

	fs, err := file.NewFileService(cdc, casStore, manifestStore, file.Config{
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
	tests := []struct {
		name        string
		fileName    string
		payloadSize int
		wantErr     error
	}{
		{name: "empty name", fileName: "", wantErr: file.ErrInvalidName},
		{name: "file too large", fileName: "large.bin", payloadSize: 3 * 1024 * 1024, wantErr: file.ErrFileTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs, _ := setupFileService(t)
			reader := bytes.NewReader(make([]byte, tc.payloadSize))
			if _, err := fs.Store(context.Background(), tc.fileName, reader); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Store returned %v, want %v", err, tc.wantErr)
			}
		})
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

func TestFileService_RetrieveValidatesChunkSizesBeforeWriting(t *testing.T) {
	tests := []struct {
		name          string
		mismatchIndex int
		smallerSize   bool
		wantErr       error
	}{
		{name: "correct sizes", mismatchIndex: -1},
		{name: "first chunk mismatch", mismatchIndex: 0, wantErr: file.ErrChunkSizeMismatch},
		{name: "first reference smaller than chunk", mismatchIndex: 0, smallerSize: true, wantErr: file.ErrChunkSizeMismatch},
		{name: "later chunk mismatch", mismatchIndex: 1, wantErr: file.ErrChunkSizeMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs, manifests := setupFileService(t)
			ctx := context.Background()
			payload := bytes.Repeat([]byte("x"), 768*1024)
			manifestCID, err := fs.Store(ctx, "multichunk.bin", bytes.NewReader(payload))
			if err != nil {
				t.Fatalf("Store: %v", err)
			}
			manifest, err := manifests.Get(ctx, manifestCID)
			if err != nil {
				t.Fatalf("Get manifest: %v", err)
			}
			if len(manifest.Chunks) < 3 {
				t.Fatalf("expected at least three chunks, got %d", len(manifest.Chunks))
			}
			wantBytes := len(payload)
			if tc.mismatchIndex >= 0 {
				wantBytes = 0
				for _, reference := range manifest.Chunks[:tc.mismatchIndex] {
					wantBytes += int(reference.Size)
				}
				if manifest.Chunks[tc.mismatchIndex+1].Size <= 1 {
					t.Fatal("compensating chunk must have more than one byte")
				}
				if tc.smallerSize {
					manifest.Chunks[tc.mismatchIndex].Size--
					manifest.Chunks[tc.mismatchIndex+1].Size++
				} else {
					manifest.Chunks[tc.mismatchIndex].Size++
					manifest.Chunks[tc.mismatchIndex+1].Size--
				}
				manifestCID, err = manifests.Put(ctx, manifest)
				if err != nil {
					t.Fatalf("Put mismatched references with correct total: %v", err)
				}
			}
			reader, _, err := fs.Retrieve(ctx, manifestCID)
			if err != nil {
				t.Fatalf("Retrieve: %v", err)
			}
			defer reader.Close()
			got, err := io.ReadAll(reader)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ReadAll returned %v, want %v", err, tc.wantErr)
			}
			if !bytes.Equal(got, payload[:wantBytes]) {
				t.Fatalf("read %d bytes, want exactly %d validated bytes", len(got), wantBytes)
			}
		})
	}
}
