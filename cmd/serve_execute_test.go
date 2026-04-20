package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timoxue/loom-cli/internal/security"
)

// testMCPServer builds an mcpServer rooted at a temp skills dir. It
// copies the real test_skills fixtures in so the integration tests
// exercise the same files our fixture matrix uses — not hand-forged
// stubs that can drift from production behavior.
func testMCPServer(t *testing.T) *mcpServer {
	t.Helper()

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	skillsDir := t.TempDir()
	copyFixture(t, skillsDir, "templated_write.loom.json")
	copyFixture(t, skillsDir, "reject_path_escape.loom.json")
	copyFixture(t, skillsDir, "demo_cleaner.md")

	return &mcpServer{
		skillsDir:     skillsDir,
		workspaceRoot: t.TempDir(),
		policy:        security.DefaultPolicy(),
	}
}

func copyFixture(t *testing.T, destDir, filename string) {
	t.Helper()
	src := filepath.Join("..", "test_skills", filename)
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture %q: %v", src, err)
	}
	if err := os.WriteFile(filepath.Join(destDir, filename), raw, 0o644); err != nil {
		t.Fatalf("write fixture %q: %v", filename, err)
	}
}

func postMCP(t *testing.T, server *mcpServer, body string) map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/mcp", bytes.NewReader([]byte(body)))
	recorder := httptest.NewRecorder()
	server.handleMCPRequest(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200 (body: %s)", recorder.Code, recorder.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, recorder.Body.String())
	}
	return envelope
}

// TestToolsListReturnsDiscoveredSkills confirms the list discovers the
// real skills, assigns JSON-Schema shapes, and exposes descriptions so
// Claude has something to base tool selection on.
func TestToolsListReturnsDiscoveredSkills(t *testing.T) {
	server := testMCPServer(t)

	envelope := postMCP(t, server, `{"jsonrpc":"2.0","method":"tools/list","id":"1","params":{}}`)
	result, _ := envelope["result"].(map[string]any)
	if result == nil {
		t.Fatalf("envelope.result nil; full envelope=%v", envelope)
	}
	tools, _ := result["tools"].([]any)
	if len(tools) < 3 {
		t.Fatalf("len(tools) = %d, want >= 3 (templated_write + reject_path_escape + demo_cleaner)", len(tools))
	}

	var found map[string]any
	for _, t := range tools {
		entry, _ := t.(map[string]any)
		if entry["name"] == "templated_write" {
			found = entry
			break
		}
	}
	if found == nil {
		t.Fatal("templated_write not in tools/list output")
	}
	desc, _ := found["description"].(string)
	if desc == "" {
		t.Fatal("templated_write description is empty; Claude needs something to base selection on")
	}
	schema, _ := found["inputSchema"].(map[string]any)
	if schema["type"] != "object" {
		t.Fatalf("inputSchema.type = %#v, want object", schema["type"])
	}
	required, _ := schema["required"].([]any)
	if len(required) != 1 || required[0] != "msg" {
		t.Fatalf("required = %#v, want [msg]", required)
	}
}

// TestToolsListTolerantOfBrokenFile drops a malformed file into the
// skills dir and asserts the MCP response still succeeds (silent-skip)
// while serve's stderr loudly names the bad file.
func TestToolsListTolerantOfBrokenFile(t *testing.T) {
	server := testMCPServer(t)

	brokenPath := filepath.Join(server.skillsDir, "broken.loom.json")
	if err := os.WriteFile(brokenPath, []byte("{ this is not valid json"), 0o644); err != nil {
		t.Fatalf("write broken fixture: %v", err)
	}

	// Redirect stderr to capture the "tolerant outside, noisy inside" log.
	originalStderr := os.Stderr
	readPipe, writePipe, _ := os.Pipe()
	os.Stderr = writePipe

	envelope := postMCP(t, server, `{"jsonrpc":"2.0","method":"tools/list","id":"1","params":{}}`)

	writePipe.Close()
	os.Stderr = originalStderr
	captured, _ := readStderrBuffer(readPipe)

	if errorField, _ := envelope["error"].(map[string]any); errorField != nil {
		t.Fatalf("envelope.error = %v, want nil (broken files must not break the protocol)", errorField)
	}
	result, _ := envelope["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	for _, tool := range tools {
		entry, _ := tool.(map[string]any)
		if entry["name"] == "broken" {
			t.Fatal("broken file appeared in tools/list output")
		}
	}
	if !strings.Contains(captured, "broken.loom.json") {
		t.Fatalf("stderr missing filename diagnostic, got: %q", captured)
	}
}

func readStderrBuffer(readPipe *os.File) (string, error) {
	var buffer bytes.Buffer
	_, err := buffer.ReadFrom(readPipe)
	return buffer.String(), err
}

// TestToolsCallTemplatedWriteEndToEnd runs a real v1 skill via MCP with
// substituted input, and verifies the shadow was populated, the
// workspace was not, and the response carries the session-id the
// operator needs to commit.
func TestToolsCallTemplatedWriteEndToEnd(t *testing.T) {
	server := testMCPServer(t)

	body := `{"jsonrpc":"2.0","method":"tools/call","id":"1","params":{"name":"templated_write","arguments":{"msg":"world"}}}`
	envelope := postMCP(t, server, body)

	if envelope["error"] != nil {
		t.Fatalf("envelope.error = %v, want nil", envelope["error"])
	}
	result, _ := envelope["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); isError {
		content, _ := result["content"].([]any)
		t.Fatalf("isError = true, content = %v", content)
	}

	loom, _ := result["_loom"].(map[string]any)
	if loom == nil {
		t.Fatal("_loom metadata missing from successful response")
	}
	sessionID, _ := loom["session_id"].(string)
	if sessionID == "" {
		t.Fatal("_loom.session_id empty — operator cannot commit without it")
	}
	shadowPath, _ := loom["shadow_path"].(string)
	if shadowPath == "" {
		t.Fatal("_loom.shadow_path empty")
	}

	shadowFile := filepath.Join(shadowPath, "out", "greeting.txt")
	got, err := os.ReadFile(shadowFile)
	if err != nil {
		t.Fatalf("read shadow file: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("shadow content = %q, want %q (substitution must expand msg)", string(got), "hello world")
	}

	// Real workspace stays untouched — commit is out-of-band.
	if _, err := os.Stat(filepath.Join(server.workspaceRoot, "out", "greeting.txt")); !os.IsNotExist(err) {
		t.Fatalf("workspace was mutated, stat err = %v (commit must be human-driven)", err)
	}

	// Summary text must mention the commit command so the agent can
	// relay it to the user.
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatal("content empty on successful call")
	}
	firstBlock, _ := content[0].(map[string]any)
	text, _ := firstBlock["text"].(string)
	expected := fmt.Sprintf("loom commit %s --yes", sessionID)
	if !strings.Contains(text, expected) {
		t.Fatalf("summary missing commit hint %q, got:\n%s", expected, text)
	}
}

// TestToolsCallRejectsPathEscape confirms that a capability-ceiling
// violation surfaces as a tool-result with isError: true, NOT as a
// JSON-RPC protocol error. The agent must see one consistent failure
// branch regardless of which loom layer caught the issue.
func TestToolsCallRejectsPathEscape(t *testing.T) {
	server := testMCPServer(t)

	body := `{"jsonrpc":"2.0","method":"tools/call","id":"1","params":{"name":"reject_path_escape","arguments":{}}}`
	envelope := postMCP(t, server, body)

	if envelope["error"] != nil {
		t.Fatalf("envelope.error = %v, want nil (errors surface in result)", envelope["error"])
	}
	result, _ := envelope["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("isError = false, want true for capability-ceiling violation")
	}
	content, _ := result["content"].([]any)
	firstBlock, _ := content[0].(map[string]any)
	text, _ := firstBlock["text"].(string)
	if !strings.Contains(text, "capability") {
		t.Fatalf("error text = %q, want message containing \"capability\"", text)
	}
	// No session should have been created.
	if loom, _ := result["_loom"].(map[string]any); loom != nil {
		if sid, _ := loom["session_id"].(string); sid != "" {
			t.Fatalf("_loom.session_id = %q, want empty for rejected-at-admission case", sid)
		}
	}
}

// TestToolsCallNotFound surfaces an unknown skill name as an isError
// tool result (not a JSON-RPC error).
func TestToolsCallNotFound(t *testing.T) {
	server := testMCPServer(t)

	body := `{"jsonrpc":"2.0","method":"tools/call","id":"1","params":{"name":"no_such_skill","arguments":{}}}`
	envelope := postMCP(t, server, body)

	if envelope["error"] != nil {
		t.Fatalf("envelope.error = %v, want nil", envelope["error"])
	}
	result, _ := envelope["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatal("isError = false, want true for unknown skill")
	}
}
