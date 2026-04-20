package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/timoxue/loom-cli/internal/engine"
	"github.com/timoxue/loom-cli/internal/engine/parser"
	"github.com/timoxue/loom-cli/internal/security"
)

const (
	colorRed   = "\x1b[31m"
	colorGreen = "\x1b[32m"
	colorReset = "\x1b[0m"
)

func main() {
	rootCmd := newRootCmd()
	if err := rootCmd.Execute(); err != nil {
		printErrorAndExit(err)
	}
}

func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "loom",
		Short:         "A Deterministic AI Skill Gateway",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	rootCmd.AddCommand(newVerifyCmd())
	rootCmd.AddCommand(newRunCmd())
	rootCmd.AddCommand(newCommitCmd())
	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newMigrateOpenClawCmd())
	rootCmd.AddCommand(newAcceptMigrationCmd())
	return rootCmd
}

// newCommitCmd is the promotion boundary. `loom run` only populates the
// shadow; `loom commit <session>` is the sole path that copies bytes into
// the real workspace. Without --yes, it prints a dry-run preview and
// exits without mutating anything. With --yes, it promotes and removes
// the shadow directory.
func newCommitCmd() *cobra.Command {
	var confirm bool

	commitCmd := &cobra.Command{
		Use:   "commit <session_id>",
		Short: "Promote a completed shadow workspace into the real workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := args[0]

			gate := &engine.CommitGate{}
			receipt, err := gate.LoadReceipt(sessionID)
			if err != nil {
				return err
			}

			manifest, err := gate.Preview(receipt)
			if err != nil {
				return err
			}
			engine.PrintManifest(os.Stdout, manifest)

			if !confirm {
				fmt.Fprintln(os.Stdout, "")
				fmt.Fprintln(os.Stdout, "(dry-run) no workspace changes made. Re-run with --yes to promote.")
				return nil
			}

			if err := gate.Promote(receipt); err != nil {
				return err
			}

			printSuccess(
				fmt.Sprintf("\u2705 Promoted %d change(s) to workspace", len(manifest)),
				fmt.Sprintf("\U0001F511 Skill ID: %s", receipt.SkillID),
				fmt.Sprintf("\U0001F4C2 Workspace: %s", receipt.WorkspaceRoot),
			)
			return nil
		},
	}

	commitCmd.Flags().BoolVar(&confirm, "yes", false, "actually promote; without this the command only previews")
	return commitCmd
}

// newRunCmd exposes the v1 end-to-end execution path: parse → compile →
// executor → commit-gate manifest. The commit gate today prints the shadow
// manifest and stops; it never promotes bytes into the real workspace.
//
// `--accept-draft` is a shorthand for `--draft-policy=allow` scoped to
// this invocation only. Use `LOOM_DRAFT_POLICY` (refuse | warn | allow)
// to change the default across every command in the shell.
func newRunCmd() *cobra.Command {
	var inputFlags []string
	var draftPolicyFlag string
	var acceptDraft bool

	runCmd := &cobra.Command{
		Use:   "run <skill_file_path>",
		Short: "Execute a v1 skill inside an isolated shadow workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			skillFilePath := args[0]

			rawContent, err := os.ReadFile(skillFilePath)
			if err != nil {
				return fmt.Errorf("read skill file %q: %w", skillFilePath, err)
			}

			skill, err := parser.ParseFile(skillFilePath, rawContent)
			if err != nil {
				return err
			}

			if skill.SchemaVersion != engine.CurrentSchemaVersion {
				return fmt.Errorf("schema %q is not executable; run requires schema_version=%q", skill.SchemaVersion, engine.CurrentSchemaVersion)
			}

			draftPolicy, err := resolveDraftPolicy(draftPolicyFlag, acceptDraft)
			if err != nil {
				return err
			}

			policy := security.DefaultPolicy()
			compiler := &engine.Compiler{
				Policy:        policy,
				WorkspaceRoot: ".",
			}

			sessionID, err := newSessionID()
			if err != nil {
				return err
			}

			rawInputs, err := parseInputFlags(inputFlags)
			if err != nil {
				return err
			}

			vfs, sanitizedInputs, err := compiler.CompileAndSetup(skill, rawInputs, sessionID)
			if err != nil {
				return err
			}

			executor := &engine.Executor{
				VFS:         vfs,
				DraftPolicy: draftPolicy,
			}
			manifest, execErr := executor.Execute(context.Background(), skill, sanitizedInputs)

			engine.PrintManifest(os.Stdout, manifest)

			if execErr != nil {
				return execErr
			}

			printSuccess(
				fmt.Sprintf("\u2705 Skill executed inside shadow workspace"),
				fmt.Sprintf("\U0001F511 Skill ID: %s", skill.SkillID),
				fmt.Sprintf("\U0001F6E1\uFE0F  Logical Hash: %s", skill.GetLogicalHash()),
				fmt.Sprintf("\U0001F4C2 Shadow Path: %s", vfs.ShadowDir),
			)
			fmt.Fprintf(os.Stdout, "\nTo promote to workspace, run:\n  loom commit %s --yes\n", sessionID)
			return nil
		},
	}

	runCmd.Flags().StringSliceVar(&inputFlags, "input", nil, "repeatable key=value input pair")
	runCmd.Flags().StringVar(&draftPolicyFlag, "draft-policy", "", "how to treat unreviewed migration drafts: refuse|warn|allow (default: refuse, can also be set via LOOM_DRAFT_POLICY)")
	runCmd.Flags().BoolVar(&acceptDraft, "accept-draft", false, "shorthand for --draft-policy=allow, scoped to this invocation")
	return runCmd
}

// resolveDraftPolicy reconciles (in precedence order) the --accept-draft
// shorthand, the explicit --draft-policy flag, the LOOM_DRAFT_POLICY
// environment variable, and the built-in "refuse" default.
func resolveDraftPolicy(flagValue string, acceptDraft bool) (engine.DraftPolicy, error) {
	if acceptDraft {
		return engine.DraftPolicyAllow, nil
	}
	if flagValue == "" {
		flagValue = os.Getenv("LOOM_DRAFT_POLICY")
	}
	switch strings.ToLower(strings.TrimSpace(flagValue)) {
	case "", "refuse":
		return engine.DraftPolicyRefuse, nil
	case "warn":
		return engine.DraftPolicyWarn, nil
	case "allow":
		return engine.DraftPolicyAllow, nil
	default:
		return "", fmt.Errorf("invalid draft policy %q (expected refuse|warn|allow)", flagValue)
	}
}

func parseInputFlags(flags []string) (map[string]string, error) {
	inputs := make(map[string]string, len(flags))
	for _, flag := range flags {
		idx := strings.IndexByte(flag, '=')
		if idx <= 0 {
			return nil, fmt.Errorf("invalid --input %q: expected key=value", flag)
		}
		inputs[flag[:idx]] = flag[idx+1:]
	}
	return inputs, nil
}

func newVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify <skill_file_path>",
		Short: "Statically verify a skill definition",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			skillFilePath := args[0]

			rawContent, err := os.ReadFile(skillFilePath)
			if err != nil {
				return fmt.Errorf("read skill file %q: %w", skillFilePath, err)
			}

			skill, err := parser.ParseFile(skillFilePath, rawContent)
			if err != nil {
				return err
			}

			policy := security.DefaultPolicy()
			compiler := &engine.Compiler{
				Policy:        policy,
				WorkspaceRoot: ".",
			}

			sessionID, err := newSessionID()
			if err != nil {
				return err
			}

			mockInputs, err := buildVerificationInputs(skill.Parameters)
			if err != nil {
				return err
			}

			if _, _, err := compiler.CompileAndSetup(skill, mockInputs, sessionID); err != nil {
				return err
			}

			printSuccess(
				fmt.Sprintf("\u2705 Skill Verified Successfully"),
				fmt.Sprintf("\U0001F511 Skill ID: %s", skill.SkillID),
				fmt.Sprintf("\U0001F6E1\uFE0F Logical Hash: %s", skill.GetLogicalHash()),
				fmt.Sprintf("\U0001F4C2 Shadow Workspace Ready."),
			)

			return nil
		},
	}
}

func buildVerificationInputs(schema map[string]engine.Parameter) (map[string]string, error) {
	mockInputs := make(map[string]string, len(schema))

	for name, parameter := range schema {
		if parameter.DefaultValue != "" {
			mockInputs[name] = parameter.DefaultValue
			continue
		}

		switch parameter.Type {
		case engine.ParameterTypeString:
			mockInputs[name] = "mock-value"
		case engine.ParameterTypeInt:
			mockInputs[name] = "0"
		case engine.ParameterTypeBool:
			mockInputs[name] = "false"
		case engine.ParameterTypeFloat:
			mockInputs[name] = "0"
		default:
			return nil, &engine.ContractError{
				Field:  name,
				Reason: fmt.Sprintf("unsupported parameter type %q for verification", parameter.Type),
			}
		}
	}

	return mockInputs, nil
}

func newSessionID() (string, error) {
	var randomBytes [8]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", fmt.Errorf("generate session id entropy: %w", err)
	}

	return fmt.Sprintf("verify-%d-%s", time.Now().UTC().UnixNano(), hex.EncodeToString(randomBytes[:])), nil
}

func printSuccess(lines ...string) {
	for _, line := range lines {
		fmt.Fprintf(os.Stdout, "%s%s%s\n", colorGreen, line, colorReset)
	}
}

func printErrorAndExit(err error) {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "unknown error"
	}

	fmt.Fprintf(os.Stderr, "%sError: %s%s\n", colorRed, message, colorReset)
	os.Exit(1)
}
