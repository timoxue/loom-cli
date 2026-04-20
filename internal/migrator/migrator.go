// Package migrator converts OpenClaw v0 markdown skills into v1
// typed-IR `.loom.json` drafts. Its output is always marked unreviewed
// — `loom run` refuses to execute migration products until a human
// passes them through `loom accept-migration`. See the plan at
// plans/shimmering-riding-lark.md for the full design rationale.
package migrator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/timoxue/loom-cli/internal/engine"
	"github.com/timoxue/loom-cli/internal/engine/parser"
)

// MechanicalMode controls how aggressively the Tier 1 regex classifier
// tries to match OpenClaw action text. Documented in the plan.
type MechanicalMode string

const (
	MechanicalModeConservative MechanicalMode = "conservative"
	MechanicalModeAggressive   MechanicalMode = "aggressive"
	MechanicalModeOff          MechanicalMode = "off"
)

// ConflictMode decides what happens when a target file already exists
// at the destination path. Mirrors Hermes's semantics.
type ConflictMode string

const (
	ConflictModeSkip      ConflictMode = "skip"
	ConflictModeRename    ConflictMode = "rename"
	ConflictModeOverwrite ConflictMode = "overwrite"
)

// Options configures a single Migrate invocation. SourceDir is read-only;
// OutDir is where new .loom.json files land. PromptTemplateVersion is
// stamped into provenance so we can invalidate old drafts when the
// prompt changes materially.
type Options struct {
	SourceDir             string
	OutDir                string
	MechanicalMode        MechanicalMode
	ConflictMode          ConflictMode
	NoLLM                 bool
	ForceReMigrateReviewed bool
	Execute               bool // false = dry run (preview only)
	LLMClient             LLMClient
	PromptTemplateVersion string
	Now                   func() time.Time // injectable for tests
}

// LLMClient abstracts the Tier 2 translator so we can swap providers or
// stub out in tests. Translate gets the original action text (and a few
// neighboring context lines from the skill markdown) and must return a
// valid v1 LoomSkill body. Returning an error causes the migrator to
// emit a stub with the error as stub_reason.
type LLMClient interface {
	Translate(ctx TranslateContext) (*engine.LoomSkill, error)
	Name() string // e.g. "claude-sonnet-4-6"
}

// TranslateContext is what the migrator hands to an LLMClient. It
// intentionally excludes filesystem paths so LLMs cannot be tricked
// into reasoning about where the skill lives on disk.
type TranslateContext struct {
	SkillID        string
	Description    string
	Parameters     map[string]engine.Parameter
	LegacyActions  []string // natural-language action text lines, in order
	AllowedKinds   []engine.StepKind
	PromptTemplate string
}

// ItemStatus reports the outcome of processing a single OpenClaw skill.
// Written into the per-skill entry of the Report.
type ItemStatus string

const (
	ItemStatusMechanical  ItemStatus = "mechanical"
	ItemStatusLLMAssisted ItemStatus = "llm_assisted"
	ItemStatusStub        ItemStatus = "stub"
	ItemStatusSkipped     ItemStatus = "skipped"
	ItemStatusOverwritten ItemStatus = "overwritten"
	ItemStatusRenamed     ItemStatus = "renamed"
	ItemStatusError       ItemStatus = "error"
	ItemStatusRefused     ItemStatus = "refused_reviewed_target"
)

// ItemReport records what happened for one source skill. Destination is
// empty for dry-run or error cases. StubReason is populated only when
// Status == Stub.
type ItemReport struct {
	SourcePath     string     `json:"source_path"`
	SourceRelative string     `json:"source_relative"`
	SkillID        string     `json:"skill_id"`
	Destination    string     `json:"destination,omitempty"`
	Status         ItemStatus `json:"status"`
	StubReason     string     `json:"stub_reason,omitempty"`
	ErrorMessage   string     `json:"error,omitempty"`
}

// Report is the full output of one migrate run — both the machine-
// readable per-skill list and the aggregate capability-gap data the
// roadmap can use as a signal (not as autopilot).
type Report struct {
	MigratedAt      time.Time                `json:"migrated_at"`
	Options         ReportOptions            `json:"options"`
	Counts          map[string]int           `json:"counts"`
	CapabilityGaps  map[string][]string      `json:"capability_gaps,omitempty"`
	Items           []ItemReport             `json:"per_skill"`
}

// ReportOptions echoes the knobs that shaped this run, so a report read
// back later is self-describing.
type ReportOptions struct {
	SourceDir      string `json:"source_dir"`
	OutDir         string `json:"out_dir"`
	MechanicalMode string `json:"mechanical_mode"`
	ConflictMode   string `json:"conflict_mode"`
	NoLLM          bool   `json:"no_llm"`
	Execute        bool   `json:"execute"`
}

// Migrate walks SourceDir for OpenClaw `.md` files, classifies each
// through the three-tier pipeline (mechanical → LLM → stub), and (if
// Execute=true) writes `.loom.json` drafts into OutDir. A Report is
// returned regardless of Execute so callers can render a preview.
//
// Source files are never written to. OutDir is created if missing.
func Migrate(opts Options) (*Report, error) {
	if opts.SourceDir == "" {
		return nil, fmt.Errorf("migrator: SourceDir required")
	}
	if opts.OutDir == "" {
		return nil, fmt.Errorf("migrator: OutDir required")
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.MechanicalMode == "" {
		opts.MechanicalMode = MechanicalModeConservative
	}
	if opts.ConflictMode == "" {
		opts.ConflictMode = ConflictModeSkip
	}
	if opts.PromptTemplateVersion == "" {
		opts.PromptTemplateVersion = "migrate-openclaw-v1"
	}

	absSource, err := filepath.Abs(opts.SourceDir)
	if err != nil {
		return nil, fmt.Errorf("resolve source dir: %w", err)
	}

	report := &Report{
		MigratedAt: opts.Now(),
		Options: ReportOptions{
			SourceDir:      absSource,
			OutDir:         opts.OutDir,
			MechanicalMode: string(opts.MechanicalMode),
			ConflictMode:   string(opts.ConflictMode),
			NoLLM:          opts.NoLLM,
			Execute:        opts.Execute,
		},
		Counts:         map[string]int{},
		CapabilityGaps: map[string][]string{},
	}

	entries, err := collectSourceSkills(absSource)
	if err != nil {
		return nil, fmt.Errorf("collect source skills: %w", err)
	}

	if opts.Execute {
		if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
			return nil, fmt.Errorf("create out dir: %w", err)
		}
	}

	for _, entry := range entries {
		item := processSkill(opts, entry, absSource)
		report.Items = append(report.Items, item)
		report.Counts[string(item.Status)]++

		if item.Status == ItemStatusStub {
			// Gap categorization looks at the original legacy action
			// text, not the stub reason — the reason tracks WHY we
			// couldn't translate (e.g. "LLM disabled"), but what we
			// actually want for the roadmap signal is WHAT capability
			// was requested. Pull from the parsed v0 skill on the entry.
			if entry.Skill != nil {
				kind := inferCapabilityGap(entry.Skill)
				if kind != "" {
					report.CapabilityGaps[kind] = append(report.CapabilityGaps[kind], item.SkillID)
				}
			}
		}
	}

	// Sort capability-gap lists for stable output.
	for kind := range report.CapabilityGaps {
		sort.Strings(report.CapabilityGaps[kind])
	}

	return report, nil
}

// sourceEntry is an internal bundle: a resolved source file and its
// parsed v0 LoomSkill. We read raw bytes once so SourceHash is stable
// across the two separate uses (matching + writing).
type sourceEntry struct {
	AbsPath     string
	RelPath     string
	RawContent  []byte
	Skill       *engine.LoomSkill
	ParseError  error
}

// collectSourceSkills walks absSource for `.md` files and tries to
// parse each. Parse failures are recorded on the entry so the per-skill
// report can mention them rather than aborting the whole run.
func collectSourceSkills(absSource string) ([]sourceEntry, error) {
	info, err := os.Stat(absSource)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source %q is not a directory", absSource)
	}

	var entries []sourceEntry
	walkErr := filepath.WalkDir(absSource, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			entries = append(entries, sourceEntry{
				AbsPath:    path,
				RelPath:    relOrAbs(absSource, path),
				ParseError: readErr,
			})
			return nil
		}
		skill, parseErr := parser.ParseFile(path, raw)
		entries = append(entries, sourceEntry{
			AbsPath:    path,
			RelPath:    relOrAbs(absSource, path),
			RawContent: raw,
			Skill:      skill,
			ParseError: parseErr,
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].RelPath < entries[j].RelPath })
	return entries, nil
}

func relOrAbs(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return filepath.ToSlash(rel)
}

// processSkill runs one source entry through the classify → emit pipeline
// and returns its ItemReport. File I/O happens only if opts.Execute.
func processSkill(opts Options, entry sourceEntry, absSource string) ItemReport {
	if entry.ParseError != nil {
		return ItemReport{
			SourcePath:     entry.AbsPath,
			SourceRelative: entry.RelPath,
			Status:         ItemStatusError,
			ErrorMessage:   fmt.Sprintf("parse: %v", entry.ParseError),
		}
	}
	if entry.Skill == nil {
		return ItemReport{
			SourcePath:     entry.AbsPath,
			SourceRelative: entry.RelPath,
			Status:         ItemStatusError,
			ErrorMessage:   "parser returned nil skill without error",
		}
	}

	skillID := entry.Skill.SkillID
	targetName := skillID + ".loom.json"
	targetPath := filepath.Join(opts.OutDir, targetName)

	absTarget, absErr := filepath.Abs(targetPath)
	if absErr != nil {
		return ItemReport{
			SourcePath:     entry.AbsPath,
			SourceRelative: entry.RelPath,
			SkillID:        skillID,
			Status:         ItemStatusError,
			ErrorMessage:   fmt.Sprintf("resolve target path: %v", absErr),
		}
	}

	// Conflict handling needs to happen BEFORE any classification work
	// so we don't waste LLM calls on a skill we're about to skip.
	preEmptStatus, renamedPath, conflictErr := resolveConflict(opts, absTarget)
	if conflictErr != nil {
		return ItemReport{
			SourcePath:     entry.AbsPath,
			SourceRelative: entry.RelPath,
			SkillID:        skillID,
			Destination:    absTarget,
			Status:         ItemStatusRefused,
			ErrorMessage:   conflictErr.Error(),
		}
	}
	switch preEmptStatus {
	case ItemStatusSkipped:
		return ItemReport{
			SourcePath:     entry.AbsPath,
			SourceRelative: entry.RelPath,
			SkillID:        skillID,
			Destination:    absTarget,
			Status:         ItemStatusSkipped,
		}
	case ItemStatusRenamed:
		absTarget = renamedPath
	}

	sourceHash := hashBytes(entry.RawContent)
	migratedAt := opts.Now()

	// Classify: try Tier 1, else Tier 2, else stub.
	classified, classifyErr := classify(opts, entry)
	var skill *engine.LoomSkill
	var mode engine.ProvenanceMode
	var model string
	var promptTemplate string
	var stubReason string

	switch {
	case classified != nil && classifyErr == nil && classified.Mode == MatchMechanical:
		skill = classified.Skill
		mode = engine.ProvenanceModeMechanical
	case classified != nil && classifyErr == nil && classified.Mode == MatchLLM:
		skill = classified.Skill
		mode = engine.ProvenanceModeLLMAssisted
		model = classified.Model
		promptTemplate = opts.PromptTemplateVersion
	default:
		skill = stubSkillFor(entry.Skill)
		mode = engine.ProvenanceModeStub
		if classifyErr != nil {
			stubReason = classifyErr.Error()
		} else if opts.NoLLM {
			stubReason = "LLM disabled by --no-llm"
		} else if opts.LLMClient == nil {
			stubReason = "LLM not available: no client configured (set ANTHROPIC_API_KEY or --no-llm to acknowledge)"
		} else {
			stubReason = "no mechanical match and LLM translation produced no usable result"
		}
	}

	// Stamp provenance.
	skill.Provenance = &engine.Provenance{
		Origin:         "openclaw-migrate",
		Mode:           mode,
		Model:          model,
		PromptTemplate: promptTemplate,
		SourcePath:     entry.RelPath,
		SourceHash:     sourceHash,
		StubReason:     stubReason,
		MigratedAt:     migratedAt,
		Reviewed:       false,
	}

	writeStatus := ItemStatus("")
	switch mode {
	case engine.ProvenanceModeMechanical:
		writeStatus = ItemStatusMechanical
	case engine.ProvenanceModeLLMAssisted:
		writeStatus = ItemStatusLLMAssisted
	case engine.ProvenanceModeStub:
		writeStatus = ItemStatusStub
	}
	if preEmptStatus == ItemStatusRenamed {
		writeStatus = ItemStatusRenamed
	} else if preEmptStatus == ItemStatusOverwritten {
		writeStatus = ItemStatusOverwritten
	}

	if !opts.Execute {
		return ItemReport{
			SourcePath:     entry.AbsPath,
			SourceRelative: entry.RelPath,
			SkillID:        skillID,
			Destination:    absTarget,
			Status:         writeStatus,
			StubReason:     stubReason,
		}
	}

	if err := writeSkill(absTarget, skill); err != nil {
		return ItemReport{
			SourcePath:     entry.AbsPath,
			SourceRelative: entry.RelPath,
			SkillID:        skillID,
			Destination:    absTarget,
			Status:         ItemStatusError,
			ErrorMessage:   fmt.Sprintf("write: %v", err),
		}
	}

	return ItemReport{
		SourcePath:     entry.AbsPath,
		SourceRelative: entry.RelPath,
		SkillID:        skillID,
		Destination:    absTarget,
		Status:         writeStatus,
		StubReason:     stubReason,
	}
}

func hashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// resolveConflict inspects the target path and reports what the caller
// should do. When ConflictMode is rename, it chooses the next available
// name and returns that via renamedPath.
//
// refusedErr is returned only for the specific case of overwriting a
// REVIEWED target without --force-re-migrate-reviewed — that's the
// "human review must not be silently destroyed" guarantee.
func resolveConflict(opts Options, targetPath string) (status ItemStatus, renamedPath string, refusedErr error) {
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		return "", "", nil // no conflict
	} else if err != nil {
		return "", "", fmt.Errorf("stat target: %w", err)
	}

	switch opts.ConflictMode {
	case ConflictModeSkip:
		return ItemStatusSkipped, "", nil
	case ConflictModeRename:
		next, err := nextAvailableName(targetPath)
		if err != nil {
			return "", "", err
		}
		return ItemStatusRenamed, next, nil
	case ConflictModeOverwrite:
		reviewed, err := isTargetReviewed(targetPath)
		if err != nil {
			return "", "", err
		}
		if reviewed && !opts.ForceReMigrateReviewed {
			return "", "", fmt.Errorf("target is human-reviewed; pass --force-re-migrate-reviewed to overwrite")
		}
		return ItemStatusOverwritten, "", nil
	default:
		return "", "", fmt.Errorf("unknown conflict mode %q", opts.ConflictMode)
	}
}

func nextAvailableName(basePath string) (string, error) {
	dir := filepath.Dir(basePath)
	base := filepath.Base(basePath)
	ext := ""
	stem := base
	if strings.HasSuffix(strings.ToLower(base), ".loom.json") {
		ext = base[len(base)-len(".loom.json"):]
		stem = base[:len(base)-len(".loom.json")]
	}
	for i := 1; i < 10000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s-imported-%d%s", stem, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("exhausted rename suffixes for %q", basePath)
}

func isTargetReviewed(targetPath string) (bool, error) {
	raw, err := os.ReadFile(targetPath)
	if err != nil {
		return false, err
	}
	skill, err := parser.ParseFile(targetPath, raw)
	if err != nil {
		// Unparsable target: treat as unreviewed so re-migrate can fix it.
		return false, nil
	}
	if skill.Provenance == nil {
		return false, nil
	}
	return skill.Provenance.Reviewed, nil
}

// inferCapabilityGap scans the legacy skill's action text for keywords
// that suggest which step kind would have been needed. Conservative —
// returns "" when it can't tell. Better to under-report than to
// mislead the roadmap into prioritizing a capability that wasn't
// actually requested.
func inferCapabilityGap(legacy *engine.LoomSkill) string {
	for _, action := range extractLegacyActions(legacy) {
		lower := strings.ToLower(action)
		switch {
		case strings.Contains(lower, "shell") ||
			strings.Contains(lower, "run command") ||
			strings.Contains(lower, "os_command") ||
			strings.Contains(lower, "execute") && strings.Contains(lower, "script"):
			return "os_command"
		case strings.Contains(lower, "http://") ||
			strings.Contains(lower, "https://") ||
			strings.Contains(lower, "fetch") ||
			strings.Contains(lower, "curl") ||
			strings.Contains(lower, "url"):
			return "http_call"
		}
	}
	return ""
}

// WriteReport renders the Report as indented JSON for the developer
// and as a short human summary for the terminal. out receives the
// prose summary; the JSON goes to reportPath.
func WriteReport(out io.Writer, report *Report, reportPath string) error {
	if reportPath != "" {
		if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
			return fmt.Errorf("create report dir: %w", err)
		}
		raw, err := jsonMarshalIndent(report)
		if err != nil {
			return err
		}
		if err := os.WriteFile(reportPath, raw, 0o644); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
	}

	if out != nil {
		fmt.Fprintln(out, "Migration summary:")
		for _, status := range sortedStatuses(report.Counts) {
			fmt.Fprintf(out, "  %-15s  %d\n", status, report.Counts[status])
		}
		if len(report.CapabilityGaps) > 0 {
			fmt.Fprintln(out, "\nCapability gaps observed:")
			for _, kind := range sortedKeys(report.CapabilityGaps) {
				names := report.CapabilityGaps[kind]
				fmt.Fprintf(out, "  %-12s  %d skill(s): %s\n", kind, len(names), strings.Join(names, ", "))
			}
		}
	}
	return nil
}

func sortedStatuses(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
