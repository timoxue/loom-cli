package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/timoxue/loom-cli/internal/engine"
	"github.com/timoxue/loom-cli/internal/engine/parser"
	"github.com/timoxue/loom-cli/internal/migrator"
	"github.com/timoxue/loom-cli/internal/security"
)

// writeOpenClawSkill drops a minimal v0 SKILL.md into sourceDir. All
// the integration tests below hit the migrator package's public API
// (not the cobra command directly) since we want deterministic flag
// handling without shelling out.
func writeOpenClawSkill(t *testing.T, sourceDir, filename, name string, instruction string, permsLine string) string {
	t.Helper()
	body := "---\nname: " + name + "\n---\n\n" +
		"## Parameters\n" +
		"- `msg` (string): message. Required.\n\n" +
		"## Permissions\n" +
		permsLine + "\n\n" +
		"## Instructions\n" +
		"1. " + instruction + "\n"

	path := filepath.Join(sourceDir, filename)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	return path
}

// TestMigrateAcceptRunFullRoundTrip verifies the whole intended flow:
//
//  1. `loom migrate-openclaw` produces an unreviewed draft
//  2. `loom run` refuses the unreviewed draft (zero-trust default)
//  3. `loom accept-migration` signs it
//  4. `loom run` now executes it successfully
//
// Every step is invoked via the public package API so the test stays
// fast and doesn't need an API key. The LLM path is exercised in a
// separate test using a fake client.
func TestMigrateAcceptRunFullRoundTrip(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	sourceDir := t.TempDir()
	outDir := t.TempDir()
	workspaceRoot := t.TempDir()

	writeOpenClawSkill(t, sourceDir, "greet.md", "greet",
		"write ${msg} to out/greeting.txt",
		"- `fs.write`: out")

	// 1. migrate
	now := time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)
	report, err := migrator.Migrate(migrator.Options{
		SourceDir: sourceDir,
		OutDir:    outDir,
		Execute:   true,
		Now:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if report.Items[0].Status != migrator.ItemStatusMechanical {
		t.Fatalf("status = %s, want mechanical", report.Items[0].Status)
	}

	migratedPath := filepath.Join(outDir, "greet.loom.json")
	raw, err := os.ReadFile(migratedPath)
	if err != nil {
		t.Fatalf("read migrated: %v", err)
	}
	skill, err := parser.ParseFile(migratedPath, raw)
	if err != nil {
		t.Fatalf("parse migrated: %v", err)
	}

	// 2. run refuses unreviewed draft (default policy = refuse)
	compiler := &engine.Compiler{
		Policy:        security.DefaultPolicy(),
		WorkspaceRoot: workspaceRoot,
	}
	vfs, inputs, err := compiler.CompileAndSetup(skill, map[string]string{"msg": "hi"}, "round-trip-1")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	executor := &engine.Executor{VFS: vfs}
	if _, err := executor.Execute(context.Background(), skill, inputs); err == nil {
		t.Fatal("Execute() should refuse unreviewed draft, got nil error")
	} else if !strings.Contains(err.Error(), "reviewed") {
		t.Fatalf("refusal error %q should mention 'reviewed'", err.Error())
	}

	// 3. accept-migration
	accepted, err := migrator.AcceptMigration(migrator.AcceptOptions{
		SkillPath:  migratedPath,
		SourceRoot: sourceDir,
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !accepted.Reviewed {
		t.Fatal("AcceptMigration did not mark Reviewed true")
	}

	// 4. re-read and run — should succeed
	raw, err = os.ReadFile(migratedPath)
	if err != nil {
		t.Fatal(err)
	}
	skill, err = parser.ParseFile(migratedPath, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !skill.Provenance.Reviewed {
		t.Fatal("re-parsed skill Reviewed=false after accept")
	}

	vfs2, inputs2, err := compiler.CompileAndSetup(skill, map[string]string{"msg": "hi"}, "round-trip-2")
	if err != nil {
		t.Fatalf("compile after accept: %v", err)
	}
	executor2 := &engine.Executor{VFS: vfs2}
	if _, err := executor2.Execute(context.Background(), skill, inputs2); err != nil {
		t.Fatalf("Execute() after accept: %v", err)
	}

	shadowFile := filepath.Join(vfs2.ShadowDir, "out", "greeting.txt")
	content, err := os.ReadFile(shadowFile)
	if err != nil {
		t.Fatalf("read shadow greeting: %v", err)
	}
	if string(content) != "hi" {
		t.Fatalf("shadow content = %q, want %q", string(content), "hi")
	}
}

// TestAcceptMigrationRejectsStaleSource verifies that a user who
// changes the source markdown between migrate and accept cannot
// review a draft that no longer reflects the source.
func TestAcceptMigrationRejectsStaleSource(t *testing.T) {
	sourceDir := t.TempDir()
	outDir := t.TempDir()

	sourcePath := writeOpenClawSkill(t, sourceDir, "drift.md", "drift",
		"write ${msg} to out/x.txt", "- `fs.write`: out")

	if _, err := migrator.Migrate(migrator.Options{
		SourceDir: sourceDir, OutDir: outDir, Execute: true,
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Simulate the source file being edited after migration.
	if err := os.WriteFile(sourcePath, []byte("modified content"), 0o644); err != nil {
		t.Fatalf("modify source: %v", err)
	}

	migratedPath := filepath.Join(outDir, "drift.loom.json")
	_, err := migrator.AcceptMigration(migrator.AcceptOptions{
		SkillPath:  migratedPath,
		SourceRoot: sourceDir,
	})
	if err == nil {
		t.Fatal("accept should reject stale source, got nil error")
	}
	if !strings.Contains(err.Error(), "changed since migration") {
		t.Fatalf("error %q should mention stale source", err.Error())
	}
}

// TestAcceptMigrationRejectsStub confirms that Tier 3 output can never
// be marked reviewed — reviewing an empty skill is a footgun.
func TestAcceptMigrationRejectsStub(t *testing.T) {
	sourceDir := t.TempDir()
	outDir := t.TempDir()

	writeOpenClawSkill(t, sourceDir, "shellwrap.md", "shellwrap",
		"run shell command to clean up", "- `fs.write`: out")

	if _, err := migrator.Migrate(migrator.Options{
		SourceDir: sourceDir, OutDir: outDir, Execute: true, NoLLM: true,
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	migratedPath := filepath.Join(outDir, "shellwrap.loom.json")
	_, err := migrator.AcceptMigration(migrator.AcceptOptions{
		SkillPath:  migratedPath,
		SourceRoot: sourceDir,
	})
	if err == nil {
		t.Fatal("accept should reject stub, got nil error")
	}
	if !strings.Contains(err.Error(), "stub") {
		t.Fatalf("error %q should mention stub", err.Error())
	}
}

// TestReviewedSkillHasValidSignature round-trips a migrated skill
// through JSON and verifies the reviewer signature matches what the
// executor recomputes.
func TestReviewedSkillHasValidSignature(t *testing.T) {
	sourceDir := t.TempDir()
	outDir := t.TempDir()

	writeOpenClawSkill(t, sourceDir, "sig.md", "sig",
		"write ${msg} to out/a.txt", "- `fs.write`: out")

	if _, err := migrator.Migrate(migrator.Options{
		SourceDir: sourceDir, OutDir: outDir, Execute: true,
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	migratedPath := filepath.Join(outDir, "sig.loom.json")
	if _, err := migrator.AcceptMigration(migrator.AcceptOptions{
		SkillPath: migratedPath, SourceRoot: sourceDir,
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}

	raw, err := os.ReadFile(migratedPath)
	if err != nil {
		t.Fatal(err)
	}
	var skill engine.LoomSkill
	if err := json.Unmarshal(raw, &skill); err != nil {
		t.Fatal(err)
	}
	expected, err := engine.CanonicalBodyHash(&skill)
	if err != nil {
		t.Fatalf("CanonicalBodyHash: %v", err)
	}
	if skill.Provenance.ReviewerSignature != expected {
		t.Fatalf("signature mismatch: stored=%q recomputed=%q", skill.Provenance.ReviewerSignature, expected)
	}
}

// TestResolveDraftPolicy exercises the run-subcommand's flag plumbing.
func TestResolveDraftPolicy(t *testing.T) {
	t.Setenv("LOOM_DRAFT_POLICY", "") // clean slate

	cases := []struct {
		flag     string
		accept   bool
		env      string
		want     engine.DraftPolicy
		wantErr  bool
	}{
		{"", false, "", engine.DraftPolicyRefuse, false},
		{"refuse", false, "", engine.DraftPolicyRefuse, false},
		{"warn", false, "", engine.DraftPolicyWarn, false},
		{"allow", false, "", engine.DraftPolicyAllow, false},
		{"", true, "", engine.DraftPolicyAllow, false},       // --accept-draft
		{"", false, "warn", engine.DraftPolicyWarn, false},   // env var
		{"refuse", false, "allow", engine.DraftPolicyRefuse, false}, // flag beats env
		{"garbage", false, "", "", true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.flag+"/"+tc.env, func(t *testing.T) {
			t.Setenv("LOOM_DRAFT_POLICY", tc.env)
			got, err := resolveDraftPolicy(tc.flag, tc.accept)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
