package parser

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yourname/loom-cli/internal/engine"
)

// SkillParser translates an external skill document into the internal Loom IR.
type SkillParser interface {
	Parse(rawContent []byte) (*engine.LoomSkill, error)
}

// SyntaxError pinpoints a structural parsing failure in the source document.
type SyntaxError struct {
	Line   int    // Approximate 1-based line number of the malformed syntax, when known.
	Reason string // Precise reason why the parser rejected the source document.
}

func (e *SyntaxError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Line <= 0 {
		return "syntax error: " + e.Reason
	}

	return fmt.Sprintf("syntax error at line %d: %s", e.Line, e.Reason)
}

// ParseFile routes a skill document to the correct parser implementation based on file shape.
//
// Dispatch rules:
//   - .loom.json (or any .json with schema_version=="v1") → V1JSONParser
//   - .md → OpenClawParser (v0 legacy)
//   - no extension → detect markdown heuristically, else reject
//
// v0 and v1 live side-by-side intentionally: older markdown skills remain
// parseable (and therefore `verify`-able) but the executor refuses to run
// anything lacking a v1 schema version.
func ParseFile(filename string, rawContent []byte) (*engine.LoomSkill, error) {
	trimmed := strings.TrimSpace(filename)
	lower := strings.ToLower(trimmed)
	extension := strings.ToLower(filepath.Ext(trimmed))

	switch {
	case strings.HasSuffix(lower, ".loom.json"):
		return (&V1JSONParser{}).Parse(rawContent)
	case extension == ".json":
		return (&V1JSONParser{}).Parse(rawContent)
	case extension == ".md":
		return (&OpenClawParser{}).Parse(rawContent)
	case extension == "":
		if looksLikeOpenClaw(rawContent) {
			return (&OpenClawParser{}).Parse(rawContent)
		}
		return nil, fmt.Errorf("unsupported skill format")
	default:
		return nil, fmt.Errorf("unsupported skill format: %s", extension)
	}
}

func looksLikeOpenClaw(rawContent []byte) bool {
	content := normalizeMarkdown(rawContent)
	return strings.Contains(content, "\n## Parameters") &&
		strings.Contains(content, "\n## Permissions") &&
		strings.Contains(content, "\n## Instructions")
}
