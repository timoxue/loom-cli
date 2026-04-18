package engine

import (
	"fmt"
	"path/filepath"
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

// SanitizeShadowRelPath normalizes a caller-supplied path and guarantees it
// stays inside the shadow root. It is the only approved gateway for turning
// untrusted path strings into executor-visible relative paths.
//
// Rejection criteria: empty input, absolute paths, Windows drive prefixes,
// and any clean form whose relative-to-shadow path climbs out via "..". The
// returned path is normalized with forward slashes for manifest stability.
func SanitizeShadowRelPath(shadowDir, rawPath string) (string, error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return "", &ContractError{
			Field:  "path",
			Reason: "path must not be empty",
		}
	}

	shadowRoot, err := normalizeRootPath(shadowDir)
	if err != nil {
		return "", &ContractError{
			Field:  "shadow_dir",
			Reason: err.Error(),
		}
	}

	relativePath, err := sanitizeCallerRelativePath(rawPath, shadowRoot)
	if err != nil {
		return "", err
	}

	return filepath.ToSlash(relativePath), nil
}

// sanitizeCallerRelativePath mirrors resolveManagedRelativePath but scopes
// the acceptance test to the shadow root only. The sanitizer must reject
// before the executor ever invokes ShadowVFS.
func sanitizeCallerRelativePath(rawPath, shadowRoot string) (string, error) {
	normalized := filepath.Clean(filepath.FromSlash(strings.TrimSpace(rawPath)))

	if filepath.VolumeName(normalized) != "" && !filepath.IsAbs(normalized) {
		return "", &SecurityError{
			Field:  rawPath,
			Reason: "drive-qualified relative paths are not allowed",
		}
	}

	if filepath.IsAbs(normalized) {
		return "", &SecurityError{
			Field:  rawPath,
			Reason: "absolute paths are not allowed inside the shadow root",
		}
	}

	// On Windows, a leading slash is "rooted" but not "absolute" (no drive).
	// Treat it as an escape attempt so Unix-style /etc/passwd is rejected
	// regardless of host OS.
	if len(normalized) > 0 && (normalized[0] == '/' || normalized[0] == '\\') {
		return "", &SecurityError{
			Field:  rawPath,
			Reason: "rooted paths are not allowed inside the shadow root",
		}
	}

	candidate, err := joinWithinBase(shadowRoot, normalized)
	if err != nil {
		return "", &SecurityError{
			Field:  rawPath,
			Reason: "path escapes shadow root",
		}
	}

	relative, err := filepath.Rel(shadowRoot, candidate)
	if err != nil {
		return "", &SecurityError{
			Field:  rawPath,
			Reason: fmt.Sprintf("resolve relative path: %v", err),
		}
	}
	if relative == "." || relative == "" {
		return "", &SecurityError{
			Field:  rawPath,
			Reason: "path must not address the shadow root itself",
		}
	}
	if escapesBase(relative) {
		return "", &SecurityError{
			Field:  rawPath,
			Reason: "path escapes shadow root",
		}
	}

	return relative, nil
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
