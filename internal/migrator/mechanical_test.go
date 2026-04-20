package migrator

import (
	"testing"

	"github.com/timoxue/loom-cli/internal/engine"
)

// legacySkillWithActions builds a v0-shape LoomSkill with the given
// action texts, each as a separate LegacyStepArgs step.
func legacySkillWithActions(skillID string, params map[string]engine.Parameter, actions ...string) *engine.LoomSkill {
	steps := make([]engine.Step, len(actions))
	for i, a := range actions {
		steps[i] = engine.Step{
			StepID: "step_" + string(rune('1'+i)),
			Kind:   engine.StepKindLegacy,
			Args:   engine.LegacyStepArgs{Action: a},
		}
	}
	return &engine.LoomSkill{
		SchemaVersion: "",
		SkillID:       skillID,
		Parameters:    params,
		ExecutionDAG:  steps,
	}
}

func TestMatchMechanicalConservativeHits(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		params     map[string]engine.Parameter
		action     string
		wantKind   engine.StepKind
		wantPath   string
		wantContent string
	}{
		{
			name:        "write literal to path",
			action:      "write hi to out/hello.txt",
			wantKind:    engine.StepKindWriteFile,
			wantPath:    "out/hello.txt",
			wantContent: "hi",
		},
		{
			name:        "write var to path",
			params:      map[string]engine.Parameter{"msg": {Type: engine.ParameterTypeString, Required: true}},
			action:      "write ${msg} to out/report.txt",
			wantKind:    engine.StepKindWriteFile,
			wantPath:    "out/report.txt",
			wantContent: "${msg}",
		},
		{
			name:     "read literal",
			action:   "read config/settings.yaml",
			wantKind: engine.StepKindReadFile,
			wantPath: "config/settings.yaml",
		},
		{
			name:     "read var",
			params:   map[string]engine.Parameter{"input_path": {Type: engine.ParameterTypeString, Required: true}},
			action:   "read ${input_path}",
			wantKind: engine.StepKindReadFile,
			wantPath: "${input_path}",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			legacy := legacySkillWithActions("demo", tc.params, tc.action)
			got := matchMechanicalConservative(legacy)
			if got == nil {
				t.Fatalf("expected match, got nil")
			}
			if len(got.ExecutionDAG) != 1 {
				t.Fatalf("got %d steps, want 1", len(got.ExecutionDAG))
			}
			step := got.ExecutionDAG[0]
			if step.Kind != tc.wantKind {
				t.Fatalf("step.Kind = %s, want %s", step.Kind, tc.wantKind)
			}
			switch tc.wantKind {
			case engine.StepKindWriteFile:
				args, ok := step.Args.(engine.WriteFileArgs)
				if !ok {
					t.Fatalf("step.Args = %T, want WriteFileArgs", step.Args)
				}
				if args.Path != tc.wantPath {
					t.Errorf("Path = %q, want %q", args.Path, tc.wantPath)
				}
				if args.Content != tc.wantContent {
					t.Errorf("Content = %q, want %q", args.Content, tc.wantContent)
				}
			case engine.StepKindReadFile:
				args, ok := step.Args.(engine.ReadFileArgs)
				if !ok {
					t.Fatalf("step.Args = %T, want ReadFileArgs", step.Args)
				}
				if args.Path != tc.wantPath {
					t.Errorf("Path = %q, want %q", args.Path, tc.wantPath)
				}
			}
			if len(got.Capabilities) == 0 {
				t.Fatal("expected non-empty capabilities")
			}
		})
	}
}

func TestMatchMechanicalConservativeMisses(t *testing.T) {
	t.Parallel()

	// These are the cases the conservative matcher MUST reject, so they
	// flow through to Tier 2 or stub. Each test case is something that
	// looks superficially tractable but would be a semantic trap if
	// half-matched.
	cases := []struct {
		name   string
		params map[string]engine.Parameter
		action string
	}{
		{
			name:   "conditional",
			action: "if the file exists, write foo to out/x.txt",
		},
		{
			name:   "multi-verb",
			action: "read out/x.txt and write summary to out/y.txt",
		},
		{
			name:   "multi-step natural language",
			action: "first write hi to out/a.txt, then write bye to out/b.txt",
		},
		{
			name:   "undeclared variable",
			params: map[string]engine.Parameter{}, // no msg
			action: "write ${msg} to out/report.txt",
		},
		{
			name:   "modifier word",
			action: "carefully write hi to out/x.txt",
		},
		{
			name:   "unknown verb",
			action: "delete out/stale.txt",
		},
		{
			name:   "fetch",
			action: "fetch http://example.com/data and store",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			legacy := legacySkillWithActions("demo", tc.params, tc.action)
			got := matchMechanicalConservative(legacy)
			if got != nil {
				t.Fatalf("expected miss, got match: %+v", got)
			}
		})
	}
}

func TestMatchMechanicalRejectsMultiStepSkills(t *testing.T) {
	t.Parallel()

	// Even if every individual step is a Tier 1 hit, the multi-step
	// skill goes to LLM — we don't auto-sequence multiple regex hits
	// into a DAG without explicit help.
	legacy := legacySkillWithActions(
		"multi",
		map[string]engine.Parameter{},
		"write a to out/a.txt",
		"write b to out/b.txt",
	)
	if got := matchMechanicalConservative(legacy); got != nil {
		t.Fatalf("expected miss for multi-step skill, got %+v", got)
	}
}
