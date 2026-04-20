package engine

import (
	"errors"
	"testing"

	"github.com/timoxue/loom-cli/internal/security"
)

func TestValidateSkillAcceptsValidDataflowV1(t *testing.T) {
	t.Parallel()

	skill := &LoomSkill{
		SchemaVersion: CurrentSchemaVersion,
		Parameters: map[string]Parameter{
			"prompt": {Type: ParameterTypeString, Required: true},
		},
		ExecutionDAG: []Step{
			{
				StepID:  "step_1",
				Kind:    StepKindWriteFile,
				Args:    WriteFileArgs{Path: "workspace/out.txt", Content: "${prompt}"},
				Inputs:  map[string]string{"text": "${prompt}"},
				Outputs: []string{"output_1"},
			},
			{
				StepID: "step_2",
				Kind:   StepKindReadFile,
				Args:   ReadFileArgs{Path: "workspace/out.txt"},
				Inputs: map[string]string{"body": "${output_1}"},
			},
		},
		Capabilities: []Capability{
			{Kind: CapKindVFSRead, Scope: "workspace/"},
			{Kind: CapKindVFSWrite, Scope: "workspace/"},
		},
	}

	if err := ValidateSkill(skill, security.DefaultPolicy()); err != nil {
		t.Fatalf("ValidateSkill() error = %v", err)
	}
}

func TestValidateSkillRejectsUndeclaredDependency(t *testing.T) {
	t.Parallel()

	skill := &LoomSkill{
		SchemaVersion: CurrentSchemaVersion,
		ExecutionDAG: []Step{
			{
				StepID: "step_2",
				Kind:   StepKindWriteFile,
				Args:   WriteFileArgs{Path: "out/body.txt", Content: "${output_1}"},
				Inputs: map[string]string{"body": "${output_1}"},
			},
		},
		Capabilities: []Capability{{Kind: CapKindVFSWrite, Scope: "out/"}},
	}

	err := ValidateSkill(skill, security.DefaultPolicy())
	if err == nil {
		t.Fatal("ValidateSkill() error = nil, want structure error")
	}

	var structureErr *StructureError
	if !errors.As(err, &structureErr) {
		t.Fatalf("error = %T, want *StructureError", err)
	}
	if structureErr.Field != "step_2" {
		t.Fatalf("structureErr.Field = %q, want step_2", structureErr.Field)
	}
}

func TestValidateSkillRejectsHighRiskCapabilityScope(t *testing.T) {
	t.Parallel()

	skill := &LoomSkill{
		SchemaVersion: CurrentSchemaVersion,
		Capabilities: []Capability{
			{Kind: CapKindVFSRead, Scope: "/etc"},
		},
	}

	err := ValidateSkill(skill, security.DefaultPolicy())
	if err == nil {
		t.Fatal("ValidateSkill() error = nil, want security error")
	}

	var securityErr *SecurityError
	if !errors.As(err, &securityErr) {
		t.Fatalf("error = %T, want *SecurityError", err)
	}
	if securityErr.Field != string(CapKindVFSRead) {
		t.Fatalf("securityErr.Field = %q, want %q", securityErr.Field, CapKindVFSRead)
	}
}

func TestValidateSkillRejectsStepExceedingDeclaredCapabilityScope(t *testing.T) {
	t.Parallel()

	skill := &LoomSkill{
		SchemaVersion: CurrentSchemaVersion,
		ExecutionDAG: []Step{
			{
				StepID: "step_1",
				Kind:   StepKindWriteFile,
				Args:   WriteFileArgs{Path: "other/secret.txt", Content: "x"},
			},
		},
		Capabilities: []Capability{
			{Kind: CapKindVFSWrite, Scope: "out/"},
		},
	}

	err := ValidateSkill(skill, security.DefaultPolicy())
	if err == nil {
		t.Fatal("ValidateSkill() error = nil, want security error")
	}

	var securityErr *SecurityError
	if !errors.As(err, &securityErr) {
		t.Fatalf("error = %T, want *SecurityError", err)
	}
	if securityErr.Field != "step_1" {
		t.Fatalf("securityErr.Field = %q, want step_1", securityErr.Field)
	}
}

func TestValidateSkillRejectsDangerousCommandInLegacyStep(t *testing.T) {
	t.Parallel()

	skill := &LoomSkill{
		// v0 legacy skill — action text is still scanned.
		ExecutionDAG: []Step{
			{
				StepID: "step_1",
				Kind:   StepKindLegacy,
				Args:   LegacyStepArgs{Action: "shell"},
				Inputs: map[string]string{
					"command": "rm -rf /tmp/demo",
				},
			},
		},
	}

	err := ValidateSkill(skill, security.DefaultPolicy())
	if err == nil {
		t.Fatal("ValidateSkill() error = nil, want security error")
	}

	var securityErr *SecurityError
	if !errors.As(err, &securityErr) {
		t.Fatalf("error = %T, want *SecurityError", err)
	}
	if securityErr.Field != "step_1" {
		t.Fatalf("securityErr.Field = %q, want step_1", securityErr.Field)
	}
}

func TestValidateSkillRejectsBlockedSSRFAddressInLegacyStep(t *testing.T) {
	t.Parallel()

	skill := &LoomSkill{
		ExecutionDAG: []Step{
			{
				StepID: "step_1",
				Kind:   StepKindLegacy,
				Args:   LegacyStepArgs{Action: "fetch"},
				Inputs: map[string]string{
					"url": "http://169.254.169.254/latest/meta-data",
				},
			},
		},
	}

	err := ValidateSkill(skill, security.DefaultPolicy())
	if err == nil {
		t.Fatal("ValidateSkill() error = nil, want security error")
	}

	var securityErr *SecurityError
	if !errors.As(err, &securityErr) {
		t.Fatalf("error = %T, want *SecurityError", err)
	}
	if securityErr.Field != "step_1" {
		t.Fatalf("securityErr.Field = %q, want step_1", securityErr.Field)
	}
}
