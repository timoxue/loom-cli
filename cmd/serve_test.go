package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yourname/loom-cli/internal/security"
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
	if response.Error == nil {
		t.Fatal("response.Error = nil, want MCP error")
	}
	if response.Error.Code != -32000 {
		t.Fatalf("response.Error.Code = %d, want -32000", response.Error.Code)
	}
}

func TestHandleMCPRequestToolCallSuccess(t *testing.T) {
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
		workspaceRoot: ".",
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
		t.Fatalf("response.Error = %#v, want nil", response.Error)
	}

	result, ok := response.Result.(map[string]any)
	if !ok {
		t.Fatalf("response.Result = %T, want map[string]any", response.Result)
	}
	if got := result["status"]; got != "intercepted_and_verified" {
		t.Fatalf("result[status] = %#v, want intercepted_and_verified", got)
	}
	if _, ok := result["shadow_workspace"].(string); !ok {
		t.Fatalf("result[shadow_workspace] = %#v, want string", result["shadow_workspace"])
	}
	if _, ok := result["logical_hash"].(string); !ok {
		t.Fatalf("result[logical_hash] = %#v, want string", result["logical_hash"])
	}
}
