package parser

import (
	"errors"
	"testing"

	"github.com/yourname/loom-cli/internal/engine"
)

func TestParseFileOpenClawMarkdown(t *testing.T) {
	t.Parallel()

	rawContent := []byte(`---
name: demo_cleaner
description: test skill
---

## Parameters
- ` + "`target_path`" + ` (string): cleanup target. Required.
- ` + "`dry_run`" + ` (bool): dry run flag. Default: true

## Permissions
- ` + "`fs.read`" + `: /tmp/workspace
- ` + "`fs.write`" + `: /tmp/workspace

## Instructions
1. Validate ${target_path}.
2. Delete matching files when ${dry_run} is false.
`)

	skill, err := ParseFile("demo_cleaner.md", rawContent)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	if skill.SkillID != "demo_cleaner" {
		t.Fatalf("SkillID = %q, want demo_cleaner", skill.SkillID)
	}
	if len(skill.Parameters) != 2 {
		t.Fatalf("len(Parameters) = %d, want 2", len(skill.Parameters))
	}
	if skill.Parameters["target_path"].Type != engine.ParameterTypeString {
		t.Fatalf("target_path type = %q, want string", skill.Parameters["target_path"].Type)
	}
	if !skill.Parameters["target_path"].Required {
		t.Fatal("target_path.Required = false, want true")
	}
	if skill.Parameters["dry_run"].Type != engine.ParameterTypeBool {
		t.Fatalf("dry_run type = %q, want bool", skill.Parameters["dry_run"].Type)
	}
	if skill.Parameters["dry_run"].DefaultValue != "true" {
		t.Fatalf("dry_run.DefaultValue = %q, want true", skill.Parameters["dry_run"].DefaultValue)
	}
	if len(skill.ExecutionDAG) != 2 {
		t.Fatalf("len(ExecutionDAG) = %d, want 2", len(skill.ExecutionDAG))
	}
	if skill.ExecutionDAG[0].StepID != "step_1" {
		t.Fatalf("step 1 id = %q, want step_1", skill.ExecutionDAG[0].StepID)
	}
	if skill.ExecutionDAG[0].Inputs["target_path"] != "${target_path}" {
		t.Fatalf("step 1 target_path input = %q, want ${target_path}", skill.ExecutionDAG[0].Inputs["target_path"])
	}
	if skill.ExecutionDAG[1].Inputs["dry_run"] != "${dry_run}" {
		t.Fatalf("step 2 dry_run input = %q, want ${dry_run}", skill.ExecutionDAG[1].Inputs["dry_run"])
	}
	if got := skill.Permissions["fs.read"]; len(got) != 1 || got[0] != "/tmp/workspace" {
		t.Fatalf("fs.read = %#v, want [/tmp/workspace]", got)
	}
}

func TestParseFileRejectsUnsupportedFormat(t *testing.T) {
	t.Parallel()

	if _, err := ParseFile("skill.json", []byte(`{}`)); err == nil {
		t.Fatal("ParseFile() error = nil, want unsupported format error")
	}
}

func TestOpenClawParserRejectsMissingSection(t *testing.T) {
	t.Parallel()

	rawContent := []byte(`---
name: broken_skill
---

## Parameters
- ` + "`target_path`" + ` (string): cleanup target. Required.

## Instructions
1. Validate ${target_path}.
`)

	_, err := (&OpenClawParser{}).Parse(rawContent)
	if err == nil {
		t.Fatal("Parse() error = nil, want syntax error")
	}

	var syntaxErr *SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("error = %T, want *SyntaxError", err)
	}
}

func TestOpenClawParserRejectsUnknownParameterType(t *testing.T) {
	t.Parallel()

	rawContent := []byte(`---
name: broken_skill
---

## Parameters
- ` + "`target_path`" + ` (uuid): cleanup target. Required.

## Permissions
- ` + "`fs.read`" + `: /tmp/workspace

## Instructions
1. Validate ${target_path}.
`)

	_, err := (&OpenClawParser{}).Parse(rawContent)
	if err == nil {
		t.Fatal("Parse() error = nil, want syntax error")
	}

	var syntaxErr *SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("error = %T, want *SyntaxError", err)
	}
}
