package engine

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCommitGatePromoteCopiesShadowIntoWorkspace exercises the full
// promotion: write a file into the shadow, Promote, assert the workspace
// now has it and the shadow is gone.
func TestCommitGatePromoteCopiesShadowIntoWorkspace(t *testing.T) {
	workspaceRoot := t.TempDir()
	shadowRoot := t.TempDir()

	shadowFile := filepath.Join(shadowRoot, "out", "hello.txt")
	if err := os.MkdirAll(filepath.Dir(shadowFile), 0o755); err != nil {
		t.Fatalf("mkdir shadow subdir: %v", err)
	}
	if err := os.WriteFile(shadowFile, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write shadow file: %v", err)
	}

	receipt := &Receipt{
		WorkspaceRoot: workspaceRoot,
		ShadowPath:    shadowRoot,
	}

	gate := &CommitGate{}

	// Preview before promote: workspace must still be empty.
	manifest, err := gate.Preview(receipt)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if len(manifest) != 1 || manifest[0].Path != "out/hello.txt" {
		t.Fatalf("manifest = %+v, want single write for out/hello.txt", manifest)
	}
	if _, err := os.Stat(filepath.Join(workspaceRoot, "out", "hello.txt")); !os.IsNotExist(err) {
		t.Fatalf("workspace mutated during preview, stat err = %v", err)
	}

	if err := gate.Promote(receipt); err != nil {
		t.Fatalf("Promote() error = %v", err)
	}

	workspaceFile := filepath.Join(workspaceRoot, "out", "hello.txt")
	got, err := os.ReadFile(workspaceFile)
	if err != nil {
		t.Fatalf("read promoted file: %v", err)
	}
	if string(got) != "hi" {
		t.Fatalf("workspace content = %q, want %q", string(got), "hi")
	}

	if _, err := os.Stat(shadowRoot); !os.IsNotExist(err) {
		t.Fatalf("shadow dir still exists after promote, stat err = %v", err)
	}
}

func TestCommitGateLoadReceiptRejectsMissingSession(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	gate := &CommitGate{}
	_, err := gate.LoadReceipt("nonexistent-session")
	if err == nil {
		t.Fatal("LoadReceipt() error = nil, want session-not-found")
	}
	var contractErr *ContractError
	if !errors.As(err, &contractErr) {
		t.Fatalf("error type = %T, want *ContractError", err)
	}
	if contractErr.Field != "session_id" {
		t.Fatalf("ContractError.Field = %q, want session_id", contractErr.Field)
	}
}

func TestCommitGateLoadReceiptRejectsPathEscape(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	gate := &CommitGate{}
	// A session id that tries to escape the cache root must be rejected
	// at the path sanitation boundary, not at the OS boundary.
	_, err := gate.LoadReceipt("../../../etc/passwd")
	if err == nil {
		t.Fatal("LoadReceipt() error = nil, want session id rejection")
	}
}

func TestCommitGateLoadReceiptDecodesValidReceipt(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	sessionID := "session-abc"
	cacheDir := filepath.Join(homeDir, ".loom", "cache", sessionID)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}

	receipt := Receipt{
		SessionID:     sessionID,
		SkillID:       "demo",
		SchemaVersion: CurrentSchemaVersion,
		LogicalHash:   "abc123",
		InputDigest:   "def456",
		Timestamp:     time.Now().UTC(),
		ShadowPath:    t.TempDir(),
		WorkspaceRoot: t.TempDir(),
	}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "receipt.json"), raw, 0o644); err != nil {
		t.Fatalf("write receipt: %v", err)
	}

	gate := &CommitGate{}
	got, err := gate.LoadReceipt(sessionID)
	if err != nil {
		t.Fatalf("LoadReceipt() error = %v", err)
	}
	if got.SessionID != sessionID {
		t.Fatalf("SessionID = %q, want %q", got.SessionID, sessionID)
	}
	if got.LogicalHash != "abc123" {
		t.Fatalf("LogicalHash = %q, want abc123", got.LogicalHash)
	}
	if got.InputDigest != "def456" {
		t.Fatalf("InputDigest = %q, want def456", got.InputDigest)
	}
}

func TestCommitGateLoadReceiptRejectsMalformedJSON(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	sessionID := "broken-session"
	cacheDir := filepath.Join(homeDir, ".loom", "cache", sessionID)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "receipt.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write bad receipt: %v", err)
	}

	gate := &CommitGate{}
	_, err := gate.LoadReceipt(sessionID)
	if err == nil {
		t.Fatal("LoadReceipt() error = nil, want decode error")
	}
	var contractErr *ContractError
	if !errors.As(err, &contractErr) {
		t.Fatalf("error type = %T, want *ContractError", err)
	}
}

func TestCommitGateLoadReceiptRejectsIncompleteReceipt(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	sessionID := "incomplete-session"
	cacheDir := filepath.Join(homeDir, ".loom", "cache", sessionID)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir cache dir: %v", err)
	}
	// A receipt missing WorkspaceRoot / ShadowPath is useless for promote.
	incomplete := Receipt{SessionID: sessionID, SkillID: "x"}
	raw, _ := json.Marshal(incomplete)
	if err := os.WriteFile(filepath.Join(cacheDir, "receipt.json"), raw, 0o644); err != nil {
		t.Fatalf("write receipt: %v", err)
	}

	gate := &CommitGate{}
	_, err := gate.LoadReceipt(sessionID)
	if err == nil {
		t.Fatal("LoadReceipt() error = nil, want incomplete-receipt rejection")
	}
}
