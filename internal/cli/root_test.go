package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mexirica/strata/internal/metadata"
	"github.com/mexirica/strata/internal/storage"
)

func TestCLIWorkflow(t *testing.T) {
	temporaryDir := t.TempDir()
	configPath := filepath.Join(temporaryDir, "strata.yaml")
	inputPath := filepath.Join(temporaryDir, "hello.txt")
	content := []byte("content stored by strata")
	if err := os.WriteFile(inputPath, content, 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}

	initOutput := executeCommand(t, "--config", configPath, "init")
	if !strings.Contains(initOutput, "initialized repository") {
		t.Fatalf("init output = %q", initOutput)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config file was not created: %v", err)
	}
	generatedConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	if strings.Contains(string(generatedConfig), "chunk_size") {
		t.Fatalf("operational config contains persisted chunking: %s", generatedConfig)
	}

	manifestCID := strings.TrimSpace(executeCommand(t, "--config", configPath, "add", inputPath))
	if len(manifestCID) != 68 {
		t.Fatalf("add returned invalid CID %q", manifestCID)
	}

	listOutput := executeCommand(t, "--config", configPath, "list")
	if !strings.Contains(listOutput, manifestCID) || !strings.Contains(listOutput, "24 B") || !strings.Contains(listOutput, "hello.txt") {
		t.Fatalf("list output = %q", listOutput)
	}

	destination := filepath.Join(temporaryDir, "retrieved.txt")
	executeCommand(t, "--config", configPath, "get", manifestCID, destination)
	retrieved, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if !bytes.Equal(retrieved, content) {
		t.Fatalf("retrieved content = %q, want %q", retrieved, content)
	}

	executeCommand(t, "--config", configPath, "remove", manifestCID)
	if output := executeCommand(t, "--config", configPath, "list"); strings.Contains(output, manifestCID) {
		t.Fatalf("removed CID remains in list: %q", output)
	}
	gcOutput := executeCommand(t, "--config", configPath, "gc", "--dry-run")
	if !strings.Contains(gcOutput, "deleted=1") || !strings.Contains(gcOutput, "dry_run=true") {
		t.Fatalf("gc output = %q", gcOutput)
	}
	if output := executeCommand(t, "--config", configPath, "scrub"); output != "issues=0\n" {
		t.Fatalf("scrub output = %q", output)
	}
}

func TestInitUsesChunkingEnvironmentOverrides(t *testing.T) {
	t.Setenv("STRATA_MIN_CHUNK_SIZE", "524288")
	t.Setenv("STRATA_NORMAL_CHUNK_SIZE", "2097152")
	t.Setenv("STRATA_MAX_CHUNK_SIZE", "4194304")
	temporaryDir := t.TempDir()
	configPath := filepath.Join(temporaryDir, "strata.yaml")
	executeCommand(t, "--config", configPath, "init", "--normal-chunk-size", "1048576")

	db, err := storage.NewBadger(filepath.Join(temporaryDir, ".strata", "data"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := metadata.NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	stored, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load metadata: %v", err)
	}
	if stored.Chunking.MinSize != 524288 || stored.Chunking.NormalSize != 1048576 || stored.Chunking.MaxSize != 4194304 {
		t.Fatalf("persisted chunking = %+v", stored.Chunking)
	}
}

func TestRepeatedInitReportsIncompatibleChunking(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "strata.yaml")
	executeCommand(t, "--config", configPath, "init")

	command := NewRootCommand()
	command.SetArgs([]string{"--config", configPath, "init", "--normal-chunk-size", "2097152"})
	err := command.Execute()
	if !errors.Is(err, metadata.ErrIncompatibleRepository) {
		t.Fatalf("repeated init returned %v, want ErrIncompatibleRepository", err)
	}
	if !strings.Contains(err.Error(), "chunking:") || !strings.Contains(err.Error(), "requested=") {
		t.Fatalf("repeated init error does not identify incompatible values: %v", err)
	}
}

func executeCommand(t *testing.T, args ...string) string {
	t.Helper()
	command := NewRootCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(args)
	if err := command.Execute(); err != nil {
		t.Fatalf("strata %s: %v\noutput: %s", strings.Join(args, " "), err, output.String())
	}
	return output.String()
}
