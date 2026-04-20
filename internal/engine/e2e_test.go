package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timoxue/loom-cli/internal/security"
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

// draftSkill builds a v1 write-file skill with the given provenance.
// Used as a shared fixture for draft-policy executor tests.
func draftSkill(prov *Provenance) *LoomSkill {
	return &LoomSkill{
		SchemaVersion: CurrentSchemaVersion,
		SkillID:       "draft_probe",
		ExecutionDAG: []Step{
			{
				StepID: "s1",
				Kind:   StepKindWriteFile,
				Args:   WriteFileArgs{Path: "out/hi.txt", Content: "hi"},
			},
		},
		Capabilities: []Capability{{Kind: CapKindVFSWrite, Scope: "out/"}},
		Provenance:   prov,
	}
}

func compileDraftSkill(t *testing.T, skill *LoomSkill, sessionID string) (*ShadowVFS, map[string]any) {
	t.Helper()
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)
	compiler := &Compiler{
		Policy:        security.DefaultPolicy(),
		WorkspaceRoot: t.TempDir(),
	}
	vfs, inputs, err := compiler.CompileAndSetup(skill, nil, sessionID)
	if err != nil {
		t.Fatalf("CompileAndSetup() error = %v", err)
	}
	return vfs, inputs
}

func TestExecutorRefusesUnreviewedDraftByDefault(t *testing.T) {
	skill := draftSkill(&Provenance{
		Origin:     "openclaw-migrate",
		Mode:       ProvenanceModeLLMAssisted,
		SourcePath: "skills/demo.md",
		SourceHash: "abc",
		Reviewed:   false,
	})
	vfs, sanitized := compileDraftSkill(t, skill, "draft-refuse")

	executor := &Executor{VFS: vfs} // zero DraftPolicy == refuse
	_, err := executor.Execute(context.Background(), skill, sanitized)
	if err == nil {
		t.Fatal("Execute() error = nil, want refusal of unreviewed draft")
	}
	if !strings.Contains(err.Error(), "reviewed") {
		t.Fatalf("error %q should mention 'reviewed'", err.Error())
	}
}

func TestExecutorAllowsUnreviewedDraftUnderAllowPolicy(t *testing.T) {
	skill := draftSkill(&Provenance{
		Origin:     "openclaw-migrate",
		Mode:       ProvenanceModeLLMAssisted,
		SourcePath: "skills/demo.md",
		SourceHash: "abc",
		Reviewed:   false,
	})
	vfs, sanitized := compileDraftSkill(t, skill, "draft-allow")

	executor := &Executor{VFS: vfs, DraftPolicy: DraftPolicyAllow}
	if _, err := executor.Execute(context.Background(), skill, sanitized); err != nil {
		t.Fatalf("Execute() error = %v, want success under DraftPolicyAllow", err)
	}
}

func TestExecutorWarnPolicyEmitsWarningButExecutes(t *testing.T) {
	skill := draftSkill(&Provenance{
		Origin:     "openclaw-migrate",
		Mode:       ProvenanceModeMechanical,
		SourcePath: "skills/demo.md",
		SourceHash: "abc",
		Reviewed:   false,
	})
	vfs, sanitized := compileDraftSkill(t, skill, "draft-warn")

	var warningBuffer bytes.Buffer
	executor := &Executor{
		VFS:          vfs,
		DraftPolicy:  DraftPolicyWarn,
		DraftWarning: &warningBuffer,
	}
	if _, err := executor.Execute(context.Background(), skill, sanitized); err != nil {
		t.Fatalf("Execute() error = %v, want success under DraftPolicyWarn", err)
	}
	if !strings.Contains(warningBuffer.String(), "draft_probe") {
		t.Fatalf("warning output %q should name the skill", warningBuffer.String())
	}
}

func TestExecutorRefusesStubEvenUnderAllow(t *testing.T) {
	// Stubs are never executable, regardless of policy. This is the
	// invariant that makes Tier 3 migration output safe to ship.
	skill := draftSkill(&Provenance{
		Origin:     "openclaw-migrate",
		Mode:       ProvenanceModeStub,
		SourcePath: "skills/shell_only.md",
		SourceHash: "abc",
		StubReason: "capability os_command not supported",
	})
	vfs, sanitized := compileDraftSkill(t, skill, "draft-stub")

	executor := &Executor{VFS: vfs, DraftPolicy: DraftPolicyAllow}
	_, err := executor.Execute(context.Background(), skill, sanitized)
	if err == nil {
		t.Fatal("Execute() error = nil, want stub to be refused even under DraftPolicyAllow")
	}
	if !strings.Contains(err.Error(), "stub") {
		t.Fatalf("error %q should mention 'stub'", err.Error())
	}
}

func TestExecutorAllowsReviewedSkillWithValidSignature(t *testing.T) {
	skill := draftSkill(&Provenance{
		Origin:     "openclaw-migrate",
		Mode:       ProvenanceModeMechanical,
		SourcePath: "skills/demo.md",
		SourceHash: "abc",
	})
	// Compute the signature the way `loom accept-migration` would.
	skill.Provenance.Reviewed = true
	hash, err := CanonicalBodyHash(skill)
	if err != nil {
		t.Fatalf("CanonicalBodyHash error = %v", err)
	}
	skill.Provenance.ReviewerSignature = hash

	vfs, sanitized := compileDraftSkill(t, skill, "draft-valid-signed")

	executor := &Executor{VFS: vfs}
	if _, err := executor.Execute(context.Background(), skill, sanitized); err != nil {
		t.Fatalf("Execute() error = %v, want success for properly signed reviewed skill", err)
	}
}

func TestExecutorRefusesReviewedSkillWithMismatchedSignature(t *testing.T) {
	// Simulates a user who hand-edited "reviewed: true" into the file
	// without running accept-migration. The signature won't match,
	// so the executor rejects even under DraftPolicyAllow.
	skill := draftSkill(&Provenance{
		Origin:            "openclaw-migrate",
		Mode:              ProvenanceModeMechanical,
		SourcePath:        "skills/demo.md",
		SourceHash:        "abc",
		Reviewed:          true,
		ReviewerSignature: "deadbeef_definitely_not_the_right_hash",
	})
	vfs, sanitized := compileDraftSkill(t, skill, "draft-bad-sig")

	executor := &Executor{VFS: vfs, DraftPolicy: DraftPolicyAllow}
	_, err := executor.Execute(context.Background(), skill, sanitized)
	if err == nil {
		t.Fatal("Execute() error = nil, want refusal on signature mismatch")
	}
	if !strings.Contains(err.Error(), "reviewer_signature") {
		t.Fatalf("error %q should mention reviewer_signature", err.Error())
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
