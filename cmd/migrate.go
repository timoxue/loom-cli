package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/timoxue/loom-cli/internal/migrator"
)

// newMigrateOpenClawCmd wires the migrator package to the CLI. The
// command is two-phase by default: preview first, require --yes to
// actually write `.loom.json` drafts. This matches Hermes's claw
// migrate UX and prevents surprise writes.
func newMigrateOpenClawCmd() *cobra.Command {
	var (
		outDir              string
		mechanicalModeFlag  string
		conflictModeFlag    string
		noLLM               bool
		forceReMigrate      bool
		execute             bool
	)

	cmd := &cobra.Command{
		Use:   "migrate-openclaw <source_dir>",
		Short: "Compile OpenClaw markdown skills into Loom v1 drafts (requires review before execution)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sourceDir := args[0]

			mechMode, err := parseMechanicalMode(mechanicalModeFlag)
			if err != nil {
				return err
			}
			conflictMode, err := parseConflictMode(conflictModeFlag)
			if err != nil {
				return err
			}

			// Decide LLM client based on env + --no-llm. A nil client is
			// the explicit "graceful-degradation" signal to the migrator.
			llmClient := migrator.LLMClient(nil)
			if !noLLM {
				if cc := migrator.NewClaudeClient(os.Getenv("ANTHROPIC_API_KEY")); cc != nil {
					llmClient = cc
				}
			}

			opts := migrator.Options{
				SourceDir:              sourceDir,
				OutDir:                 outDir,
				MechanicalMode:         mechMode,
				ConflictMode:           conflictMode,
				NoLLM:                  noLLM,
				ForceReMigrateReviewed: forceReMigrate,
				Execute:                execute,
				LLMClient:              llmClient,
			}

			report, err := migrator.Migrate(opts)
			if err != nil {
				return err
			}

			reportPath := ""
			if execute {
				reportPath = filepath.Join(outDir, "_migration-report.json")
			}
			if err := migrator.WriteReport(os.Stdout, report, reportPath); err != nil {
				return err
			}

			if !execute {
				fmt.Fprintln(os.Stdout, "")
				fmt.Fprintln(os.Stdout, "(dry-run) no files written. Re-run with --yes to produce drafts.")
			} else {
				fmt.Fprintln(os.Stdout, "")
				fmt.Fprintf(os.Stdout, "Wrote drafts to %s. Every skill starts unreviewed — run `loom accept-migration` per file after review.\n", outDir)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&outDir, "out", "./skills/openclaw-imports", "where to write migrated .loom.json drafts")
	cmd.Flags().StringVar(&mechanicalModeFlag, "mechanical-mode", string(migrator.MechanicalModeConservative), "conservative|aggressive|off")
	cmd.Flags().StringVar(&conflictModeFlag, "on-conflict", string(migrator.ConflictModeSkip), "skip|rename|overwrite")
	cmd.Flags().BoolVar(&noLLM, "no-llm", false, "disable LLM-assisted Tier 2; all ambiguous skills become stubs")
	cmd.Flags().BoolVar(&forceReMigrate, "force-re-migrate-reviewed", false, "allow --on-conflict=overwrite to destroy a human-reviewed target (rarely correct)")
	cmd.Flags().BoolVar(&execute, "yes", false, "actually write files (default: preview only)")
	return cmd
}

// newAcceptMigrationCmd flips Provenance.Reviewed to true on a single
// migrated skill, after verifying the source file hasn't changed since
// migration. This is the ONLY supported way to earn a
// ReviewerSignature that the executor trusts.
func newAcceptMigrationCmd() *cobra.Command {
	var sourceRoot string

	cmd := &cobra.Command{
		Use:   "accept-migration <skill_file_path>",
		Short: "Mark a migrated skill as reviewed so `loom run` will execute it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := migrator.AcceptMigration(migrator.AcceptOptions{
				SkillPath:  args[0],
				SourceRoot: sourceRoot,
			})
			if err != nil {
				return err
			}
			printSuccess(
				fmt.Sprintf("\u2705 Accepted migration"),
				fmt.Sprintf("\U0001F511 Skill: %s", result.SkillPath),
				fmt.Sprintf("\U0001F4C4 Source verified: %s", result.SourcePath),
				fmt.Sprintf("\U0001F512 Reviewer signature: %s", truncateForPrint(result.Signature, 16)),
			)
			fmt.Fprintln(os.Stdout, "")
			fmt.Fprintln(os.Stdout, "The skill is now executable via `loom run`.")
			return nil
		},
	}

	cmd.Flags().StringVar(&sourceRoot, "source-root", "", "base directory for resolving provenance.source_path (required if source_path is relative)")
	return cmd
}

func parseMechanicalMode(v string) (migrator.MechanicalMode, error) {
	switch migrator.MechanicalMode(v) {
	case migrator.MechanicalModeConservative, migrator.MechanicalModeAggressive, migrator.MechanicalModeOff:
		return migrator.MechanicalMode(v), nil
	}
	return "", fmt.Errorf("invalid --mechanical-mode %q (expected conservative|aggressive|off)", v)
}

func parseConflictMode(v string) (migrator.ConflictMode, error) {
	switch migrator.ConflictMode(v) {
	case migrator.ConflictModeSkip, migrator.ConflictModeRename, migrator.ConflictModeOverwrite:
		return migrator.ConflictMode(v), nil
	}
	return "", fmt.Errorf("invalid --on-conflict %q (expected skip|rename|overwrite)", v)
}

func truncateForPrint(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
