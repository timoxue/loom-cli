package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ExecutionError tags a runtime failure with the step that produced it so the
// commit gate and audit surfaces can render a precise attribution line.
type ExecutionError struct {
	StepID string
	Reason string
}

func (e *ExecutionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("execution failed at step %q: %s", e.StepID, e.Reason)
}

// DraftPolicy controls how the executor treats skills whose provenance
// marks them as unreviewed (e.g. fresh output of `loom migrate-openclaw`).
// The default is DraftPolicyRefuse — a migrated skill has to go through
// `loom accept-migration` before it can run. Teams with their own review
// workflow can relax this via CLI flag or LOOM_DRAFT_POLICY env var.
type DraftPolicy string

const (
	// DraftPolicyRefuse blocks any skill with Provenance.Reviewed=false.
	// This is the safe default — migrated skills cannot execute until a
	// human has explicitly signed off.
	DraftPolicyRefuse DraftPolicy = "refuse"

	// DraftPolicyWarn prints a warning but proceeds. For teams that have
	// moved review into a downstream system and need loom to not block.
	DraftPolicyWarn DraftPolicy = "warn"

	// DraftPolicyAllow executes unreviewed drafts without complaint. For
	// CI and trusted automation only. Never appropriate for production
	// promotion flows — those should require explicit review.
	DraftPolicyAllow DraftPolicy = "allow"
)

// Executor runs admitted v1 skills inside the isolated shadow tree.
//
// Atomicity contract: any step error aborts the run immediately. The shadow
// workspace is left intact for post-mortem inspection but is never promoted
// to the real workspace — the commit gate alone performs promotion, and it
// refuses to promote after an abort.
//
// DraftPolicy governs whether the executor allows skills whose provenance
// marks them unreviewed. Stubs (Provenance.Mode == ProvenanceModeStub) are
// always refused, even under DraftPolicyAllow — a stub by definition has
// no runnable body.
type Executor struct {
	VFS          *ShadowVFS
	DraftPolicy  DraftPolicy // zero value means refuse; set explicitly otherwise
	DraftWarning io.Writer   // where DraftPolicyWarn emits warnings (defaults to os.Stderr)
}

// Execute walks the DAG in declaration order and dispatches each Step
// through the registered typed handlers. A partial Manifest is returned on
// failure so operators can see exactly which paths were touched before the
// abort.
func (e *Executor) Execute(ctx context.Context, skill *LoomSkill, sanitizedInputs map[string]any) ([]Change, error) {
	if e == nil || e.VFS == nil {
		return nil, &ContractError{
			Field:  "executor",
			Reason: "executor is not initialized with a ShadowVFS",
		}
	}
	if skill == nil {
		return nil, &ContractError{
			Field:  "skill",
			Reason: "skill is nil",
		}
	}
	if skill.SchemaVersion != CurrentSchemaVersion {
		return nil, &ContractError{
			Field:  "schema_version",
			Reason: fmt.Sprintf("schema %q is not executable; expected %q", skill.SchemaVersion, CurrentSchemaVersion),
		}
	}

	if err := e.enforceDraftPolicy(skill); err != nil {
		return nil, err
	}

	for _, step := range skill.ExecutionDAG {
		if err := ctx.Err(); err != nil {
			return e.safeManifest(), err
		}
		if err := e.dispatch(step, sanitizedInputs); err != nil {
			return e.safeManifest(), err
		}
	}

	return e.safeManifest(), nil
}

// dispatch substitutes caller inputs into the step's args and routes to
// the typed handler. Substitution happens AFTER admission (the logical
// hash was computed against the pre-substitution args) and BEFORE any I/O,
// so a substitution error aborts the run before the shadow is touched.
func (e *Executor) dispatch(step Step, inputs map[string]any) error {
	if step.Args == nil {
		return &ExecutionError{
			StepID: step.StepID,
			Reason: "step has no args",
		}
	}

	substituted, err := step.Args.substituteInputs(inputs)
	if err != nil {
		return &ExecutionError{StepID: step.StepID, Reason: err.Error()}
	}

	switch args := substituted.(type) {
	case ReadFileArgs:
		return e.handleReadFile(step, args)
	case WriteFileArgs:
		return e.handleWriteFile(step, args)
	case LegacyStepArgs:
		return &ExecutionError{
			StepID: step.StepID,
			Reason: "legacy v0 steps are not executable",
		}
	default:
		return &ExecutionError{
			StepID: step.StepID,
			Reason: fmt.Sprintf("unsupported step kind %q", step.Kind),
		}
	}
}

func (e *Executor) handleReadFile(step Step, args ReadFileArgs) error {
	sanitized, err := SanitizeShadowRelPath(e.VFS.ShadowDir, args.Path)
	if err != nil {
		return &ExecutionError{StepID: step.StepID, Reason: err.Error()}
	}

	resolved, err := e.VFS.ResolveReadPath(sanitized)
	if err != nil {
		return &ExecutionError{StepID: step.StepID, Reason: err.Error()}
	}
	if _, err := os.ReadFile(resolved); err != nil {
		return &ExecutionError{StepID: step.StepID, Reason: fmt.Sprintf("read %q: %v", resolved, err)}
	}
	return nil
}

func (e *Executor) handleWriteFile(step Step, args WriteFileArgs) error {
	sanitized, err := SanitizeShadowRelPath(e.VFS.ShadowDir, args.Path)
	if err != nil {
		return &ExecutionError{StepID: step.StepID, Reason: err.Error()}
	}

	target, err := e.VFS.ResolveWritePath(sanitized)
	if err != nil {
		return &ExecutionError{StepID: step.StepID, Reason: err.Error()}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return &ExecutionError{StepID: step.StepID, Reason: fmt.Sprintf("create parent for %q: %v", target, err)}
	}
	if err := os.WriteFile(target, []byte(args.Content), 0o644); err != nil {
		return &ExecutionError{StepID: step.StepID, Reason: fmt.Sprintf("write %q: %v", target, err)}
	}
	return nil
}

// enforceDraftPolicy implements the three-axis check that runs before
// any step dispatches:
//
//  1. Stubs (ProvenanceModeStub) are refused unconditionally. A stub has
//     no runnable body; there is nothing "allow" could mean.
//  2. Reviewed skills (Provenance.Reviewed == true) with a valid
//     signature pass. Mismatched signatures are treated as unreviewed —
//     a hand-edit that sets "reviewed": true without also computing the
//     signature is semantically equivalent to not having reviewed at all.
//  3. Unreviewed skills obey DraftPolicy: refuse, warn-then-continue, or
//     silently allow.
//
// Skills without a Provenance block (hand-authored) always pass — draft
// policy only bites on migration output.
func (e *Executor) enforceDraftPolicy(skill *LoomSkill) error {
	if skill.Provenance == nil {
		return nil
	}

	if skill.Provenance.Mode == ProvenanceModeStub {
		return &ContractError{
			Field:  "_provenance.mode",
			Reason: "stub skills are never executable; they exist as placeholders for manual rewrite",
		}
	}

	if skill.Provenance.Reviewed {
		expected, err := CanonicalBodyHash(skill)
		if err != nil {
			return &ContractError{
				Field:  "_provenance",
				Reason: fmt.Sprintf("compute canonical body hash: %v", err),
			}
		}
		if skill.Provenance.ReviewerSignature != expected {
			return &ContractError{
				Field:  "_provenance.reviewer_signature",
				Reason: "reviewer_signature does not match canonical body; the reviewed flag appears to be hand-set. Run `loom accept-migration` to review properly.",
			}
		}
		return nil
	}

	policy := e.DraftPolicy
	if policy == "" {
		policy = DraftPolicyRefuse
	}

	switch policy {
	case DraftPolicyRefuse:
		return &ContractError{
			Field:  "_provenance.reviewed",
			Reason: "skill is a migration draft (reviewed=false). Run `loom accept-migration <path>` to review it, or use --accept-draft to bypass just this run.",
		}
	case DraftPolicyWarn:
		writer := e.DraftWarning
		if writer == nil {
			writer = os.Stderr
		}
		fmt.Fprintf(writer, "loom: warning: running unreviewed migration draft %q (mode=%s)\n", skill.SkillID, skill.Provenance.Mode)
		return nil
	case DraftPolicyAllow:
		return nil
	default:
		return &ContractError{
			Field:  "draft_policy",
			Reason: fmt.Sprintf("unknown draft policy %q", policy),
		}
	}
}

func (e *Executor) safeManifest() []Change {
	if e == nil || e.VFS == nil {
		return nil
	}
	manifest, err := e.VFS.Manifest()
	if err != nil {
		return nil
	}
	return manifest
}
