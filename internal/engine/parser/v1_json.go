package parser

import (
	"encoding/json"
	"fmt"

	"github.com/timoxue/loom-cli/internal/engine"
)

// V1JSONParser reads a v1 skill document in JSON form. Step.UnmarshalJSON
// handles the Kind→Args dispatch via the IR's argsRegistry, so this parser
// itself only has to assert the schema version and delegate.
type V1JSONParser struct{}

// Parse turns v1 JSON bytes into a LoomSkill. It rejects any document whose
// schema_version is not the current one — version upgrades are a parser
// responsibility, but this spike does not yet have older v1.x dialects to
// upgrade from.
func (p *V1JSONParser) Parse(rawContent []byte) (*engine.LoomSkill, error) {
	var skill engine.LoomSkill
	if err := json.Unmarshal(rawContent, &skill); err != nil {
		return nil, &SyntaxError{
			Reason: fmt.Sprintf("decode v1 skill json: %v", err),
		}
	}

	if skill.SchemaVersion != engine.CurrentSchemaVersion {
		return nil, &SyntaxError{
			Reason: fmt.Sprintf("v1 parser requires schema_version=%q, got %q", engine.CurrentSchemaVersion, skill.SchemaVersion),
		}
	}

	return &skill, nil
}
