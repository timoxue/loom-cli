package engine

import (
	"errors"
	"testing"

	"github.com/timoxue/loom-cli/internal/security"
)

func TestSanitizeInputAppliesDefaultAndConvertsTypes(t *testing.T) {
	t.Parallel()

	schema := map[string]Parameter{
		"name": {
			Type:     ParameterTypeString,
			Required: true,
		},
		"retries": {
			Type:         ParameterTypeInt,
			DefaultValue: "3",
		},
		"verbose": {
			Type:         ParameterTypeBool,
			DefaultValue: "true",
		},
		"ratio": {
			Type:         ParameterTypeFloat,
			DefaultValue: "0.5",
		},
	}

	got, err := SanitizeInput(map[string]string{
		"name": "demo",
	}, schema)
	if err != nil {
		t.Fatalf("SanitizeInput() error = %v", err)
	}

	if value, ok := got["name"].(string); !ok || value != "demo" {
		t.Fatalf("name = %#v, want string demo", got["name"])
	}
	if value, ok := got["retries"].(int); !ok || value != 3 {
		t.Fatalf("retries = %#v, want int 3", got["retries"])
	}
	if value, ok := got["verbose"].(bool); !ok || !value {
		t.Fatalf("verbose = %#v, want bool true", got["verbose"])
	}
	if value, ok := got["ratio"].(float64); !ok || value != 0.5 {
		t.Fatalf("ratio = %#v, want float64 0.5", got["ratio"])
	}
}

func TestSanitizeInputRejectsUnknownField(t *testing.T) {
	t.Parallel()

	_, err := SanitizeInput(
		map[string]string{"unexpected": "value"},
		map[string]Parameter{},
	)
	if err == nil {
		t.Fatal("SanitizeInput() error = nil, want contract error")
	}

	var contractErr *ContractError
	if !errors.As(err, &contractErr) {
		t.Fatalf("error = %T, want *ContractError", err)
	}
	if contractErr.Field != "unexpected" {
		t.Fatalf("contractErr.Field = %q, want unexpected", contractErr.Field)
	}
}

func TestSanitizeInputRejectsShellInjection(t *testing.T) {
	t.Parallel()

	_, err := SanitizeInput(
		map[string]string{"cmd": "echo ok && rm -rf /"},
		map[string]Parameter{
			"cmd": {Type: ParameterTypeString, Required: true},
		},
	)
	if err == nil {
		t.Fatal("SanitizeInput() error = nil, want security error")
	}

	var securityErr *SecurityError
	if !errors.As(err, &securityErr) {
		t.Fatalf("error = %T, want *SecurityError", err)
	}
	if securityErr.Field != "cmd" {
		t.Fatalf("securityErr.Field = %q, want cmd", securityErr.Field)
	}
}

func TestRedactOutputRedactsCredentials(t *testing.T) {
	t.Parallel()

	policy := security.DefaultPolicy()
	raw := "Authorization: Bearer secret-token\nkey=sk-0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijkl"

	got := RedactOutput(raw, policy)

	want := "Authorization: [REDACTED]\nkey=[REDACTED]"
	if got != want {
		t.Fatalf("RedactOutput() = %q, want %q", got, want)
	}
}
