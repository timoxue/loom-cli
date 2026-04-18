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
	if skill.SchemaVersion != "" {
		t.Fatalf("SchemaVersion = %q, want empty (v0 legacy)", skill.SchemaVersion)
	}
	if skill.SkillID != "demo_cleaner" {
		t.Fatalf("SkillID = %q, want demo_cleaner", skill.SkillID)
	}
	if len(skill.Parameters) != 2 {
		t.Fatalf("len(Parameters) = %d, want 2", len(skill.Parameters))
	}
	if len(skill.ExecutionDAG) != 2 {
		t.Fatalf("len(ExecutionDAG) = %d, want 2", len(skill.ExecutionDAG))
	}
	if skill.ExecutionDAG[0].Kind != engine.StepKindLegacy {
		t.Fatalf("step 1 kind = %q, want legacy", skill.ExecutionDAG[0].Kind)
	}
	legacyArgs, ok := skill.ExecutionDAG[0].Args.(engine.LegacyStepArgs)
	if !ok {
		t.Fatalf("step 1 args = %T, want LegacyStepArgs", skill.ExecutionDAG[0].Args)
	}
	if legacyArgs.Action == "" {
		t.Fatal("step 1 legacy action is empty")
	}
	if skill.ExecutionDAG[0].Inputs["target_path"] != "${target_path}" {
		t.Fatalf("step 1 target_path input = %q, want ${target_path}", skill.ExecutionDAG[0].Inputs["target_path"])
	}

	hasRead := false
	hasWrite := false
	for _, capability := range skill.Capabilities {
		if capability.Kind == engine.CapKindVFSRead && capability.Scope == "/tmp/workspace" {
			hasRead = true
		}
		if capability.Kind == engine.CapKindVFSWrite && capability.Scope == "/tmp/workspace" {
			hasWrite = true
		}
	}
	if !hasRead || !hasWrite {
		t.Fatalf("capabilities = %#v, want fs.read+fs.write on /tmp/workspace", skill.Capabilities)
	}
}

func TestParseFileRejectsUnsupportedFormat(t *testing.T) {
	t.Parallel()

	if _, err := ParseFile("skill.yaml", []byte(`{}`)); err == nil {
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

func TestParseFileV1JSONRoundTrip(t *testing.T) {
	t.Parallel()

	rawContent := []byte(`{
  "schema_version": "v1",
  "skill_id": "demo_writefile",
  "parameters": {
    "msg": {"type": "string", "default_value": "", "required": true}
  },
  "execution_dag": [
    {
      "step_id": "s1",
      "kind": "write_file",
      "args": {"path": "out/hello.txt", "content": "hi"},
      "inputs": {},
      "outputs": []
    }
  ],
  "capabilities": [
    {"kind": "vfs.write", "scope": "out/"}
  ]
}`)

	skill, err := ParseFile("demo.loom.json", rawContent)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	if skill.SchemaVersion != engine.CurrentSchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", skill.SchemaVersion, engine.CurrentSchemaVersion)
	}
	if len(skill.ExecutionDAG) != 1 {
		t.Fatalf("len(ExecutionDAG) = %d, want 1", len(skill.ExecutionDAG))
	}
	writeArgs, ok := skill.ExecutionDAG[0].Args.(engine.WriteFileArgs)
	if !ok {
		t.Fatalf("step args = %T, want WriteFileArgs", skill.ExecutionDAG[0].Args)
	}
	if writeArgs.Path != "out/hello.txt" || writeArgs.Content != "hi" {
		t.Fatalf("writeArgs = %#v, want out/hello.txt + hi", writeArgs)
	}
}

func TestParseFileV1JSONRejectsWrongSchemaVersion(t *testing.T) {
	t.Parallel()

	rawContent := []byte(`{"schema_version": "v2", "skill_id": "x"}`)
	if _, err := ParseFile("x.loom.json", rawContent); err == nil {
		t.Fatal("ParseFile() error = nil, want schema version rejection")
	}
}

func TestParseFileV1JSONRejectsUnknownStepKind(t *testing.T) {
	t.Parallel()

	rawContent := []byte(`{
  "schema_version": "v1",
  "skill_id": "x",
  "execution_dag": [
    {"step_id": "s1", "kind": "os_command", "args": {"cmd": "rm -rf /"}}
  ]
}`)
	if _, err := ParseFile("x.loom.json", rawContent); err == nil {
		t.Fatal("ParseFile() error = nil, want unknown-kind rejection")
	}
}
