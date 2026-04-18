package parser

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/yourname/loom-cli/internal/engine"
)

var (
	openClawParameterPattern   = regexp.MustCompile("^-\\s+`([^`]+)`\\s*\\(([^)]+)\\)\\s*:\\s*(.+)$")
	openClawPermissionPattern  = regexp.MustCompile("^-\\s+`([^`]+)`\\s*:\\s*(.+)$")
	openClawInstructionPattern = regexp.MustCompile(`^(\d+)\.\s+(.+)$`)
	openClawVariablePattern    = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	openClawDefaultPattern     = regexp.MustCompile(`(?i)\bdefault\s*:\s*([^\]\.]+)`)
)

// OpenClawParser adapts OpenClaw-flavored markdown skills into Loom IR.
type OpenClawParser struct{}

// Parse translates a markdown skill document into a strongly typed LoomSkill.
func (p *OpenClawParser) Parse(rawContent []byte) (*engine.LoomSkill, error) {
	content := normalizeMarkdown(rawContent)
	lines := strings.Split(content, "\n")

	frontmatter, frontmatterEndLine, err := extractFrontmatter(lines)
	if err != nil {
		return nil, err
	}

	skillID, err := parseSkillID(frontmatter)
	if err != nil {
		return nil, err
	}

	parametersLines, parametersStartLine, err := extractSection(lines, "Parameters", frontmatterEndLine)
	if err != nil {
		return nil, err
	}
	permissionsLines, permissionsStartLine, err := extractSection(lines, "Permissions", frontmatterEndLine)
	if err != nil {
		return nil, err
	}
	instructionLines, instructionsStartLine, err := extractSection(lines, "Instructions", frontmatterEndLine)
	if err != nil {
		return nil, err
	}

	parameters, err := parseParameters(parametersLines, parametersStartLine)
	if err != nil {
		return nil, err
	}
	permissions, err := parsePermissions(permissionsLines, permissionsStartLine)
	if err != nil {
		return nil, err
	}
	executionDAG, err := parseInstructions(instructionLines, instructionsStartLine)
	if err != nil {
		return nil, err
	}

	capabilities, err := capabilitiesFromPermissions(permissions)
	if err != nil {
		return nil, err
	}

	return &engine.LoomSkill{
		SchemaVersion: "",
		SkillID:       skillID,
		Parameters:    parameters,
		ExecutionDAG:  executionDAG,
		Capabilities:  capabilities,
	}, nil
}

func capabilitiesFromPermissions(permissions map[string][]string) ([]engine.Capability, error) {
	capabilities := make([]engine.Capability, 0, len(permissions))
	for _, name := range sortedPermissionNames(permissions) {
		kind, err := permissionNameToCapabilityKind(name)
		if err != nil {
			return nil, err
		}
		for _, scope := range permissions[name] {
			capabilities = append(capabilities, engine.Capability{
				Kind:  kind,
				Scope: scope,
			})
		}
	}
	return capabilities, nil
}

func permissionNameToCapabilityKind(name string) (engine.CapabilityKind, error) {
	switch name {
	case "fs.read":
		return engine.CapKindVFSRead, nil
	case "fs.write":
		return engine.CapKindVFSWrite, nil
	default:
		return "", &SyntaxError{
			Reason: fmt.Sprintf("unsupported permission name %q (v0 parser accepts fs.read and fs.write only)", name),
		}
	}
}

func sortedPermissionNames(permissions map[string][]string) []string {
	names := make([]string, 0, len(permissions))
	for name := range permissions {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j-1] > values[j]; j-- {
			values[j-1], values[j] = values[j], values[j-1]
		}
	}
}

func normalizeMarkdown(rawContent []byte) string {
	content := string(rawContent)
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	return content
}

func extractFrontmatter(lines []string) ([]string, int, error) {
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, 0, &SyntaxError{
			Line:   1,
			Reason: "missing frontmatter opening delimiter ---",
		}
	}

	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) != "---" {
			continue
		}

		return lines[1:index], index + 1, nil
	}

	return nil, 0, &SyntaxError{
		Line:   1,
		Reason: "missing frontmatter closing delimiter ---",
	}
}

func parseSkillID(frontmatter []string) (string, error) {
	matchLine, skillID, found := findFrontmatterName(frontmatter)
	if !found {
		return "", &SyntaxError{
			Line:   matchLine,
			Reason: "frontmatter is missing required name field",
		}
	}

	trimmed := strings.TrimSpace(skillID)
	if trimmed == "" {
		return "", &SyntaxError{
			Line:   matchLine,
			Reason: "frontmatter name must not be empty",
		}
	}

	return trimmed, nil
}

func extractSection(lines []string, sectionName string, startLine int) ([]string, int, error) {
	header := "## " + sectionName

	for index := max(startLine-1, 0); index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) != header {
			continue
		}

		sectionStartLine := index + 2
		endIndex := len(lines)
		for scanIndex := index + 1; scanIndex < len(lines); scanIndex++ {
			trimmed := strings.TrimSpace(lines[scanIndex])
			if strings.HasPrefix(trimmed, "## ") {
				endIndex = scanIndex
				break
			}
		}

		return lines[index+1 : endIndex], sectionStartLine, nil
	}

	return nil, 0, &SyntaxError{
		Line:   startLine,
		Reason: fmt.Sprintf("missing required section %q", header),
	}
}

func parseParameters(sectionLines []string, startLine int) (map[string]engine.Parameter, error) {
	parameters := make(map[string]engine.Parameter)
	parsedCount := 0

	for offset, rawLine := range sectionLines {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" {
			continue
		}

		lineNumber := startLine + offset
		matches := openClawParameterPattern.FindStringSubmatch(trimmed)
		if matches == nil {
			return nil, &SyntaxError{
				Line:   lineNumber,
				Reason: "invalid parameter declaration",
			}
		}

		parameterType, err := parseParameterType(matches[2], lineNumber)
		if err != nil {
			return nil, err
		}

		name := strings.TrimSpace(matches[1])
		if name == "" {
			return nil, &SyntaxError{
				Line:   lineNumber,
				Reason: "parameter name must not be empty",
			}
		}
		if _, exists := parameters[name]; exists {
			return nil, &SyntaxError{
				Line:   lineNumber,
				Reason: fmt.Sprintf("duplicate parameter %q", name),
			}
		}

		metadata := matches[3]
		parameters[name] = engine.Parameter{
			Type:         parameterType,
			DefaultValue: extractDefaultValue(metadata),
			Required:     isRequired(metadata),
		}
		parsedCount++
	}

	if parsedCount == 0 {
		return nil, &SyntaxError{
			Line:   startLine,
			Reason: "section ## Parameters does not contain any parameter declarations",
		}
	}

	return parameters, nil
}

func parsePermissions(sectionLines []string, startLine int) (map[string][]string, error) {
	permissions := make(map[string][]string)
	parsedCount := 0

	for offset, rawLine := range sectionLines {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" {
			continue
		}

		lineNumber := startLine + offset
		matches := openClawPermissionPattern.FindStringSubmatch(trimmed)
		if matches == nil {
			return nil, &SyntaxError{
				Line:   lineNumber,
				Reason: "invalid permission declaration",
			}
		}

		permissionName := strings.TrimSpace(matches[1])
		if permissionName == "" {
			return nil, &SyntaxError{
				Line:   lineNumber,
				Reason: "permission name must not be empty",
			}
		}

		rawScopes := strings.Split(matches[2], ",")
		scopes := make([]string, 0, len(rawScopes))
		for _, rawScope := range rawScopes {
			scope := strings.TrimSpace(rawScope)
			if scope == "" {
				return nil, &SyntaxError{
					Line:   lineNumber,
					Reason: fmt.Sprintf("permission %q contains an empty scope", permissionName),
				}
			}
			scopes = append(scopes, scope)
		}

		permissions[permissionName] = append(permissions[permissionName], scopes...)
		parsedCount++
	}

	if parsedCount == 0 {
		return nil, &SyntaxError{
			Line:   startLine,
			Reason: "section ## Permissions does not contain any permission declarations",
		}
	}

	return permissions, nil
}

func parseInstructions(sectionLines []string, startLine int) ([]engine.Step, error) {
	steps := make([]engine.Step, 0, len(sectionLines))
	expectedIndex := 1

	for offset, rawLine := range sectionLines {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" {
			continue
		}

		lineNumber := startLine + offset
		matches := openClawInstructionPattern.FindStringSubmatch(trimmed)
		if matches == nil {
			return nil, &SyntaxError{
				Line:   lineNumber,
				Reason: "invalid instruction declaration",
			}
		}

		if matches[1] != fmt.Sprintf("%d", expectedIndex) {
			return nil, &SyntaxError{
				Line:   lineNumber,
				Reason: fmt.Sprintf("instruction numbering must be sequential, expected %d", expectedIndex),
			}
		}

		actionText := strings.TrimSpace(matches[2])
		if actionText == "" {
			return nil, &SyntaxError{
				Line:   lineNumber,
				Reason: "instruction text must not be empty",
			}
		}

		steps = append(steps, engine.Step{
			StepID:  fmt.Sprintf("step_%d", expectedIndex),
			Kind:    engine.StepKindLegacy,
			Args:    engine.LegacyStepArgs{Action: actionText},
			Inputs:  parseInstructionInputs(actionText),
			Outputs: nil,
		})

		expectedIndex++
	}

	if len(steps) == 0 {
		return nil, &SyntaxError{
			Line:   startLine,
			Reason: "section ## Instructions does not contain any numbered instructions",
		}
	}

	return steps, nil
}

func parseInstructionInputs(actionText string) map[string]string {
	matches := openClawVariablePattern.FindAllStringSubmatch(actionText, -1)
	if len(matches) == 0 {
		return map[string]string{}
	}

	inputs := make(map[string]string, len(matches))
	for _, match := range matches {
		variableName := match[1]
		inputs[variableName] = "${" + variableName + "}"
	}

	return inputs
}

func parseParameterType(rawType string, lineNumber int) (engine.ParameterType, error) {
	switch strings.ToLower(strings.TrimSpace(rawType)) {
	case "string":
		return engine.ParameterTypeString, nil
	case "int", "integer":
		return engine.ParameterTypeInt, nil
	case "bool", "boolean":
		return engine.ParameterTypeBool, nil
	case "float", "float64", "number":
		return engine.ParameterTypeFloat, nil
	default:
		return "", &SyntaxError{
			Line:   lineNumber,
			Reason: fmt.Sprintf("unsupported parameter type %q", rawType),
		}
	}
}

func isRequired(metadata string) bool {
	lowered := strings.ToLower(metadata)
	return strings.Contains(lowered, "required") || strings.Contains(metadata, "必填")
}

func extractDefaultValue(metadata string) string {
	matches := openClawDefaultPattern.FindStringSubmatch(metadata)
	if matches == nil {
		return ""
	}

	return strings.TrimSpace(strings.Trim(matches[1], "`\"' "))
}

func findFrontmatterName(lines []string) (int, string, bool) {
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(trimmed), "name:") {
			continue
		}

		value := strings.TrimSpace(trimmed[len("name:"):])
		value = strings.Trim(value, `"`)
		return index + 2, value, true
	}

	return 1, "", false
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}
