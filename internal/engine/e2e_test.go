package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourname/loom-cli/internal/security"
)

// TestE2ESpikeWriteFileHoldsSandbox validates the four acceptance checks
// that justify this whole spike:
//
//  1. real workspace is untouched after run
//  2. shadow has the target file with expected bytes
//  3. manifest lists the write
//  4. a path-escape variant is rejected before the executor even runs
//
// If any one fails, the sandbox story is aspirational — the spike would
// have exposed a real design gap.
func TestE2ESpikeWriteFileHoldsSandbox(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	workspaceRoot := t.TempDir()

	skill := &LoomSkill{
		SchemaVersion: CurrentSchemaVersion,
		SkillID:       "spike_writefile",
		Parameters:    map[string]Parameter{},
		ExecutionDAG: []Step{
			{
				StepID: "s1",
				Kind:   StepKindWriteFile,
				Args:   WriteFileArgs{Path: "out/hello.txt", Content: "hi"},
			},
		},
		Capabilities: []Capability{
			{Kind: CapKindVFSWrite, Scope: "out/"},
		},
	}

	compiler := &Compiler{
		Policy:        security.DefaultPolicy(),
		WorkspaceRoot: workspaceRoot,
	}
	vfs, sanitizedInputs, err := compiler.CompileAndSetup(skill, nil, "spike-session")
	if err != nil {
		t.Fatalf("CompileAndSetup() error = %v", err)
	}

	executor := &Executor{VFS: vfs}
	manifest, err := executor.Execute(context.Background(), skill, sanitizedInputs)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Check 1: real workspace untouched.
	if _, statErr := os.Stat(filepath.Join(workspaceRoot, "out", "hello.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("workspace mutated: stat = %v, want ENOENT", statErr)
	}

	// Check 2: shadow populated with expected content.
	shadowFile := filepath.Join(vfs.ShadowDir, "out", "hello.txt")
	shadowBytes, err := os.ReadFile(shadowFile)
	if err != nil {
		t.Fatalf("read shadow file error = %v", err)
	}
	if string(shadowBytes) != "hi" {
		t.Fatalf("shadow content = %q, want hi", string(shadowBytes))
	}

	// Check 3: manifest correctly lists the write.
	var buffer bytes.Buffer
	PrintManifest(&buffer, manifest)
	manifestOutput := buffer.String()
	if !strings.Contains(manifestOutput, "out/hello.txt") {
		t.Fatalf("manifest missing out/hello.txt, got:\n%s", manifestOutput)
	}
	if !strings.Contains(manifestOutput, string(ChangeOpWrite)) {
		t.Fatalf("manifest missing write op, got:\n%s", manifestOutput)
	}
}

func TestE2ESpikePathEscapeRejectedBeforeExecution(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	workspaceRoot := t.TempDir()

	// Escape attempt via ".." inside WriteFileArgs.Path. Note: the validator
	// catches this via capability-ceiling (../etc/passwd is not covered by
	// scope "out/") BEFORE the executor ever invokes the sanitizer. Either
	// layer rejecting is a pass — what must NOT happen is any file
	// appearing outside the shadow root.
	skill := &LoomSkill{
		SchemaVersion: CurrentSchemaVersion,
		SkillID:       "escape_attempt",
		ExecutionDAG: []Step{
			{
				StepID: "s1",
				Kind:   StepKindWriteFile,
				Args:   WriteFileArgs{Path: "../etc/passwd", Content: "x"},
			},
		},
		Capabilities: []Capability{
			{Kind: CapKindVFSWrite, Scope: "out/"},
		},
	}

	compiler := &Compiler{
		Policy:        security.DefaultPolicy(),
		WorkspaceRoot: workspaceRoot,
	}
	_, _, err := compiler.CompileAndSetup(skill, nil, "escape-session")
	if err == nil {
		t.Fatal("CompileAndSetup() error = nil, want rejection of path escape")
	}

	shadowRoot := filepath.Join(homeDir, ".loom", "shadow", "escape-session")
	if _, statErr := os.Stat(shadowRoot); !os.IsNotExist(statErr) {
		t.Fatalf("shadow dir exists after rejection, stat = %v", statErr)
	}

	parentEscape := filepath.Join(filepath.Dir(homeDir), "etc", "passwd")
	if _, statErr := os.Stat(parentEscape); !os.IsNotExist(statErr) {
		t.Fatalf("escape path %q exists, stat = %v", parentEscape, statErr)
	}
}

func TestE2ESpikeSanitizerRejectsPathEscape(t *testing.T) {
	t.Parallel()

	shadowDir := t.TempDir()
	badPaths := []string{"../etc/passwd", "/etc/passwd", "", ".", ".."}
	for _, badPath := range badPaths {
		if _, err := SanitizeShadowRelPath(shadowDir, badPath); err == nil {
			t.Errorf("SanitizeShadowRelPath(%q) error = nil, want rejection", badPath)
		}
	}

	got, err := SanitizeShadowRelPath(shadowDir, "out/hello.txt")
	if err != nil {
		t.Fatalf("SanitizeShadowRelPath(valid) error = %v", err)
	}
	if got != "out/hello.txt" {
		t.Fatalf("sanitized = %q, want out/hello.txt", got)
	}
}
