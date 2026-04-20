package migrator

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/timoxue/loom-cli/internal/engine"
)

// MatchKind distinguishes the two tiers that produced a successful
// classification. Stub falls through the classify() path to caller-side
// handling.
type MatchKind string

const (
	MatchMechanical MatchKind = "mechanical"
	MatchLLM        MatchKind = "llm"
)

// Match is what classify() returns when it succeeds — enough information
// for processSkill() to stamp correct provenance. When classify returns
// (nil, err), the caller emits a stub with err as the stub reason.
type Match struct {
	Mode  MatchKind
	Skill *engine.LoomSkill // body-only; caller attaches Provenance
	Model string            // populated for LLM-tier matches only
}

// classify runs the Tier 1 → Tier 2 pipeline for one source entry.
//
// Pipeline semantics:
//  1. If mechanical mode is off, skip straight to Tier 2.
//  2. Otherwise try the regex matcher. Success → return Mechanical.
//  3. If no mechanical match, and LLM is disabled (NoLLM or nil client),
//     return (nil, reason) so caller emits a stub.
//  4. Otherwise call LLM. On shape-valid response, return LLM. On
//     failure, return (nil, err) so caller emits a stub.
//
// This function is deliberately strict — anything ambiguous falls
// through to stub rather than producing a half-right v1 body.
func classify(opts Options, entry sourceEntry) (*Match, error) {
	if entry.Skill == nil {
		return nil, fmt.Errorf("no parsed v0 skill")
	}

	if opts.MechanicalMode != MechanicalModeOff {
		if match := matchMechanicalConservative(entry.Skill); match != nil {
			return &Match{Mode: MatchMechanical, Skill: match}, nil
		}
	}

	if opts.NoLLM {
		return nil, fmt.Errorf("LLM disabled by --no-llm and no mechanical match")
	}
	if opts.LLMClient == nil {
		return nil, fmt.Errorf("LLM not available: no client configured (set ANTHROPIC_API_KEY or --no-llm to acknowledge)")
	}

	ctx := TranslateContext{
		SkillID:        entry.Skill.SkillID,
		Description:    entry.Skill.Description,
		Parameters:     entry.Skill.Parameters,
		LegacyActions:  extractLegacyActions(entry.Skill),
		AllowedKinds:   []engine.StepKind{engine.StepKindReadFile, engine.StepKindWriteFile},
		PromptTemplate: opts.PromptTemplateVersion,
	}

	translated, err := opts.LLMClient.Translate(ctx)
	if err != nil {
		return nil, fmt.Errorf("LLM translation failed: %v", err)
	}
	if translated == nil {
		return nil, fmt.Errorf("LLM returned nil skill")
	}
	if translated.SchemaVersion != engine.CurrentSchemaVersion {
		return nil, fmt.Errorf("LLM output has wrong schema_version %q", translated.SchemaVersion)
	}

	return &Match{Mode: MatchLLM, Skill: translated, Model: opts.LLMClient.Name()}, nil
}

// extractLegacyActions pulls the action text out of StepKindLegacy
// steps (which is what the v0 markdown parser produces).
func extractLegacyActions(skill *engine.LoomSkill) []string {
	out := make([]string, 0, len(skill.ExecutionDAG))
	for _, step := range skill.ExecutionDAG {
		if legacy, ok := step.Args.(engine.LegacyStepArgs); ok {
			out = append(out, legacy.Action)
		}
	}
	return out
}

// Conservative Tier 1 patterns.
//
// Deliberately narrow. We match only single-step, single-verb actions
// whose arguments are unambiguously literal strings or bare variable
// references. Anything with conditionals ("if"/"when"/"unless"),
// multiple verbs ("then"/"and then"), or natural-language modifiers
// ("carefully"/"only if"/"after") falls through to LLM or stub.
//
// Rejecting marginal matches is safer than accepting them — a half-
// matched action silently drops semantics, and the Phase A+C plan was
// explicit that Tier 1 is an efficiency claim, not a trust claim.
var (
	// write ${var} to literal/path
	// e.g. "write ${content} to out/report.txt"
	mechanicalWriteVarToPath = regexp.MustCompile(
		`^\s*write\s+\$\{([A-Za-z_][A-Za-z0-9_]*)\}\s+to\s+([A-Za-z0-9_./\-]+)\s*\.?\s*$`,
	)
	// write literal to literal (content is a short literal string)
	// e.g. "write hi to out/hello.txt"
	mechanicalWriteLiteralToPath = regexp.MustCompile(
		`^\s*write\s+([A-Za-z0-9_\-]+)\s+to\s+([A-Za-z0-9_./\-]+)\s*\.?\s*$`,
	)
	// read ${var}
	mechanicalReadVar = regexp.MustCompile(
		`^\s*read\s+\$\{([A-Za-z_][A-Za-z0-9_]*)\}\s*\.?\s*$`,
	)
	// read literal/path
	mechanicalReadLiteral = regexp.MustCompile(
		`^\s*read\s+([A-Za-z0-9_./\-]+)\s*\.?\s*$`,
	)
)

// matchMechanicalConservative tries the conservative patterns in order.
// It returns a v1 LoomSkill body (no Provenance) on success.
//
// Multi-step v0 skills are always rejected at the conservative tier —
// translating two independent patterns into a correct DAG requires
// judgment we won't make without explicit LLM help.
func matchMechanicalConservative(legacy *engine.LoomSkill) *engine.LoomSkill {
	actions := extractLegacyActions(legacy)
	if len(actions) != 1 {
		return nil
	}
	action := actions[0]

	if m := mechanicalWriteVarToPath.FindStringSubmatch(action); m != nil {
		varName, path := m[1], m[2]
		if _, ok := legacy.Parameters[varName]; !ok {
			return nil // variable not declared; safer to bail than guess
		}
		return buildWriteFileSkill(legacy, path, "${"+varName+"}")
	}
	if m := mechanicalWriteLiteralToPath.FindStringSubmatch(action); m != nil {
		return buildWriteFileSkill(legacy, m[2], m[1])
	}
	if m := mechanicalReadVar.FindStringSubmatch(action); m != nil {
		varName := m[1]
		if _, ok := legacy.Parameters[varName]; !ok {
			return nil
		}
		return buildReadFileSkill(legacy, "${"+varName+"}")
	}
	if m := mechanicalReadLiteral.FindStringSubmatch(action); m != nil {
		return buildReadFileSkill(legacy, m[1])
	}

	return nil
}

func buildWriteFileSkill(legacy *engine.LoomSkill, path, content string) *engine.LoomSkill {
	return &engine.LoomSkill{
		SchemaVersion: engine.CurrentSchemaVersion,
		SkillID:       legacy.SkillID,
		Description:   legacy.Description,
		Parameters:    clonedParams(legacy.Parameters),
		ExecutionDAG: []engine.Step{
			{
				StepID: "s1",
				Kind:   engine.StepKindWriteFile,
				Args:   engine.WriteFileArgs{Path: path, Content: content},
				Inputs: inputsFromContent(content),
			},
		},
		Capabilities: []engine.Capability{
			{Kind: engine.CapKindVFSWrite, Scope: scopeForPath(path)},
		},
	}
}

func buildReadFileSkill(legacy *engine.LoomSkill, path string) *engine.LoomSkill {
	return &engine.LoomSkill{
		SchemaVersion: engine.CurrentSchemaVersion,
		SkillID:       legacy.SkillID,
		Description:   legacy.Description,
		Parameters:    clonedParams(legacy.Parameters),
		ExecutionDAG: []engine.Step{
			{
				StepID: "s1",
				Kind:   engine.StepKindReadFile,
				Args:   engine.ReadFileArgs{Path: path},
				Inputs: inputsFromContent(path),
			},
		},
		Capabilities: []engine.Capability{
			{Kind: engine.CapKindVFSRead, Scope: scopeForPath(path)},
		},
	}
}

func clonedParams(src map[string]engine.Parameter) map[string]engine.Parameter {
	if len(src) == 0 {
		return map[string]engine.Parameter{}
	}
	out := make(map[string]engine.Parameter, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// inputsFromContent extracts ${var} references and records them in the
// Step.Inputs map so validator dataflow checks pass. For literal strings
// (no ${}), returns an empty map.
func inputsFromContent(content string) map[string]string {
	if !strings.Contains(content, "${") {
		return map[string]string{}
	}
	re := regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	matches := re.FindAllStringSubmatch(content, -1)
	out := make(map[string]string, len(matches))
	for _, m := range matches {
		out[m[1]] = "${" + m[1] + "}"
	}
	return out
}

// scopeForPath picks a defensible default capability scope: the parent
// directory of the path, or the path itself if it names a root. The
// human reviewer can narrow or widen during accept-migration.
func scopeForPath(path string) string {
	if strings.Contains(path, "${") {
		// Variable-templated path — conservative scope is the dir prefix
		// up to the first templated segment.
		idx := strings.Index(path, "${")
		prefix := path[:idx]
		if prefix == "" {
			return "./"
		}
		return ensureTrailingSlash(prefix)
	}
	dir := path
	if slash := strings.LastIndex(path, "/"); slash >= 0 {
		dir = path[:slash+1]
	} else {
		dir = "./"
	}
	return ensureTrailingSlash(dir)
}

func ensureTrailingSlash(s string) string {
	if strings.HasSuffix(s, "/") {
		return s
	}
	return s + "/"
}
