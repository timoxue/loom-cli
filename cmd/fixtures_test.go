package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourname/loom-cli/internal/engine"
	"github.com/yourname/loom-cli/internal/engine/parser"
	"github.com/yourname/loom-cli/internal/security"
)

// stage captures which trust boundary a fixture is expected to be rejected at
// (or to pass through entirely). Every fixture in test_skills/ must pin
// exactly one expected stage — this makes the matrix self-documenting and
// prevents silent regressions where a case starts getting caught by a
// different (and possibly weaker) layer than intended.
type stage int

const (
	stageParse    stage = iota // rejected at parser (unknown kind, bad schema)
	stageValidate              // rejected at validator/capability-ceiling
	stageExecute               // compiled OK but executor refuses (v0 legacy, etc.)
	stageSuccess               // executed end-to-end; manifest contains expected writes
)

type fixture struct {
	file         string   // path relative to test_skills/
	expect       stage    // where the pipeline must stop
	reasonSubstr string   // substring the error message must contain (stage != success)
	wantWrites   []string // expected manifest write paths (stage == success)
}

var fixtures = []fixture{
	// ---- success ----
	{
		file:       "demo_writefile.loom.json",
		expect:     stageSuccess,
		wantWrites: []string{"out/hello.txt"},
	},
	{
		file:       "multiwrite.loom.json",
		expect:     stageSuccess,
		wantWrites: []string{"out/first.txt", "out/second.txt"},
	},
	{
		file:       "nested_write.loom.json",
		expect:     stageSuccess,
		wantWrites: []string{"out/nested/deep/report.md"},
	},
	// ---- validator rejects ----
	{
		file:         "reject_path_escape.loom.json",
		expect:       stageValidate,
		reasonSubstr: "capability",
	},
	{
		file:         "reject_uncapped.loom.json",
		expect:       stageValidate,
		reasonSubstr: "capability",
	},
	{
		file:         "reject_highrisk_scope.loom.json",
		expect:       stageValidate,
		reasonSubstr: "high-risk",
	},
	{
		file:         "reject_v0_shell.md",
		expect:       stageValidate,
		reasonSubstr: "dangerous command",
	},
	{
		file:         "reject_v0_ssrf.md",
		expect:       stageValidate,
		reasonSubstr: "SSRF",
	},

	// ---- parser rejects ----
	{
		file:         "reject_unknown_kind.loom.json",
		expect:       stageParse,
		reasonSubstr: "unknown step kind",
	},
	{
		file:         "reject_wrong_version.loom.json",
		expect:       stageParse,
		reasonSubstr: "schema_version",
	},

	// ---- executor rejects v0 ----
	{
		// demo_cleaner.md is a valid v0 skill: parser + validator both pass,
		// but the executor refuses because the schema is not v1.
		file:         "demo_cleaner.md",
		expect:       stageExecute,
		reasonSubstr: "not executable",
	},
}

func TestTestSkillFixtures(t *testing.T) {
	// t.Parallel is incompatible with the per-subtest t.Setenv below
	// (Go's testing package refuses the combination). Sequential is fine;
	// the fixture matrix is small and each case is I/O-bound.
	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.file, func(t *testing.T) {
			runFixture(t, fx)
		})
	}
}

func runFixture(t *testing.T, fx fixture) {
	t.Helper()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	skillPath := filepath.Join("..", "test_skills", fx.file)
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read fixture %q: %v", skillPath, err)
	}

	skill, parseErr := parser.ParseFile(skillPath, raw)
	if fx.expect == stageParse {
		mustFailWith(t, parseErr, fx.reasonSubstr, "parse")
		return
	}
	if parseErr != nil {
		t.Fatalf("parse error = %v, want success", parseErr)
	}

	workspaceRoot := t.TempDir()
	compiler := &engine.Compiler{
		Policy:        security.DefaultPolicy(),
		WorkspaceRoot: workspaceRoot,
	}

	sessionID := "fixture-" + strings.ReplaceAll(fx.file, "/", "_")
	vfs, sanitizedInputs, compileErr := compiler.CompileAndSetup(
		skill,
		defaultInputsFor(skill.Parameters),
		sessionID,
	)
	if fx.expect == stageValidate {
		mustFailWith(t, compileErr, fx.reasonSubstr, "validate")
		return
	}
	if compileErr != nil {
		t.Fatalf("compile error = %v, want success", compileErr)
	}

	executor := &engine.Executor{VFS: vfs}
	_, execErr := executor.Execute(context.Background(), skill, sanitizedInputs)
	if fx.expect == stageExecute {
		mustFailWith(t, execErr, fx.reasonSubstr, "execute")
		assertWorkspaceUntouched(t, workspaceRoot)
		return
	}
	if execErr != nil {
		t.Fatalf("execute error = %v, want success", execErr)
	}

	assertWorkspaceUntouched(t, workspaceRoot)
	assertManifestWrites(t, vfs, fx.wantWrites)
}

func mustFailWith(t *testing.T, err error, reasonSubstr, stageName string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s succeeded, want failure containing %q", stageName, reasonSubstr)
	}

	// Accept both ContractError and SecurityError shapes; the pipeline emits
	// either depending on which rule fired.
	var (
		contractErr *engine.ContractError
		securityErr *engine.SecurityError
	)
	isKnown := errors.As(err, &contractErr) || errors.As(err, &securityErr)
	if !isKnown && stageName != "parse" && stageName != "execute" {
		t.Logf("note: %s returned an error of type %T, not Contract/Security", stageName, err)
	}

	if reasonSubstr != "" && !strings.Contains(err.Error(), reasonSubstr) {
		t.Fatalf("%s error %q does not contain %q", stageName, err.Error(), reasonSubstr)
	}
}

func assertWorkspaceUntouched(t *testing.T, workspaceRoot string) {
	t.Helper()
	entries, err := os.ReadDir(workspaceRoot)
	if err != nil {
		t.Fatalf("read workspace root: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("workspace root mutated, saw entries: %v", names)
	}
}

func assertManifestWrites(t *testing.T, vfs *engine.ShadowVFS, wantWrites []string) {
	t.Helper()
	manifest, err := vfs.Manifest()
	if err != nil {
		t.Fatalf("manifest error: %v", err)
	}

	got := make([]string, 0, len(manifest))
	for _, change := range manifest {
		if change.Op != engine.ChangeOpWrite {
			continue
		}
		got = append(got, change.Path)
	}

	if len(got) != len(wantWrites) {
		t.Fatalf("manifest writes = %v, want %v", got, wantWrites)
	}
	for _, want := range wantWrites {
		if !containsString(got, want) {
			t.Fatalf("manifest missing %q (got %v)", want, got)
		}
	}
}

func containsString(haystack []string, needle string) bool {
	for _, candidate := range haystack {
		if candidate == needle {
			return true
		}
	}
	return false
}

func defaultInputsFor(params map[string]engine.Parameter) map[string]string {
	inputs := make(map[string]string, len(params))
	for name, param := range params {
		if param.DefaultValue != "" {
			inputs[name] = param.DefaultValue
			continue
		}
		if !param.Required {
			continue
		}
		switch param.Type {
		case engine.ParameterTypeString:
			inputs[name] = "fixture-value"
		case engine.ParameterTypeInt:
			inputs[name] = "0"
		case engine.ParameterTypeBool:
			inputs[name] = "false"
		case engine.ParameterTypeFloat:
			inputs[name] = "0"
		}
	}
	return inputs
}
