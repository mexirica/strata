package node_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mexirica/strata/internal/chunker"
	"github.com/mexirica/strata/internal/cid"
	"github.com/mexirica/strata/internal/metadata"
	"github.com/mexirica/strata/internal/node"
	"github.com/mexirica/strata/internal/storage"
)

func validConfig(dataDir string) node.Config {
	return node.Config{
		DataDir:       dataDir,
		HashAlgorithm: cid.AlgBlake3,
		MaxFileSize:   512,
		MaxChunks:     8,
		MaxNameBytes:  128,
	}
}

func validInitConfig(dataDir string) node.InitConfig {
	return node.InitConfig{
		Config: validConfig(dataDir),
		Chunking: chunker.Config{
			Algorithm:  chunker.FastCDC,
			MinSize:    64,
			NormalSize: 128,
			MaxSize:    256,
		},
	}
}

func TestNew_RejectsIncompatibleConfigBeforeOpeningStorage(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "must-not-exist")
	config := validConfig(dataDir)
	config.MaxChunks = 0
	if _, err := node.Open(context.Background(), config); !errors.Is(err, node.ErrInvalidConfig) {
		t.Fatalf("Open returned %v, want ErrInvalidConfig", err)
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid config touched data directory: %v", err)
	}
}

func TestNew_RejectsLegacyRepositoryAndClosesStorage(t *testing.T) {
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	db, err := storage.NewBadger(dataDir)
	if err != nil {
		t.Fatalf("NewBadger: %v", err)
	}
	if err := db.Put(ctx, []byte("chunk:legacy"), []byte("data")); err != nil {
		t.Fatalf("seed legacy object: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seeded storage: %v", err)
	}

	if _, err := node.Init(ctx, validInitConfig(dataDir)); !errors.Is(err, metadata.ErrLegacyRepository) {
		t.Fatalf("Init returned %v, want ErrLegacyRepository", err)
	}

	reopened, err := storage.NewBadger(dataDir)
	if err != nil {
		t.Fatalf("storage remained locked after New failed: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened storage: %v", err)
	}
}

func TestInitRejectsIncompatiblePersistedChunking(t *testing.T) {
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	n, err := node.Init(ctx, validInitConfig(dataDir))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := n.Close(); err != nil {
		t.Fatalf("close initialized node: %v", err)
	}

	changed := validInitConfig(dataDir)
	changed.Chunking.NormalSize = 256
	changed.Chunking.MaxSize = 512
	_, err = node.Init(ctx, changed)
	if !errors.Is(err, metadata.ErrIncompatibleRepository) {
		t.Fatalf("Init returned %v, want ErrIncompatibleRepository", err)
	}
	if !strings.Contains(err.Error(), "chunking:") || !strings.Contains(err.Error(), "repository=") || !strings.Contains(err.Error(), "requested=") {
		t.Fatalf("incompatibility error does not identify chunking values: %v", err)
	}
}

func TestRepeatedInitReportsChunkingMismatchBeforeRequestedWriteLimits(t *testing.T) {
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	initial := validInitConfig(dataDir)
	initial.Chunking.MinSize = 512
	initial.Chunking.NormalSize = 1024
	initial.Chunking.MaxSize = 2048
	initial.MaxChunks = 1
	initial.MaxFileSize = 512
	n, err := node.Init(ctx, initial)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := n.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	repeated := validInitConfig(dataDir)
	repeated.MaxChunks = 1
	repeated.MaxFileSize = 512
	_, err = node.Init(ctx, repeated)
	if !errors.Is(err, metadata.ErrIncompatibleRepository) {
		t.Fatalf("repeated Init returned %v, want ErrIncompatibleRepository", err)
	}
	if errors.Is(err, node.ErrInvalidConfig) {
		t.Fatalf("repeated Init masked chunking mismatch with invalid config: %v", err)
	}
}

func TestOpenUsesPersistedChunkingAndReadsOldCIDAlgorithm(t *testing.T) {
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	n, err := node.Init(ctx, validInitConfig(dataDir))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	content := bytes.Repeat([]byte("strata"), 40)
	oldCID, err := n.Store(ctx, "old.bin", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store with BLAKE3: %v", err)
	}
	if err := n.Close(); err != nil {
		t.Fatalf("close initialized node: %v", err)
	}

	openConfig := validConfig(dataDir)
	openConfig.HashAlgorithm = cid.AlgSHA256
	reopened, err := node.Open(ctx, openConfig)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reader, _, err := reopened.Retrieve(ctx, oldCID)
	if err != nil {
		t.Fatalf("Retrieve old CID: %v", err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll old CID: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("reopened node returned different content")
	}
	newCID, err := reopened.Store(ctx, "new.bin", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store with SHA-256: %v", err)
	}
	if oldCID.Algorithm() != cid.AlgBlake3 || newCID.Algorithm() != cid.AlgSHA256 {
		t.Fatalf("CID algorithms old=%v new=%v", oldCID.Algorithm(), newCID.Algorithm())
	}
}

func TestOpenRejectsWriteLimitsChunkingCannotRepresent(t *testing.T) {
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	n, err := node.Init(ctx, validInitConfig(dataDir))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := n.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	config := validConfig(dataDir)
	config.MaxChunks = 1
	config.MaxFileSize = 65
	_, err = node.Open(ctx, config)
	if !errors.Is(err, node.ErrInvalidConfig) {
		t.Fatalf("Open returned %v, want ErrInvalidConfig", err)
	}
	for _, value := range []string{"max_file_size=65", "max_chunks=1", "min_chunk_size=64"} {
		if !strings.Contains(err.Error(), value) {
			t.Fatalf("Open error %q does not contain %q", err, value)
		}
	}
}

func TestFailedInitDoesNotPersistChunking(t *testing.T) {
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	invalid := validInitConfig(dataDir)
	invalid.MaxFileSize = int64(invalid.MaxChunks*invalid.Chunking.MinSize) + 1

	if _, err := node.Init(ctx, invalid); !errors.Is(err, node.ErrInvalidConfig) {
		t.Fatalf("Init returned %v, want ErrInvalidConfig", err)
	}

	retry := validInitConfig(dataDir)
	retry.Chunking.MinSize = 128
	retry.Chunking.NormalSize = 256
	retry.Chunking.MaxSize = 512
	n, err := node.Init(ctx, retry)
	if err != nil {
		t.Fatalf("retry Init: %v", err)
	}
	if err := n.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestNode_StoreRetrieveDeleteGCAndScrub(t *testing.T) {
	n, err := node.Init(context.Background(), validInitConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	ctx := context.Background()
	content := bytes.Repeat([]byte("strata"), 40)
	manifestCID, err := n.Store(ctx, "data.bin", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	reader, manifest, err := n.Retrieve(ctx, manifestCID)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	retrieved, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	if !bytes.Equal(retrieved, content) || manifest.Size != int64(len(content)) {
		t.Fatalf("retrieved file mismatch: size=%d", manifest.Size)
	}
	issues, err := n.Scrub(ctx)
	if err != nil || len(issues) != 0 {
		t.Fatalf("healthy scrub returned issues=%+v err=%v", issues, err)
	}

	if err := n.Delete(ctx, manifestCID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := n.Retrieve(ctx, manifestCID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Retrieve after delete returned %v, want ErrNotFound", err)
	}
	dryReport, err := n.RunGC(ctx, true)
	if err != nil || dryReport.ChunksDeleted == 0 {
		t.Fatalf("dry-run did not find retained orphan chunks: report=%+v err=%v", dryReport, err)
	}
	report, err := n.RunGC(ctx, false)
	if err != nil || report.ChunksDeleted != dryReport.ChunksDeleted {
		t.Fatalf("GC report mismatch: dry=%+v actual=%+v err=%v", dryReport, report, err)
	}
}

func TestNode_GCWaitsForOpenRetrieval(t *testing.T) {
	n, err := node.Init(context.Background(), validInitConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	manifestCID, err := n.Store(context.Background(), "data.bin", bytes.NewReader(bytes.Repeat([]byte("x"), 300)))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	reader, _, err := n.Retrieve(context.Background(), manifestCID)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := n.RunGC(context.Background(), false)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("GC completed while retrieval was open: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("GC after reader close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("GC remained blocked after reader close")
	}
}

func TestNode_GCConcurrentWithStoreRetrieveAndDelete(t *testing.T) {
	n, err := node.Init(context.Background(), validInitConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	ctx := context.Background()
	deleteCID, err := n.Store(ctx, "delete.bin", bytes.NewReader(bytes.Repeat([]byte("d"), 300)))
	if err != nil {
		t.Fatalf("store delete target: %v", err)
	}
	retrieveCID, err := n.Store(ctx, "retrieve.bin", bytes.NewReader(bytes.Repeat([]byte("r"), 300)))
	if err != nil {
		t.Fatalf("store retrieve target: %v", err)
	}

	start := make(chan struct{})
	errorsFound := make(chan error, 4)
	var wait sync.WaitGroup
	wait.Add(4)
	go func() {
		defer wait.Done()
		<-start
		_, err := n.Store(ctx, "new.bin", bytes.NewReader(bytes.Repeat([]byte("n"), 300)))
		errorsFound <- err
	}()
	go func() {
		defer wait.Done()
		<-start
		reader, _, err := n.Retrieve(ctx, retrieveCID)
		if err == nil {
			_, err = io.ReadAll(reader)
			closeErr := reader.Close()
			if err == nil {
				err = closeErr
			}
		}
		errorsFound <- err
	}()
	go func() {
		defer wait.Done()
		<-start
		errorsFound <- n.Delete(ctx, deleteCID)
	}()
	go func() {
		defer wait.Done()
		<-start
		_, err := n.RunGC(ctx, false)
		errorsFound <- err
	}()
	close(start)
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("concurrent operation failed: %v", err)
		}
	}
	if _, err := n.RunGC(ctx, false); err != nil {
		t.Fatalf("final GC: %v", err)
	}
	issues, err := n.Scrub(ctx)
	if err != nil || len(issues) != 0 {
		t.Fatalf("post-concurrency scrub returned issues=%+v err=%v", issues, err)
	}
}
