package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timoxue/loom-cli/internal/engine"
	"github.com/timoxue/loom-cli/internal/engine/parser"
	"github.com/timoxue/loom-cli/internal/security"
)

// TestCommitRoundTripPromotesSubstitutedContent exercises the whole
// post-spike pipeline end-to-end:
//  1. Parse a templated v1 skill
//  2. Compile (admission + input digest + flattened receipt path)
//  3. Execute (substitution expands ${msg} into shadow bytes)
//  4. CommitGate.LoadReceipt finds the receipt in O(1)
//  5. CommitGate.Promote copies bytes into the workspace
// The test fails if substitution, admission, or promotion breaks any of
// the sandbox invariants at any step.
func TestCommitRoundTripPromotesSubstitutedContent(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	workspaceRoot := t.TempDir()

	raw, err := os.ReadFile(filepath.Join("..", "test_skills", "templated_write.loom.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	skill, err := parser.ParseFile("templated_write.loom.json", raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	compiler := &engine.Compiler{
		Policy:        security.DefaultPolicy(),
		WorkspaceRoot: workspaceRoot,
	}
	sessionID := "round-trip-session"

	vfs, sanitizedInputs, err := compiler.CompileAndSetup(
		skill,
		map[string]string{"msg": "world"},
		sessionID,
	)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	executor := &engine.Executor{VFS: vfs}
	if _, err := executor.Execute(context.Background(), skill, sanitizedInputs); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Before promote: shadow populated with substituted content, workspace untouched.
	shadowGreeting := filepath.Join(vfs.ShadowDir, "out", "greeting.txt")
	shadowBytes, err := os.ReadFile(shadowGreeting)
	if err != nil {
		t.Fatalf("read shadow file: %v", err)
	}
	if string(shadowBytes) != "hello world" {
		t.Fatalf("shadow content = %q, want %q", string(shadowBytes), "hello world")
	}
	if _, err := os.Stat(filepath.Join(workspaceRoot, "out", "greeting.txt")); !os.IsNotExist(err) {
		t.Fatalf("workspace mutated before commit, stat err = %v", err)
	}

	// Promote via CommitGate.
	gate := &engine.CommitGate{}
	receipt, err := gate.LoadReceipt(sessionID)
	if err != nil {
		t.Fatalf("LoadReceipt: %v", err)
	}
	if receipt.InputDigest == "" {
		t.Fatal("receipt.InputDigest is empty")
	}
	if receipt.WorkspaceRoot == "" {
		t.Fatal("receipt.WorkspaceRoot is empty")
	}

	manifest, err := gate.Preview(receipt)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(manifest) != 1 || manifest[0].Path != "out/greeting.txt" {
		t.Fatalf("manifest = %+v, want single write for out/greeting.txt", manifest)
	}

	if err := gate.Promote(receipt); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	// After promote: workspace has the file with substituted content.
	workspaceBytes, err := os.ReadFile(filepath.Join(workspaceRoot, "out", "greeting.txt"))
	if err != nil {
		t.Fatalf("read promoted file: %v", err)
	}
	if string(workspaceBytes) != "hello world" {
		t.Fatalf("workspace content = %q, want %q", string(workspaceBytes), "hello world")
	}

	// Shadow directory should be gone after successful promote.
	if _, err := os.Stat(vfs.ShadowDir); !os.IsNotExist(err) {
		t.Fatalf("shadow dir still exists after promote, stat err = %v", err)
	}
}

// TestAdmissionHashStableUnderInputChange verifies that changing --input
// values does NOT change the logical hash — only the InputDigest does.
// This is the concrete statement of "admission is about shape, not values."
func TestAdmissionHashStableUnderInputChange(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	raw, err := os.ReadFile(filepath.Join("..", "test_skills", "templated_write.loom.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	skill, err := parser.ParseFile("templated_write.loom.json", raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	admissionHash := skill.GetLogicalHash()

	compiler := &engine.Compiler{
		Policy:        security.DefaultPolicy(),
		WorkspaceRoot: t.TempDir(),
	}

	// Two different input values, same skill.
	_, _, err = compiler.CompileAndSetup(skill, map[string]string{"msg": "alice"}, "s1")
	if err != nil {
		t.Fatalf("compile s1: %v", err)
	}
	_, _, err = compiler.CompileAndSetup(skill, map[string]string{"msg": "bob"}, "s2")
	if err != nil {
		t.Fatalf("compile s2: %v", err)
	}

	gate := &engine.CommitGate{}
	r1, err := gate.LoadReceipt("s1")
	if err != nil {
		t.Fatalf("load s1: %v", err)
	}
	r2, err := gate.LoadReceipt("s2")
	if err != nil {
		t.Fatalf("load s2: %v", err)
	}

	if r1.LogicalHash != admissionHash || r2.LogicalHash != admissionHash {
		t.Fatalf("logical hash should be stable, got r1=%q r2=%q want=%q",
			r1.LogicalHash, r2.LogicalHash, admissionHash)
	}
	if r1.InputDigest == r2.InputDigest {
		t.Fatalf("input digest should differ across input values, both = %q", r1.InputDigest)
	}
}

// TestUnknownVariableNamedInError ensures operators can diagnose typos.
// The error that bubbles up from the executor must literally contain the
// missing variable name.
func TestUnknownVariableNamedInError(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	skill := &engine.LoomSkill{
		SchemaVersion: engine.CurrentSchemaVersion,
		SkillID:       "typo_skill",
		Parameters: map[string]engine.Parameter{
			"msg": {Type: engine.ParameterTypeString, Required: true},
		},
		ExecutionDAG: []engine.Step{
			{
				StepID: "s1",
				Kind:   engine.StepKindWriteFile,
				Args:   engine.WriteFileArgs{Path: "out/hi.txt", Content: "hello ${typo}"},
			},
		},
		Capabilities: []engine.Capability{
			{Kind: engine.CapKindVFSWrite, Scope: "out/"},
		},
	}

	compiler := &engine.Compiler{
		Policy:        security.DefaultPolicy(),
		WorkspaceRoot: t.TempDir(),
	}
	vfs, sanitized, err := compiler.CompileAndSetup(skill, map[string]string{"msg": "world"}, "typo-session")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	executor := &engine.Executor{VFS: vfs}
	_, err = executor.Execute(context.Background(), skill, sanitized)
	if err == nil {
		t.Fatal("Execute() error = nil, want unknown-variable failure")
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Fatalf("error %q does not name the missing variable \"typo\"", err.Error())
	}
}
