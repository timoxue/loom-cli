package migrator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/timoxue/loom-cli/internal/engine"
)

// ClaudeClient is the default LLMClient implementation. It POSTs to
// Anthropic's messages endpoint, treats the tool's output as JSON, and
// shape-validates against v1 LoomSkill. A single retry is attempted on
// malformed output; anything else falls through to stub.
//
// The client is deliberately single-purpose. If we ever need streaming,
// multi-turn, or tool-use, those go in a separate abstraction.
type ClaudeClient struct {
	APIKey   string
	Model    string        // e.g. "claude-sonnet-4-6"
	Endpoint string        // defaults to https://api.anthropic.com/v1/messages
	Timeout  time.Duration // defaults to 30s
	client   *http.Client
}

// NewClaudeClient builds a client from environment. Returns nil if
// ANTHROPIC_API_KEY is unset — callers treat a nil client as "LLM not
// available" and emit stubs, which is the graceful-degradation path.
func NewClaudeClient(apiKey string) *ClaudeClient {
	if apiKey == "" {
		return nil
	}
	return &ClaudeClient{
		APIKey:   apiKey,
		Model:    "claude-sonnet-4-6",
		Endpoint: "https://api.anthropic.com/v1/messages",
		Timeout:  30 * time.Second,
	}
}

func (c *ClaudeClient) Name() string {
	if c == nil {
		return ""
	}
	return c.Model
}

func (c *ClaudeClient) Translate(ctx TranslateContext) (*engine.LoomSkill, error) {
	if c == nil {
		return nil, fmt.Errorf("nil ClaudeClient")
	}

	requestBody, err := json.Marshal(map[string]any{
		"model":      c.Model,
		"max_tokens": 2048,
		"system":     migrationSystemPrompt(ctx.AllowedKinds),
		"messages": []map[string]any{{
			"role":    "user",
			"content": migrationUserMessage(ctx),
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		skill, lastErr := c.doTranslate(requestBody)
		if lastErr == nil {
			return skill, nil
		}
		// Retry only on malformed-output errors; surface other errors immediately.
		if !strings.Contains(lastErr.Error(), "malformed") {
			return nil, lastErr
		}
		if attempt == 1 {
			return nil, lastErr
		}
	}
	return nil, fmt.Errorf("unreachable")
}

func (c *ClaudeClient) doTranslate(requestBody []byte) (*engine.LoomSkill, error) {
	httpClient := c.client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: c.Timeout}
	}
	req, err := http.NewRequest(http.MethodPost, c.Endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic API status %d: %s", resp.StatusCode, truncateForError(string(raw), 256))
	}

	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}

	text := ""
	for _, block := range envelope.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("malformed response: no text content")
	}

	skill, err := parseSkillFromLLMText(text)
	if err != nil {
		return nil, fmt.Errorf("malformed response: %w", err)
	}
	return skill, nil
}

// parseSkillFromLLMText pulls the first JSON object out of the LLM's
// response and decodes it as a LoomSkill. The LLM is prompted to return
// pure JSON, but sometimes wraps it in ```json fences — we handle both.
func parseSkillFromLLMText(text string) (*engine.LoomSkill, error) {
	// Strip common fence wrappers.
	if strings.HasPrefix(text, "```") {
		if idx := strings.Index(text[3:], "\n"); idx >= 0 {
			text = text[3+idx+1:]
		}
		if idx := strings.LastIndex(text, "```"); idx >= 0 {
			text = text[:idx]
		}
	}
	text = strings.TrimSpace(text)

	var skill engine.LoomSkill
	if err := json.Unmarshal([]byte(text), &skill); err != nil {
		return nil, fmt.Errorf("decode skill json: %w", err)
	}
	return &skill, nil
}

func truncateForError(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// migrationSystemPrompt is the system prompt Claude sees. AllowedKinds
// is injected so the prompt always matches what loom's executor can
// actually run — expanding the kinds list later won't require a prompt
// rewrite, just a new version bump.
func migrationSystemPrompt(allowedKinds []engine.StepKind) string {
	kindsList := make([]string, 0, len(allowedKinds))
	for _, k := range allowedKinds {
		kindsList = append(kindsList, string(k))
	}

	return fmt.Sprintf(`You translate OpenClaw markdown skill definitions into Loom v1 JSON skill bodies.

Output rules (STRICT):
- Return ONLY a single JSON object. No prose, no markdown fences, no explanation.
- The JSON MUST have fields: schema_version="v1", skill_id, parameters, execution_dag, capabilities.
- parameters is a map of name -> {"type": string, "default_value": string, "required": bool}.
- execution_dag is an array of steps. Each step has: step_id, kind, args, inputs (map), outputs (array).
- kind MUST be one of: %s
- args structure depends on kind:
    read_file  -> {"path": string}
    write_file -> {"path": string, "content": string}
- capabilities is an array of {"kind": "vfs.read"|"vfs.write", "scope": string}. Scope must cover every path used.
- inputs map carries %%{var} references from args back to declared parameters — always include every %%{var} in args.

If the OpenClaw skill requires capabilities NOT in the allowed list (e.g. shell execution, HTTP, delete), return:
  {"schema_version":"v1","skill_id":"<same>","parameters":{},"execution_dag":[],"capabilities":[]}
with no steps. This becomes a Tier 3 stub that the operator will rewrite by hand.

Err on the side of emitting the empty-dag stub when in doubt. Never invent capabilities that aren't needed.`, strings.Join(kindsList, ", "))
}

func migrationUserMessage(ctx TranslateContext) string {
	var out strings.Builder
	out.WriteString("Translate this OpenClaw skill to Loom v1 JSON.\n\n")
	out.WriteString(fmt.Sprintf("skill_id: %s\n", ctx.SkillID))
	if ctx.Description != "" {
		out.WriteString(fmt.Sprintf("description: %s\n", ctx.Description))
	}
	if len(ctx.Parameters) > 0 {
		out.WriteString("\nDeclared parameters:\n")
		for name, param := range ctx.Parameters {
			out.WriteString(fmt.Sprintf("  %s: type=%s required=%v default=%q\n",
				name, param.Type, param.Required, param.DefaultValue))
		}
	}
	if len(ctx.LegacyActions) > 0 {
		out.WriteString("\nOriginal instruction text (natural language):\n")
		for i, action := range ctx.LegacyActions {
			out.WriteString(fmt.Sprintf("  %d. %s\n", i+1, action))
		}
	}
	out.WriteString("\nEmit the v1 JSON skill body now. JSON only.")
	return out.String()
}

// responseFenceRE is exposed for tests that want to simulate fenced
// responses without duplicating the strip logic.
var responseFenceRE = regexp.MustCompile("(?s)^```(?:json)?\\s*(.+?)\\s*```$")
