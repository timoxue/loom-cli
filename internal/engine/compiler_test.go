package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yourname/loom-cli/internal/security"
)

func TestCompilerCompileAndSetupCreatesShadowVFSAndReceipt(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	workspaceRoot := t.TempDir()
	compiler := &Compiler{
		Policy:        security.DefaultPolicy(),
		WorkspaceRoot: workspaceRoot,
	}

	skill := &LoomSkill{
		SchemaVersion: CurrentSchemaVersion,
		SkillID:       "demo_writefile",
		Parameters: map[string]Parameter{
			"target": {
				Type:     ParameterTypeString,
				Required: true,
			},
			"retries": {
				Type:         ParameterTypeInt,
				DefaultValue: "2",
			},
		},
		ExecutionDAG: []Step{
			{
				StepID:  "step_1",
				Kind:    StepKindWriteFile,
				Args:    WriteFileArgs{Path: "out/report.txt", Content: "hello"},
				Inputs:  map[string]string{"target": "${target}"},
				Outputs: []string{"output_1"},
			},
		},
		Capabilities: []Capability{
			{Kind: CapKindVFSWrite, Scope: "out/"},
		},
	}

	vfs, sanitizedInputs, err := compiler.CompileAndSetup(skill, map[string]string{
		"target": "build/report.txt",
	}, "session-001")
	if err != nil {
		t.Fatalf("CompileAndSetup() error = %v", err)
	}

	if vfs == nil {
		t.Fatal("CompileAndSetup() vfs = nil")
	}
	if vfs.WorkspaceDir != workspaceRoot {
		t.Fatalf("WorkspaceDir = %q, want %q", vfs.WorkspaceDir, workspaceRoot)
	}

	wantShadowDir := filepath.Join(homeDir, ".loom", "shadow", "session-001")
	if vfs.ShadowDir != wantShadowDir {
		t.Fatalf("ShadowDir = %q, want %q", vfs.ShadowDir, wantShadowDir)
	}
	if _, err := os.Stat(vfs.ShadowDir); err != nil {
		t.Fatalf("shadow dir stat error = %v", err)
	}

	if value, ok := sanitizedInputs["target"].(string); !ok || value != "build/report.txt" {
		t.Fatalf("sanitized target = %#v, want build/report.txt", sanitizedInputs["target"])
	}
	if value, ok := sanitizedInputs["retries"].(int); !ok || value != 2 {
		t.Fatalf("sanitized retries = %#v, want 2", sanitizedInputs["retries"])
	}

	receiptPath := filepath.Join(homeDir, ".loom", "cache", "demo_writefile", "session-001_receipt.json")
	receiptBytes, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatalf("ReadFile(receipt) error = %v", err)
	}

	var receipt Receipt
	if err := json.Unmarshal(receiptBytes, &receipt); err != nil {
		t.Fatalf("json.Unmarshal(receipt) error = %v", err)
	}
	if receipt.SessionID != "session-001" {
		t.Fatalf("receipt.SessionID = %q, want session-001", receipt.SessionID)
	}
	if receipt.SkillID != "demo_writefile" {
		t.Fatalf("receipt.SkillID = %q, want demo_writefile", receipt.SkillID)
	}
	if receipt.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("receipt.SchemaVersion = %q, want %q", receipt.SchemaVersion, CurrentSchemaVersion)
	}
	if receipt.LogicalHash != skill.GetLogicalHash() {
		t.Fatalf("receipt.LogicalHash = %q, want %q", receipt.LogicalHash, skill.GetLogicalHash())
	}
	if receipt.ShadowPath != wantShadowDir {
		t.Fatalf("receipt.ShadowPath = %q, want %q", receipt.ShadowPath, wantShadowDir)
	}
	if len(receipt.GrantedCapabilities) != 1 || receipt.GrantedCapabilities[0].Kind != CapKindVFSWrite {
		t.Fatalf("receipt.GrantedCapabilities = %#v, want single vfs.write cap", receipt.GrantedCapabilities)
	}
}

func TestCompilerCompileAndSetupCleansShadowDirOnFailure(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	workspaceRoot := t.TempDir()
	compiler := &Compiler{
		Policy:        security.DefaultPolicy(),
		WorkspaceRoot: workspaceRoot,
	}

	skill := &LoomSkill{
		SchemaVersion: CurrentSchemaVersion,
		SkillID:       "dangerous_skill",
		ExecutionDAG: []Step{
			{
				StepID: "step_1",
				Kind:   StepKindWriteFile,
				Args:   WriteFileArgs{Path: "out/payload.sh", Content: "rm -rf /tmp/demo"},
			},
		},
		Capabilities: []Capability{
			{Kind: CapKindVFSWrite, Scope: "out/"},
		},
	}

	_, _, err := compiler.CompileAndSetup(skill, nil, "session-002")
	if err == nil {
		t.Fatal("CompileAndSetup() error = nil, want validation failure")
	}

	shadowDir := filepath.Join(homeDir, ".loom", "shadow", "session-002")
	if _, statErr := os.Stat(shadowDir); !os.IsNotExist(statErr) {
		t.Fatalf("shadow dir exists after failure, err = %v", statErr)
	}
}

func TestCompilerCompileAndSetupRejectsUnsafeSessionID(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	compiler := &Compiler{
		Policy:        security.DefaultPolicy(),
		WorkspaceRoot: t.TempDir(),
	}

	skill := &LoomSkill{
		SchemaVersion: CurrentSchemaVersion,
		SkillID:       "safe_skill",
	}

	if _, _, err := compiler.CompileAndSetup(skill, nil, "../escape"); err == nil {
		t.Fatal("CompileAndSetup() error = nil, want session id rejection")
	}
}
