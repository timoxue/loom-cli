package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yourname/loom-cli/internal/security"
)

// Compiler orchestrates validation, input sanitization, and isolated execution setup.
type Compiler struct {
	Policy        *security.SecurityPolicy // Governing policy bundle applied before any execution setup is granted.
	WorkspaceRoot string                   // Real project root that becomes the immutable baseline for shadow execution.
}

// Receipt records the deterministic execution admission decision for a single session.
type Receipt struct {
	SessionID           string       `json:"session_id"`
	SkillID             string       `json:"skill_id"`
	SchemaVersion       string       `json:"schema_version"`
	LogicalHash         string       `json:"logical_hash"`
	Timestamp           time.Time    `json:"timestamp"`
	GrantedCapabilities []Capability `json:"granted_capabilities"`
	ShadowPath          string       `json:"shadow_path"`
}

// CompileAndSetup validates the skill, sanitizes inputs, provisions a shadow workspace, and writes an execution receipt.
func (c *Compiler) CompileAndSetup(skill *LoomSkill, rawInputs map[string]string, sessionID string) (*ShadowVFS, map[string]any, error) {
	if c == nil {
		return nil, nil, &ContractError{
			Field:  "compiler",
			Reason: "compiler is nil",
		}
	}
	if skill == nil {
		return nil, nil, &ContractError{
			Field:  "skill",
			Reason: "skill is nil",
		}
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, nil, &ContractError{
			Field:  "session_id",
			Reason: "session id must not be empty",
		}
	}
	if strings.TrimSpace(skill.SkillID) == "" {
		return nil, nil, &ContractError{
			Field:  "skill_id",
			Reason: "skill id must not be empty",
		}
	}
	if c.Policy == nil {
		return nil, nil, &ContractError{
			Field:  "policy",
			Reason: "security policy is nil",
		}
	}

	workspaceRoot, err := normalizeRootPath(c.WorkspaceRoot)
	if err != nil {
		return nil, nil, &ContractError{
			Field:  "workspace_root",
			Reason: err.Error(),
		}
	}

	if err := ValidateSkill(skill, c.Policy); err != nil {
		return nil, nil, err
	}

	sanitizedInputs, err := SanitizeInput(rawInputs, skill.Parameters)
	if err != nil {
		return nil, nil, err
	}

	shadowRoot, receiptPath, err := compilerSessionPaths(skill.SkillID, sessionID)
	if err != nil {
		return nil, nil, err
	}

	shadowVFS := &ShadowVFS{
		WorkspaceDir: workspaceRoot,
		ShadowDir:    shadowRoot,
	}

	cleanupShadow := false
	defer func() {
		if !cleanupShadow {
			return
		}
		_ = os.RemoveAll(shadowRoot)
	}()

	if err := os.RemoveAll(shadowRoot); err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("reset shadow dir %q: %w", shadowRoot, err)
	}
	if err := os.MkdirAll(shadowRoot, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create shadow dir %q: %w", shadowRoot, err)
	}
	cleanupShadow = true

	if _, _, err := shadowVFS.normalizedRoots(); err != nil {
		return nil, nil, err
	}

	receipt := Receipt{
		SessionID:           sessionID,
		SkillID:             skill.SkillID,
		SchemaVersion:       skill.SchemaVersion,
		LogicalHash:         skill.GetLogicalHash(),
		Timestamp:           time.Now().UTC(),
		GrantedCapabilities: cloneCapabilities(skill.Capabilities),
		ShadowPath:          shadowRoot,
	}

	if err := writeReceipt(receiptPath, receipt); err != nil {
		return nil, nil, err
	}

	cleanupShadow = false
	return shadowVFS, sanitizedInputs, nil
}

func compilerSessionPaths(skillID, sessionID string) (string, string, error) {
	safeSkillID, err := sanitizeReceiptPathComponent(skillID, "skill_id")
	if err != nil {
		return "", "", err
	}
	safeSessionID, err := sanitizeReceiptPathComponent(sessionID, "session_id")
	if err != nil {
		return "", "", err
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve user home directory: %w", err)
	}

	loomRoot := filepath.Join(homeDir, ".loom")
	shadowRoot := filepath.Join(loomRoot, "shadow", safeSessionID)
	receiptPath := filepath.Join(loomRoot, "cache", safeSkillID, safeSessionID+"_receipt.json")

	return shadowRoot, receiptPath, nil
}

func writeReceipt(receiptPath string, receipt Receipt) error {
	if err := os.MkdirAll(filepath.Dir(receiptPath), 0o755); err != nil {
		return fmt.Errorf("create receipt directory %q: %w", filepath.Dir(receiptPath), err)
	}

	receiptBytes, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal execution receipt: %w", err)
	}
	receiptBytes = append(receiptBytes, '\n')

	tempFile, err := os.CreateTemp(filepath.Dir(receiptPath), ".loom-receipt-*")
	if err != nil {
		return fmt.Errorf("create temp receipt in %q: %w", filepath.Dir(receiptPath), err)
	}

	tempPath := tempFile.Name()
	writeSucceeded := false
	defer func() {
		if writeSucceeded {
			return
		}
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
	}()

	if _, err := tempFile.Write(receiptBytes); err != nil {
		return fmt.Errorf("write temp receipt %q: %w", tempPath, err)
	}
	if err := tempFile.Sync(); err != nil {
		return fmt.Errorf("sync temp receipt %q: %w", tempPath, err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close temp receipt %q: %w", tempPath, err)
	}
	if err := replaceFile(tempPath, receiptPath); err != nil {
		return fmt.Errorf("persist receipt %q: %w", receiptPath, err)
	}

	writeSucceeded = true
	return nil
}

func sanitizeReceiptPathComponent(value, fieldName string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", &ContractError{
			Field:  fieldName,
			Reason: "path component must not be empty",
		}
	}

	cleaned := filepath.Clean(trimmed)
	switch {
	case cleaned == ".":
		return "", &ContractError{
			Field:  fieldName,
			Reason: "path component must not resolve to current directory",
		}
	case filepath.IsAbs(cleaned):
		return "", &SecurityError{
			Field:  fieldName,
			Reason: "path component must not be absolute",
		}
	case strings.Contains(cleaned, string(filepath.Separator)):
		return "", &SecurityError{
			Field:  fieldName,
			Reason: "path component must not contain path separators",
		}
	case filepath.VolumeName(cleaned) != "":
		return "", &SecurityError{
			Field:  fieldName,
			Reason: "path component must not contain a volume prefix",
		}
	case cleaned == "..":
		return "", &SecurityError{
			Field:  fieldName,
			Reason: "path component must not escape parent directories",
		}
	}

	return cleaned, nil
}

func cloneCapabilities(capabilities []Capability) []Capability {
	if len(capabilities) == 0 {
		return []Capability{}
	}

	cloned := make([]Capability, len(capabilities))
	copy(cloned, capabilities)
	return cloned
}
