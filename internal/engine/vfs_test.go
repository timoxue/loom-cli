package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShadowVFSResolveReadPathPrefersShadow(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	shadowDir := t.TempDir()

	workspaceFile := filepath.Join(workspaceDir, "data", "output.txt")
	if err := os.MkdirAll(filepath.Dir(workspaceFile), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(workspaceFile, []byte("workspace"), 0o644); err != nil {
		t.Fatalf("WriteFile() workspace error = %v", err)
	}

	shadowFile := filepath.Join(shadowDir, "data", "output.txt")
	if err := os.MkdirAll(filepath.Dir(shadowFile), 0o755); err != nil {
		t.Fatalf("MkdirAll() shadow error = %v", err)
	}
	if err := os.WriteFile(shadowFile, []byte("shadow"), 0o644); err != nil {
		t.Fatalf("WriteFile() shadow error = %v", err)
	}

	vfs := &ShadowVFS{
		WorkspaceDir: workspaceDir,
		ShadowDir:    shadowDir,
	}

	got, err := vfs.ResolveReadPath("./data/output.txt")
	if err != nil {
		t.Fatalf("ResolveReadPath() error = %v", err)
	}
	if got != shadowFile {
		t.Fatalf("ResolveReadPath() = %q, want %q", got, shadowFile)
	}
}

func TestShadowVFSResolveWritePathRejectsEscape(t *testing.T) {
	t.Parallel()

	vfs := &ShadowVFS{
		WorkspaceDir: t.TempDir(),
		ShadowDir:    t.TempDir(),
	}

	if _, err := vfs.ResolveWritePath("../../etc/passwd"); err == nil {
		t.Fatal("ResolveWritePath() error = nil, want security error")
	}
}

func TestShadowVFSResolveWritePathMapsIntoShadow(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	shadowDir := t.TempDir()

	vfs := &ShadowVFS{
		WorkspaceDir: workspaceDir,
		ShadowDir:    shadowDir,
	}

	got, err := vfs.ResolveWritePath("./data/output.json")
	if err != nil {
		t.Fatalf("ResolveWritePath() error = %v", err)
	}

	want := filepath.Join(shadowDir, "data", "output.json")
	if got != want {
		t.Fatalf("ResolveWritePath() = %q, want %q", got, want)
	}
}

func TestShadowVFSCommitCopiesShadowChangesIntoWorkspace(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	shadowDir := t.TempDir()

	workspaceFile := filepath.Join(workspaceDir, "notes.txt")
	if err := os.WriteFile(workspaceFile, []byte("before"), 0o644); err != nil {
		t.Fatalf("WriteFile() workspace error = %v", err)
	}

	shadowFile := filepath.Join(shadowDir, "notes.txt")
	if err := os.WriteFile(shadowFile, []byte("after"), 0o644); err != nil {
		t.Fatalf("WriteFile() shadow error = %v", err)
	}

	newShadowFile := filepath.Join(shadowDir, "nested", "new.txt")
	if err := os.MkdirAll(filepath.Dir(newShadowFile), 0o755); err != nil {
		t.Fatalf("MkdirAll() shadow nested error = %v", err)
	}
	if err := os.WriteFile(newShadowFile, []byte("created"), 0o644); err != nil {
		t.Fatalf("WriteFile() shadow nested error = %v", err)
	}

	vfs := &ShadowVFS{
		WorkspaceDir: workspaceDir,
		ShadowDir:    shadowDir,
	}

	if err := vfs.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	content, err := os.ReadFile(workspaceFile)
	if err != nil {
		t.Fatalf("ReadFile() workspace error = %v", err)
	}
	if string(content) != "after" {
		t.Fatalf("workspace content = %q, want after", content)
	}

	createdFile := filepath.Join(workspaceDir, "nested", "new.txt")
	createdContent, err := os.ReadFile(createdFile)
	if err != nil {
		t.Fatalf("ReadFile() created file error = %v", err)
	}
	if string(createdContent) != "created" {
		t.Fatalf("created file content = %q, want created", createdContent)
	}

	if _, err := os.Stat(shadowDir); !os.IsNotExist(err) {
		t.Fatalf("shadow dir still exists after commit, err = %v", err)
	}
}

func TestShadowVFSCommitDoesNotTouchWorkspaceBeforeCommit(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	shadowDir := t.TempDir()

	workspaceFile := filepath.Join(workspaceDir, "artifact.txt")
	if err := os.WriteFile(workspaceFile, []byte("stable"), 0o644); err != nil {
		t.Fatalf("WriteFile() workspace error = %v", err)
	}

	vfs := &ShadowVFS{
		WorkspaceDir: workspaceDir,
		ShadowDir:    shadowDir,
	}

	writePath, err := vfs.ResolveWritePath("artifact.txt")
	if err != nil {
		t.Fatalf("ResolveWritePath() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(writePath), 0o755); err != nil {
		t.Fatalf("MkdirAll() shadow error = %v", err)
	}
	if err := os.WriteFile(writePath, []byte("shadow-only"), 0o644); err != nil {
		t.Fatalf("WriteFile() shadow error = %v", err)
	}

	content, err := os.ReadFile(workspaceFile)
	if err != nil {
		t.Fatalf("ReadFile() workspace error = %v", err)
	}
	if string(content) != "stable" {
		t.Fatalf("workspace content before commit = %q, want stable", content)
	}
}

func TestShadowVFSMarkDeletedHidesWorkspaceFileFromReads(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	shadowDir := t.TempDir()

	workspaceFile := filepath.Join(workspaceDir, "logs", "app.log")
	if err := os.MkdirAll(filepath.Dir(workspaceFile), 0o755); err != nil {
		t.Fatalf("MkdirAll() workspace error = %v", err)
	}
	if err := os.WriteFile(workspaceFile, []byte("present"), 0o644); err != nil {
		t.Fatalf("WriteFile() workspace error = %v", err)
	}

	vfs := &ShadowVFS{
		WorkspaceDir: workspaceDir,
		ShadowDir:    shadowDir,
	}

	if err := vfs.MarkDeleted("./logs/app.log"); err != nil {
		t.Fatalf("MarkDeleted() error = %v", err)
	}

	if _, err := vfs.ResolveReadPath("./logs/app.log"); !os.IsNotExist(err) {
		t.Fatalf("ResolveReadPath() error = %v, want not exist", err)
	}
}

func TestShadowVFSCommitAppliesDeletionsToWorkspace(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	shadowDir := t.TempDir()

	workspaceFile := filepath.Join(workspaceDir, "logs", "old.log")
	if err := os.MkdirAll(filepath.Dir(workspaceFile), 0o755); err != nil {
		t.Fatalf("MkdirAll() workspace error = %v", err)
	}
	if err := os.WriteFile(workspaceFile, []byte("obsolete"), 0o644); err != nil {
		t.Fatalf("WriteFile() workspace error = %v", err)
	}

	vfs := &ShadowVFS{
		WorkspaceDir: workspaceDir,
		ShadowDir:    shadowDir,
	}

	if err := vfs.MarkDeleted("./logs/old.log"); err != nil {
		t.Fatalf("MarkDeleted() error = %v", err)
	}

	if err := vfs.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	if _, err := os.Stat(workspaceFile); !os.IsNotExist(err) {
		t.Fatalf("workspace file still exists after commit, err = %v", err)
	}
}
