package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yourname/loom-cli/internal/engine"
	"github.com/yourname/loom-cli/internal/engine/parser"
	"github.com/yourname/loom-cli/internal/security"
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
	rootCmd.AddCommand(newServeCmd())
	return rootCmd
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
