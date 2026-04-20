package migrator

import (
	"github.com/timoxue/loom-cli/internal/engine"
)

// stubSkillFor produces a valid-but-unrunnable v1 skill for the Tier 3
// case. The body has no steps and no capabilities; the executor's
// stub-refusal path keeps it from ever running. We preserve the
// original skill's SkillID, Description, and Parameters so the
// reviewer can see what the v0 intent was without hunting for the
// source markdown.
//
// The stub reason itself lives in Provenance.StubReason, set by the
// caller (processSkill) because different failure paths produce
// different reasons (NoLLM, LLM translation failed, no mechanical
// match, etc.).
func stubSkillFor(legacy *engine.LoomSkill) *engine.LoomSkill {
	params := clonedParams(legacy.Parameters)
	return &engine.LoomSkill{
		SchemaVersion: engine.CurrentSchemaVersion,
		SkillID:       legacy.SkillID,
		Description:   legacy.Description,
		Parameters:    params,
		ExecutionDAG:  []engine.Step{}, // deliberately empty
		Capabilities:  []engine.Capability{},
	}
}
