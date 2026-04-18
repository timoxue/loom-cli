package engine

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/yourname/loom-cli/internal/security"
)

const redactedOutput = "[REDACTED]"

var shellInjectionMarkers = []string{
	";",
	"|",
	"&&",
	"$(",
	"`",
}

// ContractError indicates that caller-provided input violates the declared skill contract.
type ContractError struct {
	Field  string // Names the contract field that failed validation.
	Reason string // Describes the exact contract violation.
}

func (e *ContractError) Error() string {
	if e == nil {
		return "<nil>"
	}

	if strings.TrimSpace(e.Field) == "" {
		return "contract violation: " + e.Reason
	}

	return fmt.Sprintf("contract violation for %q: %s", e.Field, e.Reason)
}

// SecurityError indicates that input was rejected because it crossed a security boundary.
type SecurityError struct {
	Field  string // Names the offending input field when available.
	Reason string // Describes the security trigger that caused rejection.
}

func (e *SecurityError) Error() string {
	if e == nil {
		return "<nil>"
	}

	if strings.TrimSpace(e.Field) == "" {
		return "security violation: " + e.Reason
	}

	return fmt.Sprintf("security violation for %q: %s", e.Field, e.Reason)
}

// SanitizeInput enforces required/default semantics, strong typing, and shell-injection rejection.
func SanitizeInput(rawInputs map[string]string, schema map[string]Parameter) (map[string]any, error) {
	if schema == nil {
		if len(rawInputs) > 0 {
			return nil, &ContractError{
				Reason: "received inputs for an empty schema",
			}
		}
		return map[string]any{}, nil
	}

	for name := range rawInputs {
		if _, exists := schema[name]; !exists {
			return nil, &ContractError{
				Field:  name,
				Reason: "input is not declared in schema",
			}
		}
	}

	sanitized := make(map[string]any, len(schema))
	for name, parameter := range schema {
		rawValue, found := rawInputs[name]
		if !found {
			switch {
			case parameter.Required:
				return nil, &ContractError{
					Field:  name,
					Reason: "missing required input",
				}
			case parameter.DefaultValue != "":
				rawValue = parameter.DefaultValue
			default:
				continue
			}
		}

		value, err := sanitizeParameterValue(name, rawValue, parameter)
		if err != nil {
			return nil, err
		}

		sanitized[name] = value
	}

	return sanitized, nil
}

// RedactOutput removes credential-shaped secrets from runtime output using precompiled policy rules.
func RedactOutput(rawOutput string, policy *security.SecurityPolicy) string {
	if policy == nil {
		return rawOutput
	}

	redacted := rawOutput
	for index := range policy.Credentials {
		rule := &policy.Credentials[index]
		if rule.Action != security.RegexActionRedact {
			continue
		}

		compiledPattern := rule.CompiledPattern()
		if compiledPattern == nil {
			panic(fmt.Sprintf("security policy credential rule %q is not compiled", rule.Name))
		}

		redacted = compiledPattern.ReplaceAllString(redacted, redactedOutput)
	}

	return redacted
}

func sanitizeParameterValue(name, rawValue string, parameter Parameter) (any, error) {
	switch parameter.Type {
	case ParameterTypeString:
		if err := rejectShellInjection(name, rawValue); err != nil {
			return nil, err
		}
		return rawValue, nil
	case ParameterTypeInt:
		value, err := strconv.Atoi(rawValue)
		if err != nil {
			return nil, &ContractError{
				Field:  name,
				Reason: fmt.Sprintf("expected int, got %q", rawValue),
			}
		}
		return value, nil
	case ParameterTypeBool:
		value, err := strconv.ParseBool(rawValue)
		if err != nil {
			return nil, &ContractError{
				Field:  name,
				Reason: fmt.Sprintf("expected bool, got %q", rawValue),
			}
		}
		return value, nil
	case ParameterTypeFloat:
		value, err := strconv.ParseFloat(rawValue, 64)
		if err != nil {
			return nil, &ContractError{
				Field:  name,
				Reason: fmt.Sprintf("expected float, got %q", rawValue),
			}
		}
		return value, nil
	default:
		return nil, &ContractError{
			Field:  name,
			Reason: fmt.Sprintf("unsupported parameter type %q", parameter.Type),
		}
	}
}

func rejectShellInjection(fieldName, value string) error {
	for _, marker := range shellInjectionMarkers {
		if strings.Contains(value, marker) {
			return &SecurityError{
				Field:  fieldName,
				Reason: fmt.Sprintf("detected shell injection marker %q", marker),
			}
		}
	}

	return nil
}
