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
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/yourname/loom-cli/internal/engine"
	"github.com/yourname/loom-cli/internal/engine/parser"
	"github.com/yourname/loom-cli/internal/security"
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

func (s *mcpServer) handleToolCall(responseWriter http.ResponseWriter, mcpRequest MCPRequest) {
	skillName := strings.TrimSpace(mcpRequest.Params.Name)
	if skillName == "" {
		writeMCPResponse(responseWriter, http.StatusOK, MCPResponse{
			JSONRPC: "2.0",
			ID:      mcpRequest.ID,
			Error: &MCPErrorBody{
				Code:    -32000,
				Message: "tool name must not be empty",
			},
		})
		return
	}
	if err := validateSkillLookupName(skillName); err != nil {
		writeMCPResponse(responseWriter, http.StatusOK, MCPResponse{
			JSONRPC: "2.0",
			ID:      mcpRequest.ID,
			Error: &MCPErrorBody{
				Code:    -32000,
				Message: err.Error(),
			},
		})
		return
	}

	skillFilename := skillName + ".md"
	skillPath := filepath.Join(s.skillsDir, filepath.FromSlash(skillFilename))

	if within, err := isWithinBasePath(s.skillsDir, skillPath); err != nil {
		writeMCPResponse(responseWriter, http.StatusOK, MCPResponse{
			JSONRPC: "2.0",
			ID:      mcpRequest.ID,
			Error: &MCPErrorBody{
				Code:    -32000,
				Message: err.Error(),
			},
		})
		return
	} else if !within {
		writeMCPResponse(responseWriter, http.StatusOK, MCPResponse{
			JSONRPC: "2.0",
			ID:      mcpRequest.ID,
			Error: &MCPErrorBody{
				Code:    -32000,
				Message: "tool path escapes skills directory",
			},
		})
		return
	}

	rawContent, err := os.ReadFile(skillPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeMCPResponse(responseWriter, http.StatusOK, MCPResponse{
				JSONRPC: "2.0",
				ID:      mcpRequest.ID,
				Error: &MCPErrorBody{
					Code:    -32000,
					Message: fmt.Sprintf("skill %q not found", skillName),
				},
			})
			return
		}

		writeMCPResponse(responseWriter, http.StatusOK, MCPResponse{
			JSONRPC: "2.0",
			ID:      mcpRequest.ID,
			Error: &MCPErrorBody{
				Code:    -32000,
				Message: fmt.Sprintf("read skill file: %v", err),
			},
		})
		return
	}

	skill, err := parser.ParseFile(skillPath, rawContent)
	if err != nil {
		writeMCPResponse(responseWriter, http.StatusOK, MCPResponse{
			JSONRPC: "2.0",
			ID:      mcpRequest.ID,
			Error: &MCPErrorBody{
				Code:    -32000,
				Message: err.Error(),
			},
		})
		return
	}

	compiler := &engine.Compiler{
		Policy:        s.policy,
		WorkspaceRoot: s.workspaceRoot,
	}

	sessionID, err := newSessionID()
	if err != nil {
		writeMCPResponse(responseWriter, http.StatusOK, MCPResponse{
			JSONRPC: "2.0",
			ID:      mcpRequest.ID,
			Error: &MCPErrorBody{
				Code:    -32000,
				Message: err.Error(),
			},
		})
		return
	}

	shadowVFS, _, err := compiler.CompileAndSetup(skill, mcpRequest.Params.Arguments, sessionID)
	if err != nil {
		writeMCPResponse(responseWriter, http.StatusOK, MCPResponse{
			JSONRPC: "2.0",
			ID:      mcpRequest.ID,
			Error: &MCPErrorBody{
				Code:    -32000,
				Message: err.Error(),
			},
		})
		return
	}

	writeMCPResponse(responseWriter, http.StatusOK, MCPResponse{
		JSONRPC: "2.0",
		ID:      mcpRequest.ID,
		Result: map[string]any{
			"status":           "intercepted_and_verified",
			"shadow_workspace": shadowVFS.ShadowDir,
			"logical_hash":     skill.GetLogicalHash(),
		},
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
