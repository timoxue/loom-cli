package main

import (
	"testing"

	"github.com/timoxue/loom-cli/internal/engine"
)

func TestBuildVerificationInputs(t *testing.T) {
	t.Parallel()

	inputs, err := buildVerificationInputs(map[string]engine.Parameter{
		"name": {
			Type:     engine.ParameterTypeString,
			Required: true,
		},
		"retries": {
			Type:         engine.ParameterTypeInt,
			DefaultValue: "3",
		},
		"enabled": {
			Type: engine.ParameterTypeBool,
		},
		"ratio": {
			Type: engine.ParameterTypeFloat,
		},
	})
	if err != nil {
		t.Fatalf("buildVerificationInputs() error = %v", err)
	}

	if got := inputs["name"]; got != "mock-value" {
		t.Fatalf("name = %q, want mock-value", got)
	}
	if got := inputs["retries"]; got != "3" {
		t.Fatalf("retries = %q, want 3", got)
	}
	if got := inputs["enabled"]; got != "false" {
		t.Fatalf("enabled = %q, want false", got)
	}
	if got := inputs["ratio"]; got != "0" {
		t.Fatalf("ratio = %q, want 0", got)
	}
}
