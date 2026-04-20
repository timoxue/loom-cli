package migrator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/timoxue/loom-cli/internal/engine"
	"github.com/timoxue/loom-cli/internal/engine/parser"
)

// AcceptOptions configures a single review pass.
type AcceptOptions struct {
	SkillPath  string           // path to the migrated .loom.json
	SourceRoot string           // base dir containing the original OpenClaw file; Provenance.SourcePath is relative to this
	Now        func() time.Time // injectable for tests
}

// AcceptResult describes what AcceptMigration did.
type AcceptResult struct {
	SkillPath  string
	SourcePath string
	Reviewed   bool
	ReviewedAt time.Time
	Signature  string
}

// AcceptMigration re-reads the original OpenClaw source, recomputes its
// hash, compares it against the Provenance.SourceHash captured during
// migration, and — if they match — flips Reviewed to true and stamps
// ReviewerSignature.
//
// Refusals (each produces a distinct error message so humans can tell
// which safeguard tripped):
//   - skill missing Provenance
//   - skill already Reviewed (would double-sign; tell user so)
//   - stub (stubs cannot be reviewed; they must be rewritten)
//   - source file missing at Provenance.SourcePath
//   - source file hash differs from Provenance.SourceHash
//
// The skill file is rewritten atomically via the same temp+rename
// machinery migrate uses.
func AcceptMigration(opts AcceptOptions) (*AcceptResult, error) {
	if opts.SkillPath == "" {
		return nil, fmt.Errorf("accept: SkillPath required")
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}

	raw, err := os.ReadFile(opts.SkillPath)
	if err != nil {
		return nil, fmt.Errorf("read skill: %w", err)
	}
	skill, err := parser.ParseFile(opts.SkillPath, raw)
	if err != nil {
		return nil, fmt.Errorf("parse skill: %w", err)
	}

	if skill.Provenance == nil {
		return nil, fmt.Errorf("skill has no _provenance block; only migrated skills need accept-migration")
	}
	if skill.Provenance.Mode == engine.ProvenanceModeStub {
		return nil, fmt.Errorf("stub skills cannot be reviewed; rewrite the step body by hand instead")
	}
	if skill.Provenance.Reviewed {
		return nil, fmt.Errorf("skill is already reviewed (reviewed_at=%v); no action taken", ptrTimeString(skill.Provenance.ReviewedAt))
	}
	if skill.Provenance.SourcePath == "" {
		return nil, fmt.Errorf("provenance.source_path is empty; cannot verify source integrity")
	}
	if skill.Provenance.SourceHash == "" {
		return nil, fmt.Errorf("provenance.source_hash is empty; cannot verify source integrity")
	}

	sourcePath := skill.Provenance.SourcePath
	if !filepath.IsAbs(sourcePath) {
		if opts.SourceRoot == "" {
			return nil, fmt.Errorf("provenance.source_path is relative (%q) and --source-root was not provided", sourcePath)
		}
		sourcePath = filepath.Join(opts.SourceRoot, sourcePath)
	}

	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("read original source %q: %w", sourcePath, err)
	}
	sum := sha256.Sum256(sourceBytes)
	actualHash := hex.EncodeToString(sum[:])
	if actualHash != skill.Provenance.SourceHash {
		return nil, fmt.Errorf("source has changed since migration (%q): hash %s != recorded %s. Re-run `loom migrate-openclaw` and re-review",
			sourcePath, actualHash, skill.Provenance.SourceHash)
	}

	reviewedAt := opts.Now()
	skill.Provenance.Reviewed = true
	skill.Provenance.ReviewedAt = &reviewedAt

	signature, err := engine.CanonicalBodyHash(skill)
	if err != nil {
		return nil, fmt.Errorf("compute reviewer signature: %w", err)
	}
	skill.Provenance.ReviewerSignature = signature

	if err := writeSkill(opts.SkillPath, skill); err != nil {
		return nil, fmt.Errorf("write back reviewed skill: %w", err)
	}

	return &AcceptResult{
		SkillPath:  opts.SkillPath,
		SourcePath: sourcePath,
		Reviewed:   true,
		ReviewedAt: reviewedAt,
		Signature:  signature,
	}, nil
}

func ptrTimeString(t *time.Time) string {
	if t == nil {
		return "<nil>"
	}
	return t.Format(time.RFC3339)
}
