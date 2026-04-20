package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/timoxue/loom-cli/internal/engine"
	"github.com/timoxue/loom-cli/internal/engine/parser"
	"github.com/timoxue/loom-cli/internal/security"
)

const deterministicGatewayBanner = `
 _                                        
| |    ___   ___  _ __ ___                
| |   / _ \ / _ \| '_ ' _ \               
| |__| (_) | (_) | | | | | |              
|_____\___/ \___/|_| |_| |_|              

Deterministic AI Gateway
`

// MCPRequest is the JSON-RPC 2.0 request envelope accepted by the MCP sidecar.
type MCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	ID      any             `json:"id"`
	Params  MCPRequestParams `json:"params"`
}

// MCPRequestParams holds tool addressing and strongly stringified arguments.
type MCPRequestParams struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments"`
}

// MCPResponse is the JSON-RPC 2.0 response envelope emitted by the MCP sidecar.
type MCPResponse struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id,omitempty"`
	Result  any            `json:"result,omitempty"`
	Error   *MCPErrorBody  `json:"error,omitempty"`
}

// MCPErrorBody is the standard JSON-RPC error payload.
type MCPErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpServer struct {
	skillsDir     string
	workspaceRoot string
	policy        *security.SecurityPolicy
}

func newServeCmd() *cobra.Command {
	var port int
	var skillsDir string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the deterministic MCP gateway sidecar",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(port, skillsDir)
		},
	}

	cmd.Flags().IntVar(&port, "port", 8080, "Port to listen on")
	cmd.Flags().StringVar(&skillsDir, "skills-dir", "test_skills", "Directory containing skill files")

	return cmd
}

func runServe(port int, skillsDir string) error {
	normalizedSkillsDir, err := normalizeServeRootPath(skillsDir)
	if err != nil {
		return &engine.ContractError{
			Field:  "skills_dir",
			Reason: err.Error(),
		}
	}

	serverState := &mcpServer{
		skillsDir:     normalizedSkillsDir,
		workspaceRoot: ".",
		policy:        security.DefaultPolicy(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/mcp", serverState.handleMCPRequest)

	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Fprint(os.Stdout, deterministicGatewayBanner)
	fmt.Fprintf(os.Stdout, "Listening on http://127.0.0.1:%d/v1/mcp\n", port)
	fmt.Fprintf(os.Stdout, "Skills directory: %s\n", normalizedSkillsDir)

	serverErrors := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
			return
		}
		serverErrors <- nil
	}()

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErrors:
		return err
	case <-signalContext.Done():
		fmt.Fprintln(os.Stdout, "Shutdown signal received. Draining gateway...")
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownContext); err != nil {
		return fmt.Errorf("shutdown MCP gateway: %w", err)
	}

	if err := <-serverErrors; err != nil {
		return err
	}

	fmt.Fprintln(os.Stdout, "Gateway stopped cleanly.")
	return nil
}

func (s *mcpServer) handleMCPRequest(responseWriter http.ResponseWriter, request *http.Request) {
	responseWriter.Header().Set("Content-Type", "application/json")

	if request.Method != http.MethodPost {
		writeMCPResponse(responseWriter, http.StatusMethodNotAllowed, MCPResponse{
			JSONRPC: "2.0",
			Error: &MCPErrorBody{
				Code:    -32601,
				Message: "method not found",
			},
		})
		return
	}

	var mcpRequest MCPRequest
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&mcpRequest); err != nil {
		writeMCPResponse(responseWriter, http.StatusBadRequest, MCPResponse{
			JSONRPC: "2.0",
			Error: &MCPErrorBody{
				Code:    -32600,
				Message: "invalid request",
			},
		})
		return
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		writeMCPResponse(responseWriter, http.StatusBadRequest, MCPResponse{
			JSONRPC: "2.0",
			Error: &MCPErrorBody{
				Code:    -32600,
				Message: "invalid request",
			},
		})
		return
	}

	if mcpRequest.JSONRPC != "2.0" {
		writeMCPResponse(responseWriter, http.StatusBadRequest, MCPResponse{
			JSONRPC: "2.0",
			ID:      mcpRequest.ID,
			Error: &MCPErrorBody{
				Code:    -32600,
				Message: "invalid request",
			},
		})
		return
	}

	switch mcpRequest.Method {
	case "ping":
		writeMCPResponse(responseWriter, http.StatusOK, MCPResponse{
			JSONRPC: "2.0",
			ID:      mcpRequest.ID,
			Result:  map[string]any{},
		})
	case "tools/list":
		s.handleToolsList(responseWriter, mcpRequest)
	case "tools/call":
		s.handleToolCall(responseWriter, mcpRequest)
	default:
		writeMCPResponse(responseWriter, http.StatusOK, MCPResponse{
			JSONRPC: "2.0",
			ID:      mcpRequest.ID,
			Error: &MCPErrorBody{
				Code:    -32601,
				Message: "method not found",
			},
		})
	}
}

// toolDefinition is the MCP tools/list entry. Shape follows the MCP spec:
// name + description + input_schema (JSON Schema draft-07 object shape).
type toolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema inputSchema `json:"inputSchema"`
}

type inputSchema struct {
	Type       string                        `json:"type"`
	Properties map[string]propertyDefinition `json:"properties"`
	Required   []string                      `json:"required,omitempty"`
}

type propertyDefinition struct {
	Type    string `json:"type"`
	Default string `json:"default,omitempty"`
}

// toolResultContent is one block of the MCP tool-result content array.
type toolResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// toolResult is the MCP shape returned by tools/call. _loom carries
// loom-specific audit metadata that programmatic clients can use; Claude
// and other MCP clients read only `content` and `isError`.
type toolResult struct {
	Content []toolResultContent `json:"content"`
	IsError bool                `json:"isError"`
	Loom    *loomResultMetadata `json:"_loom,omitempty"`
}

type loomResultMetadata struct {
	SessionID   string          `json:"session_id,omitempty"`
	ShadowPath  string          `json:"shadow_path,omitempty"`
	LogicalHash string          `json:"logical_hash,omitempty"`
	InputDigest string          `json:"input_digest,omitempty"`
	Manifest    []engine.Change `json:"manifest,omitempty"`
}

// handleToolsList walks the skills directory, parses each file, and
// returns the successful parses as MCP tools. Parse failures are
// "tolerant outside, noisy inside": the MCP response silently omits
// the broken file, but a line is written to stderr with the filename
// and the concrete reason so developers don't debug in the dark.
func (s *mcpServer) handleToolsList(responseWriter http.ResponseWriter, mcpRequest MCPRequest) {
	tools := make([]toolDefinition, 0, 8)

	entries, err := os.ReadDir(s.skillsDir)
	if err != nil {
		writeMCPResponse(responseWriter, http.StatusOK, MCPResponse{
			JSONRPC: "2.0",
			ID:      mcpRequest.ID,
			Error: &MCPErrorBody{
				Code:    -32000,
				Message: fmt.Sprintf("read skills dir: %v", err),
			},
		})
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		lowerName := strings.ToLower(entry.Name())
		if !strings.HasSuffix(lowerName, ".loom.json") && !strings.HasSuffix(lowerName, ".md") {
			continue
		}

		fullPath := filepath.Join(s.skillsDir, entry.Name())
		raw, err := os.ReadFile(fullPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "loom serve: skipping %q: %v\n", entry.Name(), err)
			continue
		}
		skill, err := parser.ParseFile(entry.Name(), raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "loom serve: skipping %q: %v\n", entry.Name(), err)
			continue
		}
		tools = append(tools, skillToToolDefinition(skill))
	}

	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	writeMCPResponse(responseWriter, http.StatusOK, MCPResponse{
		JSONRPC: "2.0",
		ID:      mcpRequest.ID,
		Result:  map[string]any{"tools": tools},
	})
}

// skillToToolDefinition converts a parsed LoomSkill into the MCP tool
// definition shape. Description falls back to "Loom skill <name>" when
// empty so the agent always sees a non-empty hint for tool selection.
func skillToToolDefinition(skill *engine.LoomSkill) toolDefinition {
	description := skill.Description
	if strings.TrimSpace(description) == "" {
		description = fmt.Sprintf("Loom skill %s", skill.SkillID)
	}

	properties := make(map[string]propertyDefinition, len(skill.Parameters))
	required := make([]string, 0, len(skill.Parameters))

	names := make([]string, 0, len(skill.Parameters))
	for name := range skill.Parameters {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		param := skill.Parameters[name]
		properties[name] = propertyDefinition{
			Type:    parameterTypeToJSONSchemaType(param.Type),
			Default: param.DefaultValue,
		}
		if param.Required {
			required = append(required, name)
		}
	}

	return toolDefinition{
		Name:        skill.SkillID,
		Description: description,
		InputSchema: inputSchema{
			Type:       "object",
			Properties: properties,
			Required:   required,
		},
	}
}

func parameterTypeToJSONSchemaType(pt engine.ParameterType) string {
	switch pt {
	case engine.ParameterTypeString:
		return "string"
	case engine.ParameterTypeInt:
		return "integer"
	case engine.ParameterTypeBool:
		return "boolean"
	case engine.ParameterTypeFloat:
		return "number"
	default:
		return "string"
	}
}

// handleToolCall runs a skill end-to-end into the shadow workspace and
// returns an MCP tool-result describing what happened. Recoverable
// failures (skill not found, validator rejection, executor error) come
// back as `isError: true` tool results, not JSON-RPC errors — JSON-RPC
// errors stay reserved for protocol-level issues (bad JSON, wrong
// method). This gives the agent a single failure-handling branch.
//
// Commit stays out-of-band: this handler never calls Promote. The
// returned _loom.session_id is what the operator runs `loom commit`
// against, on the host.
func (s *mcpServer) handleToolCall(responseWriter http.ResponseWriter, mcpRequest MCPRequest) {
	skillName := strings.TrimSpace(mcpRequest.Params.Name)
	if skillName == "" {
		writeToolResultError(responseWriter, mcpRequest.ID, "tool name must not be empty")
		return
	}
	if err := validateSkillLookupName(skillName); err != nil {
		writeToolResultError(responseWriter, mcpRequest.ID, err.Error())
		return
	}

	skillPath, rawContent, err := s.locateSkillFile(skillName)
	if err != nil {
		writeToolResultError(responseWriter, mcpRequest.ID, err.Error())
		return
	}

	skill, err := parser.ParseFile(skillPath, rawContent)
	if err != nil {
		writeToolResultError(responseWriter, mcpRequest.ID, fmt.Sprintf("parse %s: %v", filepath.Base(skillPath), err))
		return
	}

	compiler := &engine.Compiler{
		Policy:        s.policy,
		WorkspaceRoot: s.workspaceRoot,
	}

	sessionID, err := newSessionID()
	if err != nil {
		writeToolResultError(responseWriter, mcpRequest.ID, err.Error())
		return
	}

	shadowVFS, sanitizedInputs, err := compiler.CompileAndSetup(skill, mcpRequest.Params.Arguments, sessionID)
	if err != nil {
		writeToolResultError(responseWriter, mcpRequest.ID, fmt.Sprintf("admission failed: %v", err))
		return
	}

	executor := &engine.Executor{VFS: shadowVFS}
	manifest, execErr := executor.Execute(context.Background(), skill, sanitizedInputs)

	meta := &loomResultMetadata{
		SessionID:   sessionID,
		ShadowPath:  shadowVFS.ShadowDir,
		LogicalHash: skill.GetLogicalHash(),
		Manifest:    manifest,
	}

	if execErr != nil {
		// Execution failed after admission — session still exists for post-mortem,
		// but the agent should see isError. Partial manifest is carried so the
		// agent can tell how far the run got.
		writeToolResult(responseWriter, mcpRequest.ID, toolResult{
			Content: []toolResultContent{{
				Type: "text",
				Text: fmt.Sprintf("execution failed: %v", execErr),
			}},
			IsError: true,
			Loom:    meta,
		})
		return
	}

	summary := formatExecutionSummary(skill, sessionID, manifest)
	writeToolResult(responseWriter, mcpRequest.ID, toolResult{
		Content: []toolResultContent{{Type: "text", Text: summary}},
		IsError: false,
		Loom:    meta,
	})
}

// locateSkillFile resolves a tool name to a concrete skill file on disk.
// v1 JSON is preferred (the only shape the executor can actually run);
// .md is the v0 fallback so `verify` behavior is preserved for legacy
// skills, even though they'll ultimately be rejected by the executor.
func (s *mcpServer) locateSkillFile(skillName string) (string, []byte, error) {
	for _, suffix := range []string{".loom.json", ".md"} {
		candidate := filepath.Join(s.skillsDir, filepath.FromSlash(skillName+suffix))

		within, err := isWithinBasePath(s.skillsDir, candidate)
		if err != nil {
			return "", nil, err
		}
		if !within {
			return "", nil, fmt.Errorf("tool path escapes skills directory")
		}

		raw, err := os.ReadFile(candidate)
		if err == nil {
			return candidate, raw, nil
		}
		if !os.IsNotExist(err) {
			return "", nil, fmt.Errorf("read skill file: %w", err)
		}
	}
	return "", nil, fmt.Errorf("skill %q not found", skillName)
}

// formatExecutionSummary renders the human-readable text block that
// Claude sees in the tool_result content. It intentionally names the
// session-id so the agent can suggest the right commit command to the
// user in its next turn.
func formatExecutionSummary(skill *engine.LoomSkill, sessionID string, manifest []engine.Change) string {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Executed skill %q in a sandboxed shadow workspace.\n", skill.SkillID))
	builder.WriteString(fmt.Sprintf("Session: %s\n", sessionID))
	if len(manifest) == 0 {
		builder.WriteString("No filesystem changes produced.\n")
	} else {
		builder.WriteString("Pending changes:\n")
		for _, change := range manifest {
			builder.WriteString(fmt.Sprintf("  %s  %s\n", change.Op, change.Path))
		}
	}
	builder.WriteString(fmt.Sprintf("\nReal workspace is unchanged. To promote, the user must run:\n  loom commit %s --yes", sessionID))
	return builder.String()
}

func writeToolResult(responseWriter http.ResponseWriter, id any, result toolResult) {
	writeMCPResponse(responseWriter, http.StatusOK, MCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func writeToolResultError(responseWriter http.ResponseWriter, id any, message string) {
	writeToolResult(responseWriter, id, toolResult{
		Content: []toolResultContent{{Type: "text", Text: message}},
		IsError: true,
	})
}

func writeMCPResponse(responseWriter http.ResponseWriter, statusCode int, response MCPResponse) {
	responseWriter.WriteHeader(statusCode)
	encoder := json.NewEncoder(responseWriter)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(response)
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}

	return fmt.Errorf("unexpected trailing JSON content")
}

func validateSkillLookupName(skillName string) error {
	switch {
	case skillName == "." || skillName == "..":
		return fmt.Errorf("tool name must not be a relative path segment")
	case filepath.IsAbs(skillName):
		return fmt.Errorf("tool name must not be an absolute path")
	case filepath.VolumeName(skillName) != "":
		return fmt.Errorf("tool name must not contain a volume prefix")
	case strings.Contains(skillName, "/"), strings.Contains(skillName, `\`):
		return fmt.Errorf("tool name must not contain path separators")
	default:
		return nil
	}
}

func normalizeServeRootPath(rawPath string) (string, error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return "", fmt.Errorf("path must not be empty")
	}

	absolutePath, err := filepath.Abs(filepath.Clean(filepath.FromSlash(trimmed)))
	if err != nil {
		return "", fmt.Errorf("normalize path %q: %w", rawPath, err)
	}

	return absolutePath, nil
}

func isWithinBasePath(basePath, candidatePath string) (bool, error) {
	baseAbs, err := filepath.Abs(filepath.Clean(basePath))
	if err != nil {
		return false, fmt.Errorf("normalize base path %q: %w", basePath, err)
	}

	candidateAbs, err := filepath.Abs(filepath.Clean(candidatePath))
	if err != nil {
		return false, fmt.Errorf("normalize candidate path %q: %w", candidatePath, err)
	}

	relativePath, err := filepath.Rel(baseAbs, candidateAbs)
	if err != nil {
		return false, fmt.Errorf("derive relative path from %q to %q: %w", baseAbs, candidateAbs, err)
	}
	if relativePath == "." {
		return true, nil
	}

	return relativePath != ".." &&
		!strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relativePath), nil
}
