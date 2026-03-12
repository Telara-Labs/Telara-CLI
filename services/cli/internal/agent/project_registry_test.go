package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectRegistry(t *testing.T) {
	// Use a temp dir as home so ConfigDir resolves inside it.
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, ".config"))

	// Register a project.
	if err := RegisterProject("/tmp/my-project", "claude-code"); err != nil {
		t.Fatalf("RegisterProject: %v", err)
	}

	// List should return it.
	projects, err := ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
	if projects[0].Path != "/tmp/my-project" {
		t.Fatalf("path = %q, want /tmp/my-project", projects[0].Path)
	}
	if len(projects[0].Tools) != 1 || projects[0].Tools[0] != "claude-code" {
		t.Fatalf("tools = %v, want [claude-code]", projects[0].Tools)
	}

	// Register same project with a different tool.
	if err := RegisterProject("/tmp/my-project", "cursor"); err != nil {
		t.Fatalf("RegisterProject (second tool): %v", err)
	}

	projects, _ = ListProjects()
	if len(projects) != 1 {
		t.Fatalf("expected 1 project after adding tool, got %d", len(projects))
	}
	if len(projects[0].Tools) != 2 {
		t.Fatalf("expected 2 tools, got %v", projects[0].Tools)
	}

	// Register same project+tool again (idempotent).
	if err := RegisterProject("/tmp/my-project", "cursor"); err != nil {
		t.Fatalf("RegisterProject (duplicate): %v", err)
	}
	projects, _ = ListProjects()
	if len(projects[0].Tools) != 2 {
		t.Fatalf("expected still 2 tools after duplicate, got %v", projects[0].Tools)
	}

	// Unregister.
	if err := UnregisterProject("/tmp/my-project"); err != nil {
		t.Fatalf("UnregisterProject: %v", err)
	}
	projects, _ = ListProjects()
	if len(projects) != 0 {
		t.Fatalf("expected 0 projects after unregister, got %d", len(projects))
	}
}

func TestProjectRegistryFileFormat(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, ".config"))

	_ = RegisterProject("/tmp/proj", "vscode")

	// Verify the file is valid JSON.
	registryPath, err := projectRegistryPath()
	if err != nil {
		t.Fatalf("registry path: %v", err)
	}
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	var records []ProjectRecord
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record in file, got %d", len(records))
	}
	if records[0].UpdatedAt == "" {
		t.Fatal("expected UpdatedAt to be set")
	}
}
