package engine

import (
	"errors"
	"testing"

	"github.com/yourname/loom-cli/internal/security"
)

func TestValidateSkillAcceptsValidDataflow(t *testing.T) {
	t.Parallel()

	skill := &LoomSkill{
		Parameters: map[string]Parameter{
			"prompt": {Type: ParameterTypeString, Required: true},
		},
		ExecutionDAG: []Step{
			{
				StepID: "step_1",
				Action: "render",
				Inputs: map[string]string{
					"text": "${prompt}",
				},
				Outputs: []string{"output_1"},
			},
			{
				StepID: "step_2",
				Action: "store",
				Inputs: map[string]string{
					"body": "${output_1}",
				},
				Outputs: []string{"final_result"},
			},
		},
		Permissions: map[string][]string{
			"fs.read": {"./workspace"},
		},
	}

	if err := ValidateSkill(skill, security.DefaultPolicy()); err != nil {
		t.Fatalf("ValidateSkill() error = %v", err)
	}
}

func TestValidateSkillRejectsUndeclaredDependency(t *testing.T) {
	t.Parallel()

	skill := &LoomSkill{
		Parameters: map[string]Parameter{},
		ExecutionDAG: []Step{
			{
				StepID: "step_2",
				Action: "store",
				Inputs: map[string]string{
					"body": "${output_1}",
				},
			},
		},
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

func TestValidateSkillRejectsHighRiskPermission(t *testing.T) {
	t.Parallel()

	skill := &LoomSkill{
		Permissions: map[string][]string{
			"fs.read": {"/etc"},
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
	if securityErr.Field != "fs.read" {
		t.Fatalf("securityErr.Field = %q, want fs.read", securityErr.Field)
	}
}

func TestValidateSkillRejectsDangerousCommand(t *testing.T) {
	t.Parallel()

	skill := &LoomSkill{
		ExecutionDAG: []Step{
			{
				StepID: "step_1",
				Action: "shell",
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

func TestValidateSkillRejectsBlockedSSRFAddress(t *testing.T) {
	t.Parallel()

	skill := &LoomSkill{
		ExecutionDAG: []Step{
			{
				StepID: "step_1",
				Action: "fetch",
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
