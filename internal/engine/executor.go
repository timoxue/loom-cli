package engine

import (
	"context"
	"fmt"
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

// Executor runs admitted v1 skills inside the isolated shadow tree.
//
// Atomicity contract: any step error aborts the run immediately. The shadow
// workspace is left intact for post-mortem inspection but is never promoted
// to the real workspace — the commit gate alone performs promotion, and it
// refuses to promote after an abort.
type Executor struct {
	VFS *ShadowVFS
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

	for _, step := range skill.ExecutionDAG {
		if err := ctx.Err(); err != nil {
			return e.safeManifest(), err
		}
		if err := e.dispatch(step); err != nil {
			return e.safeManifest(), err
		}
	}

	return e.safeManifest(), nil
}

func (e *Executor) dispatch(step Step) error {
	switch args := step.Args.(type) {
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
