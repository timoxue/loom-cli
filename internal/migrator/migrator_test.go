package migrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/timoxue/loom-cli/internal/engine"
)

// writeV0Skill is a test helper that creates an OpenClaw-shaped SKILL
// file in sourceDir with the given frontmatter + body.
func writeV0Skill(t *testing.T, sourceDir, filename, frontmatterName, description string, instructions []string, permissions ...string) string {
	t.Helper()

	if len(permissions) == 0 {
		permissions = []string{"- `fs.read`: /tmp/workspace"}
	}
	permsBlock := strings.Join(permissions, "\n")

	instructionBlock := ""
	for i, ins := range instructions {
		instructionBlock += fmt.Sprintf("%d. %s\n", i+1, ins)
	}

	descLine := ""
	if description != "" {
		descLine = "description: " + description + "\n"
	}

	body := fmt.Sprintf(`---
name: %s
%s---

## Parameters
- `+"`target`"+` (string): target path. Required.

## Permissions
%s

## Instructions
%s`, frontmatterName, descLine, permsBlock, instructionBlock)

	path := filepath.Join(sourceDir, filename)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write v0 skill: %v", err)
	}
	return path
}

// fixedLLM is a deterministic LLMClient for tests. Translate returns the
// pre-configured skill regardless of input; if Error is set, Translate
// returns that instead.
type fixedLLM struct {
	name  string
	skill *engine.LoomSkill
	err   error
}

func (f *fixedLLM) Name() string { return f.name }
func (f *fixedLLM) Translate(_ TranslateContext) (*engine.LoomSkill, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.skill, nil
}

func TestMigrateDryRunProducesNoFiles(t *testing.T) {
	sourceDir := t.TempDir()
	outDir := t.TempDir()

	writeV0Skill(t, sourceDir, "simple.md", "simple", "writes a greeting", []string{"write hi to out/hello.txt"},
		"- `fs.write`: /tmp/workspace")

	report, err := Migrate(Options{
		SourceDir: sourceDir,
		OutDir:    outDir,
		Execute:   false, // dry-run
	})
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if len(report.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(report.Items))
	}
	if report.Items[0].Status != ItemStatusMechanical {
		t.Fatalf("status = %s, want mechanical", report.Items[0].Status)
	}

	// Dry run MUST NOT write anything to OutDir.
	entries, _ := os.ReadDir(outDir)
	if len(entries) != 0 {
		t.Fatalf("OutDir has entries after dry run: %v", entries)
	}
}

func TestMigrateWritesMechanicalDraftUnreviewed(t *testing.T) {
	sourceDir := t.TempDir()
	outDir := t.TempDir()

	writeV0Skill(t, sourceDir, "simple.md", "simple", "writes a greeting", []string{"write hi to out/hello.txt"},
		"- `fs.write`: /tmp/workspace")

	report, err := Migrate(Options{
		SourceDir: sourceDir,
		OutDir:    outDir,
		Execute:   true,
	})
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if report.Items[0].Status != ItemStatusMechanical {
		t.Fatalf("status = %s, want mechanical", report.Items[0].Status)
	}

	migratedPath := filepath.Join(outDir, "simple.loom.json")
	raw, err := os.ReadFile(migratedPath)
	if err != nil {
		t.Fatalf("read migrated: %v", err)
	}

	var skill engine.LoomSkill
	if err := json.Unmarshal(raw, &skill); err != nil {
		t.Fatalf("unmarshal migrated: %v", err)
	}

	if skill.Provenance == nil {
		t.Fatal("migrated skill has no provenance")
	}
	if skill.Provenance.Mode != engine.ProvenanceModeMechanical {
		t.Errorf("mode = %s, want mechanical", skill.Provenance.Mode)
	}
	if skill.Provenance.Reviewed {
		t.Error("migrated skill should start unreviewed")
	}
	if skill.Provenance.SourceHash == "" {
		t.Error("SourceHash empty")
	}
	if skill.Provenance.SourcePath == "" {
		t.Error("SourcePath empty")
	}
}

func TestMigrateProducesStubWhenNoLLMAndNoMatch(t *testing.T) {
	sourceDir := t.TempDir()
	outDir := t.TempDir()

	// This action text won't match any conservative pattern.
	writeV0Skill(t, sourceDir, "complex.md", "complex", "", []string{
		"if the file exists, write foo to out/x.txt",
	}, "- `fs.write`: /tmp/workspace")

	report, err := Migrate(Options{
		SourceDir: sourceDir,
		OutDir:    outDir,
		Execute:   true,
		NoLLM:     true,
	})
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if report.Items[0].Status != ItemStatusStub {
		t.Fatalf("status = %s, want stub", report.Items[0].Status)
	}
	if !strings.Contains(report.Items[0].StubReason, "LLM disabled") {
		t.Errorf("stub reason = %q, expected to mention LLM disabled", report.Items[0].StubReason)
	}
}

func TestMigrateGracefulDegradationWhenLLMClientMissing(t *testing.T) {
	sourceDir := t.TempDir()
	outDir := t.TempDir()

	writeV0Skill(t, sourceDir, "complex.md", "complex", "", []string{
		"fetch http://example.com/api and store the result",
	}, "- `fs.read`: /tmp/workspace")

	// No LLMClient, NoLLM not set → this is the "no API key" case.
	// Must not error; must stub.
	report, err := Migrate(Options{
		SourceDir: sourceDir,
		OutDir:    outDir,
		Execute:   true,
	})
	if err != nil {
		t.Fatalf("Migrate() error = %v, want graceful stub emission", err)
	}
	if report.Items[0].Status != ItemStatusStub {
		t.Fatalf("status = %s, want stub (graceful degradation)", report.Items[0].Status)
	}
	if !strings.Contains(report.Items[0].StubReason, "LLM not available") {
		t.Errorf("stub reason = %q, expected LLM-not-available message", report.Items[0].StubReason)
	}
}

func TestMigrateUsesLLMWhenClientConfigured(t *testing.T) {
	sourceDir := t.TempDir()
	outDir := t.TempDir()

	writeV0Skill(t, sourceDir, "complex.md", "complex", "", []string{
		"do something complicated that isn't a mechanical pattern",
	}, "- `fs.write`: /tmp/workspace")

	translated := &engine.LoomSkill{
		SchemaVersion: engine.CurrentSchemaVersion,
		SkillID:       "complex",
		ExecutionDAG: []engine.Step{{
			StepID: "s1",
			Kind:   engine.StepKindWriteFile,
			Args:   engine.WriteFileArgs{Path: "out/result.txt", Content: "done"},
			Inputs: map[string]string{},
		}},
		Capabilities: []engine.Capability{{Kind: engine.CapKindVFSWrite, Scope: "out/"}},
	}

	report, err := Migrate(Options{
		SourceDir: sourceDir,
		OutDir:    outDir,
		Execute:   true,
		LLMClient: &fixedLLM{name: "test-model", skill: translated},
	})
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if report.Items[0].Status != ItemStatusLLMAssisted {
		t.Fatalf("status = %s, want llm_assisted", report.Items[0].Status)
	}

	// Verify the written skill has correct provenance model name.
	raw, err := os.ReadFile(filepath.Join(outDir, "complex.loom.json"))
	if err != nil {
		t.Fatal(err)
	}
	var skill engine.LoomSkill
	if err := json.Unmarshal(raw, &skill); err != nil {
		t.Fatal(err)
	}
	if skill.Provenance.Model != "test-model" {
		t.Errorf("model = %q, want test-model", skill.Provenance.Model)
	}
}

func TestMigrateStubsWhenLLMReturnsError(t *testing.T) {
	sourceDir := t.TempDir()
	outDir := t.TempDir()

	writeV0Skill(t, sourceDir, "complex.md", "complex", "", []string{
		"do something complicated",
	}, "- `fs.write`: /tmp/workspace")

	report, err := Migrate(Options{
		SourceDir: sourceDir,
		OutDir:    outDir,
		Execute:   true,
		LLMClient: &fixedLLM{name: "test-model", err: fmt.Errorf("rate limited")},
	})
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if report.Items[0].Status != ItemStatusStub {
		t.Fatalf("status = %s, want stub when LLM errs", report.Items[0].Status)
	}
}

func TestMigrateConflictModeSkipByDefault(t *testing.T) {
	sourceDir := t.TempDir()
	outDir := t.TempDir()

	writeV0Skill(t, sourceDir, "simple.md", "simple", "", []string{"write hi to out/hello.txt"},
		"- `fs.write`: /tmp/workspace")

	opts := Options{SourceDir: sourceDir, OutDir: outDir, Execute: true}

	if _, err := Migrate(opts); err != nil {
		t.Fatalf("first run: %v", err)
	}
	report, err := Migrate(opts)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if report.Items[0].Status != ItemStatusSkipped {
		t.Fatalf("second run status = %s, want skipped", report.Items[0].Status)
	}
}

func TestMigrateRefusesToOverwriteReviewed(t *testing.T) {
	sourceDir := t.TempDir()
	outDir := t.TempDir()

	sourcePath := writeV0Skill(t, sourceDir, "simple.md", "simple", "", []string{"write hi to out/hello.txt"},
		"- `fs.write`: /tmp/workspace")
	_ = sourcePath

	// First run: migrate.
	if _, err := Migrate(Options{
		SourceDir: sourceDir, OutDir: outDir, Execute: true,
	}); err != nil {
		t.Fatalf("first migrate: %v", err)
	}

	// Simulate human review: accept-migration.
	migratedPath := filepath.Join(outDir, "simple.loom.json")
	if _, err := AcceptMigration(AcceptOptions{
		SkillPath:  migratedPath,
		SourceRoot: sourceDir,
		Now:        func() time.Time { return time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC) },
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}

	// Second migrate with overwrite — must refuse reviewed target.
	report, err := Migrate(Options{
		SourceDir:              sourceDir,
		OutDir:                 outDir,
		Execute:                true,
		ConflictMode:           ConflictModeOverwrite,
		ForceReMigrateReviewed: false,
	})
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if report.Items[0].Status != ItemStatusRefused {
		t.Fatalf("status = %s, want refused_reviewed_target", report.Items[0].Status)
	}
	if !strings.Contains(report.Items[0].ErrorMessage, "reviewed") {
		t.Errorf("error = %q, expected mention of reviewed", report.Items[0].ErrorMessage)
	}
}

func TestMigrateCapabilityGapCategorization(t *testing.T) {
	sourceDir := t.TempDir()
	outDir := t.TempDir()

	writeV0Skill(t, sourceDir, "shell.md", "shell_skill", "", []string{
		"run shell command to clean up",
	}, "- `fs.write`: /tmp/workspace")

	writeV0Skill(t, sourceDir, "http.md", "http_skill", "", []string{
		"fetch http://example.com/data",
	}, "- `fs.write`: /tmp/workspace")

	report, err := Migrate(Options{
		SourceDir: sourceDir,
		OutDir:    outDir,
		Execute:   true,
		NoLLM:     true,
	})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if len(report.CapabilityGaps) == 0 {
		t.Fatal("expected capability gaps to be populated")
	}
	if _, ok := report.CapabilityGaps["os_command"]; !ok {
		t.Errorf("expected os_command gap; got %v", report.CapabilityGaps)
	}
	if _, ok := report.CapabilityGaps["http_call"]; !ok {
		t.Errorf("expected http_call gap; got %v", report.CapabilityGaps)
	}
}
