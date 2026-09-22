package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
