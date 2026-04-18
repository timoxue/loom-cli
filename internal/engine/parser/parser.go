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
func ParseFile(filename string, rawContent []byte) (*engine.LoomSkill, error) {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	switch extension {
	case ".md":
		return (&OpenClawParser{}).Parse(rawContent)
	case "":
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
