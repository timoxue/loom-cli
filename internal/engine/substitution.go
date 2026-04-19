package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// UnknownVariableError reports a `${name}` reference that the sanitized
// inputs map did not bind. The missing name is included verbatim so error
// messages stay actionable across the CLI, executor, and audit surfaces.
type UnknownVariableError struct {
	Name string
}

func (e *UnknownVariableError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("unknown variable %q", e.Name)
}

// UnsupportedInputTypeError reports a sanitized input value whose dynamic
// type is not one of the four primitive Parameter types. Reaching this
// error means the sanitizer produced an unexpected shape — it is an
// internal invariant violation rather than a user error.
type UnsupportedInputTypeError struct {
	Name  string
	Value any
}

func (e *UnsupportedInputTypeError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("unsupported input type for %q: %T", e.Name, e.Value)
}

// substituteString expands every `${name}` occurrence in s using inputs.
//
// Grammar: `${` followed by an identifier matching `[A-Za-z_][A-Za-z0-9_]*`
// and a closing `}`. This is identical to what the validator accepts as a
// variable reference (see extractVariableReferences in validator.go) so the
// two layers agree on what counts as a substitution.
//
// Behavior:
//   - unknown variable → UnknownVariableError (fail-fast; never silent-empty)
//   - unterminated `${` → contract error
//   - malformed identifier inside braces → contract error
//   - literal `$` without `{` → passes through untouched
//   - non-primitive input value → UnsupportedInputTypeError
func substituteString(s string, inputs map[string]any) (string, error) {
	var builder strings.Builder
	builder.Grow(len(s))

	remaining := s
	for {
		markerIndex := strings.Index(remaining, "${")
		if markerIndex < 0 {
			builder.WriteString(remaining)
			return builder.String(), nil
		}

		builder.WriteString(remaining[:markerIndex])
		rest := remaining[markerIndex+2:]

		closeIndex := strings.IndexByte(rest, '}')
		if closeIndex < 0 {
			return "", &ContractError{
				Field:  "args",
				Reason: fmt.Sprintf("unterminated variable reference starting at %q", remaining[markerIndex:]),
			}
		}

		name := rest[:closeIndex]
		if !isVariableReference(name) {
			return "", &ContractError{
				Field:  "args",
				Reason: fmt.Sprintf("invalid variable reference %q", name),
			}
		}

		value, exists := inputs[name]
		if !exists {
			return "", &UnknownVariableError{Name: name}
		}

		stringified, err := canonicalString(name, value)
		if err != nil {
			return "", err
		}
		builder.WriteString(stringified)

		remaining = rest[closeIndex+1:]
	}
}

// ComputeInputDigest returns a sha256 fingerprint of the sanitized caller
// inputs. It deliberately does NOT use json.Marshal: JSON value encoding
// silently collapses type distinctions (int(1) and float64(1) both render as
// `1`), which would make the digest depend on how callers happened to
// construct the map. Instead this walks sorted keys and writes
// `key\x00typeTag\x00canonicalString(value)\x00` per entry, so the digest
// is stable under ordering and type-precise across paths.
//
// A non-primitive value is a sanitizer-contract violation and returns a
// typed UnsupportedInputTypeError — never a silent degenerate digest.
func ComputeInputDigest(inputs map[string]any) (string, error) {
	hasher := sha256.New()

	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		value := inputs[name]
		tag, str, err := typeTagAndCanonicalString(name, value)
		if err != nil {
			return "", err
		}
		hasher.Write([]byte(name))
		hasher.Write([]byte{0})
		hasher.Write([]byte(tag))
		hasher.Write([]byte{0})
		hasher.Write([]byte(str))
		hasher.Write([]byte{0})
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func typeTagAndCanonicalString(name string, value any) (string, string, error) {
	switch v := value.(type) {
	case string:
		return "s", v, nil
	case int:
		return "i", strconv.Itoa(v), nil
	case int64:
		return "i64", strconv.FormatInt(v, 10), nil
	case bool:
		return "b", strconv.FormatBool(v), nil
	case float64:
		return "f", strconv.FormatFloat(v, 'g', -1, 64), nil
	case float32:
		return "f32", strconv.FormatFloat(float64(v), 'g', -1, 64), nil
	default:
		return "", "", &UnsupportedInputTypeError{Name: name, Value: value}
	}
}

// canonicalString renders a primitive input value in a single, stable form.
// It does NOT go through fmt.Sprint because fmt's float formatting is
// locale/path-dependent in surprising ways; strconv pins the form.
func canonicalString(name string, value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case bool:
		return strconv.FormatBool(v), nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	case float32:
		return strconv.FormatFloat(float64(v), 'g', -1, 64), nil
	default:
		return "", &UnsupportedInputTypeError{Name: name, Value: value}
	}
}
