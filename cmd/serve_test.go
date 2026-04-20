package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timoxue/loom-cli/internal/security"
)

func TestHandleMCPRequestPing(t *testing.T) {
	t.Parallel()

	server := &mcpServer{
		skillsDir:     t.TempDir(),
		workspaceRoot: ".",
		policy:        security.DefaultPolicy(),
	}

	requestBody := []byte(`{"jsonrpc":"2.0","method":"ping","id":"1","params":{"name":"","arguments":{}}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/mcp", bytes.NewReader(requestBody))
	recorder := httptest.NewRecorder()

	server.handleMCPRequest(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", recorder.Code)
	}

	var response MCPResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if response.Error != nil {
		t.Fatalf("response.Error = %#v, want nil", response.Error)
	}
}

// TestHandleMCPRequestToolNotFound confirms that an unknown skill name
// surfaces as a tool-result with isError: true, not as a JSON-RPC
// protocol error. The agent must see one uniform failure branch
// regardless of whether loom couldn't find the skill, rejected it at
// admission, or failed during execution.
func TestHandleMCPRequestToolNotFound(t *testing.T) {
	t.Parallel()

	server := &mcpServer{
		skillsDir:     t.TempDir(),
		workspaceRoot: ".",
		policy:        security.DefaultPolicy(),
	}

	requestBody := []byte(`{"jsonrpc":"2.0","method":"tools/call","id":"1","params":{"name":"missing_skill","arguments":{}}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/mcp", bytes.NewReader(requestBody))
	recorder := httptest.NewRecorder()

	server.handleMCPRequest(recorder, request)

	var response MCPResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if response.Error != nil {
		t.Fatalf("response.Error = %#v, want nil (errors surface in result)", response.Error)
	}
	result, ok := response.Result.(map[string]any)
	if !ok {
		t.Fatalf("response.Result = %T, want map[string]any", response.Result)
	}
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("result.isError = %#v, want true for missing skill", result["isError"])
	}
}

// TestHandleMCPRequestToolCallRejectsV0 verifies that v0 markdown skills
// — which parse and admit fine but have no typed Kind — are rejected at
// the executor boundary with a tool-result-shaped isError response. Commit
// stays out-of-band: no shadow promotion is possible from this path.
func TestHandleMCPRequestToolCallRejectsV0(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	skillsDir := t.TempDir()
	skillPath := filepath.Join(skillsDir, "demo_cleaner.md")
	skillContent := `---
name: demo_cleaner
description: test skill
---

## Parameters
- ` + "`target_path`" + ` (string): cleanup target. Required.
- ` + "`dry_run`" + ` (bool): dry run flag. Default: true

## Permissions
- ` + "`fs.read`" + `: ./workspace
- ` + "`fs.write`" + `: ./workspace

## Instructions
1. Validate ${target_path}.
2. Return summary for ${dry_run}.
`
	if err := os.WriteFile(skillPath, []byte(skillContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	server := &mcpServer{
		skillsDir:     skillsDir,
		workspaceRoot: t.TempDir(),
		policy:        security.DefaultPolicy(),
	}

	requestBody := []byte(`{"jsonrpc":"2.0","method":"tools/call","id":"1","params":{"name":"demo_cleaner","arguments":{"target_path":"./tmp/data","dry_run":"true"}}}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/mcp", bytes.NewReader(requestBody))
	recorder := httptest.NewRecorder()

	server.handleMCPRequest(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", recorder.Code)
	}

	var response MCPResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if response.Error != nil {
		t.Fatalf("response.Error = %#v, want nil (errors surface in result, not as protocol error)", response.Error)
	}

	result, ok := response.Result.(map[string]any)
	if !ok {
		t.Fatalf("response.Result = %T, want map[string]any", response.Result)
	}

	isErrorValue, _ := result["isError"].(bool)
	if !isErrorValue {
		t.Fatalf("result.isError = %#v, want true for v0 skill", result["isError"])
	}

	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("result.content = %#v, want non-empty array", result["content"])
	}
	firstBlock, _ := content[0].(map[string]any)
	text, _ := firstBlock["text"].(string)
	if !strings.Contains(text, "not executable") {
		t.Fatalf("error text = %q, want message containing \"not executable\"", text)
	}
}
