package node_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mexirica/strata/internal/cid"
	"github.com/mexirica/strata/internal/node"
	"github.com/mexirica/strata/internal/storage"
)

func validConfig(dataDir string) node.Config {
	return node.Config{
		DataDir:         dataDir,
		HashAlgorithm:   cid.AlgBlake3,
		MinChunkSize:    64,
		NormalChunkSize: 128,
		MaxChunkSize:    256,
		MaxFileSize:     512,
		MaxChunks:       8,
		MaxNameBytes:    128,
	}
}

func TestNew_RejectsIncompatibleConfigBeforeOpeningStorage(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "must-not-exist")
	config := validConfig(dataDir)
	config.MaxChunks = 1
	if _, err := node.New(config); !errors.Is(err, node.ErrInvalidConfig) {
		t.Fatalf("New returned %v, want ErrInvalidConfig", err)
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid config touched data directory: %v", err)
	}
}

func TestNode_StoreRetrieveDeleteGCAndScrub(t *testing.T) {
	n, err := node.New(validConfig(filepath.Join(t.TempDir(), "data")))
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
	n, err := node.New(validConfig(filepath.Join(t.TempDir(), "data")))
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
	n, err := node.New(validConfig(filepath.Join(t.TempDir(), "data")))
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
