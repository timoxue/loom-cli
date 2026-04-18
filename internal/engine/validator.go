package engine

import (
	"fmt"
	"net/netip"
	"path"
	"sort"
	"strings"

	"github.com/yourname/loom-cli/internal/security"
)

// StructureError is a specialized contract error for static skill graph violations.
type StructureError = ContractError

var highRiskPermissionRoots = []string{
	"/",
	"/etc",
	"/root",
	"/var",
}

// ValidateSkill performs static graph validation and security auditing before execution.
func ValidateSkill(skill *LoomSkill, policy *security.SecurityPolicy) error {
	if skill == nil {
		return &ContractError{
			Field:  "skill",
			Reason: "skill is nil",
		}
	}
	if policy == nil {
		return &ContractError{
			Field:  "policy",
			Reason: "security policy is nil",
		}
	}

	blockedPrefixes, err := parseBlockedPrefixes(policy.SSRF.BlockedCIDRs)
	if err != nil {
		return &SecurityError{
			Field:  "policy",
			Reason: fmt.Sprintf("invalid SSRF blocked CIDR configuration: %v", err),
		}
	}

	availableVariables := make(map[string]struct{}, len(skill.Parameters))
	for name := range skill.Parameters {
		availableVariables[name] = struct{}{}
	}

	for index, step := range skill.ExecutionDAG {
		if strings.TrimSpace(step.StepID) == "" {
			return &StructureError{
				Field:  fmt.Sprintf("execution_dag[%d]", index),
				Reason: "step_id must not be empty",
			}
		}

		if err := validateStepDataflow(step, availableVariables); err != nil {
			return err
		}
		if err := auditStepSecurity(step, policy, blockedPrefixes); err != nil {
			return err
		}

		for _, output := range step.Outputs {
			if strings.TrimSpace(output) == "" {
				return &StructureError{
					Field:  step.StepID,
					Reason: "declares an empty output variable",
				}
			}
			availableVariables[output] = struct{}{}
		}
	}

	if err := auditPermissions(skill.Permissions); err != nil {
		return err
	}

	return nil
}

func validateStepDataflow(step Step, availableVariables map[string]struct{}) error {
	inputNames := make([]string, 0, len(step.Inputs))
	for inputName := range step.Inputs {
		inputNames = append(inputNames, inputName)
	}
	sort.Strings(inputNames)

	for _, inputName := range inputNames {
		inputValue := step.Inputs[inputName]

		references, err := extractVariableReferences(inputValue)
		if err != nil {
			return &StructureError{
				Field:  step.StepID,
				Reason: fmt.Sprintf("invalid variable reference in input %q: %v", inputName, err),
			}
		}

		for _, reference := range references {
			if _, exists := availableVariables[reference]; !exists {
				return &StructureError{
					Field:  step.StepID,
					Reason: fmt.Sprintf("undeclared input dependency %q in input %q", reference, inputName),
				}
			}
		}
	}

	return nil
}

func auditPermissions(permissions map[string][]string) error {
	permissionNames := make([]string, 0, len(permissions))
	for permissionName := range permissions {
		permissionNames = append(permissionNames, permissionName)
	}
	sort.Strings(permissionNames)

	for _, permissionName := range permissionNames {
		for _, scope := range permissions[permissionName] {
			if !isHighRiskPermissionScope(scope) {
				continue
			}

			return &SecurityError{
				Field:  permissionName,
				Reason: fmt.Sprintf("requests high-risk filesystem scope %q", scope),
			}
		}
	}

	return nil
}

func auditStepSecurity(step Step, policy *security.SecurityPolicy, blockedPrefixes []netip.Prefix) error {
	segments := collectStaticSegments(step)
	for _, segment := range segments {
		if err := auditDangerousCommands(step.StepID, segment, policy); err != nil {
			return err
		}
		if err := auditBlockedAddresses(step.StepID, segment, blockedPrefixes); err != nil {
			return err
		}
	}

	return nil
}

func auditDangerousCommands(stepID string, segment staticSegment, policy *security.SecurityPolicy) error {
	for index := range policy.DangerousCommands {
		rule := &policy.DangerousCommands[index]
		if rule.Action != security.RegexActionDeny {
			continue
		}

		compiledPattern := rule.CompiledPattern()
		if compiledPattern == nil {
			return &SecurityError{
				Field:  stepID,
				Reason: fmt.Sprintf("dangerous command rule %q is not compiled", rule.Name),
			}
		}
		if !compiledPattern.MatchString(segment.Value) {
			continue
		}

		return &SecurityError{
			Field:  stepID,
			Reason: fmt.Sprintf("dangerous command rule %q matched %s %q", rule.Name, segment.Kind, segment.Name),
		}
	}

	return nil
}

func auditBlockedAddresses(stepID string, segment staticSegment, blockedPrefixes []netip.Prefix) error {
	for _, candidate := range extractAddressCandidates(segment.Value) {
		for _, prefix := range blockedPrefixes {
			if !prefix.Contains(candidate) {
				continue
			}

			return &SecurityError{
				Field:  stepID,
				Reason: fmt.Sprintf("blocked SSRF target %q matched %s %q via %s", candidate, prefix, segment.Name, segment.Kind),
			}
		}
	}

	return nil
}

func parseBlockedPrefixes(rawPrefixes []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(rawPrefixes))
	for _, rawPrefix := range rawPrefixes {
		prefix, err := netip.ParsePrefix(rawPrefix)
		if err != nil {
			return nil, fmt.Errorf("parse prefix %q: %w", rawPrefix, err)
		}
		prefixes = append(prefixes, prefix)
	}

	return prefixes, nil
}

func extractVariableReferences(value string) ([]string, error) {
	references := make([]string, 0, 2)
	remaining := value

	for {
		start := strings.Index(remaining, "${")
		if start < 0 {
			break
		}

		suffix := remaining[start+2:]
		end := strings.IndexByte(suffix, '}')
		if end < 0 {
			return nil, fmt.Errorf("unterminated reference in %q", value)
		}

		reference := suffix[:end]
		if !isVariableReference(reference) {
			return nil, fmt.Errorf("invalid reference %q", reference)
		}

		references = append(references, reference)
		remaining = suffix[end+1:]
	}

	if len(references) > 0 {
		return references, nil
	}
	if isVariableReference(value) {
		return []string{value}, nil
	}

	return nil, nil
}

func isVariableReference(value string) bool {
	if value == "" {
		return false
	}

	for index, r := range value {
		switch {
		case r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'):
			continue
		case index > 0 && r >= '0' && r <= '9':
			continue
		default:
			return false
		}
	}

	return true
}

func isHighRiskPermissionScope(scope string) bool {
	cleaned := path.Clean(strings.TrimSpace(scope))
	if !strings.HasPrefix(cleaned, "/") {
		return false
	}

	for _, root := range highRiskPermissionRoots {
		switch {
		case cleaned == root:
			return true
		case root != "/" && strings.HasPrefix(cleaned, root+"/"):
			return true
		}
	}

	return false
}

func extractAddressCandidates(value string) []netip.Addr {
	tokens := strings.FieldsFunc(value, func(r rune) bool {
		switch {
		case r >= '0' && r <= '9':
			return false
		case r >= 'a' && r <= 'f':
			return false
		case r >= 'A' && r <= 'F':
			return false
		case r == '.' || r == ':' || r == '[' || r == ']':
			return false
		default:
			return true
		}
	})

	addresses := make([]netip.Addr, 0, len(tokens))
	for _, token := range tokens {
		token = strings.Trim(token, "[]")
		if token == "" {
			continue
		}

		if addr, err := netip.ParseAddr(token); err == nil {
			addresses = append(addresses, addr)
			continue
		}

		if addrPort, err := netip.ParseAddrPort(token); err == nil {
			addresses = append(addresses, addrPort.Addr())
		}
	}

	return addresses
}

func collectStaticSegments(step Step) []staticSegment {
	segments := make([]staticSegment, 0, len(step.Inputs)+1)
	segments = append(segments, staticSegment{
		Kind:  "action",
		Name:  "action",
		Value: step.Action,
	})

	inputNames := make([]string, 0, len(step.Inputs))
	for inputName := range step.Inputs {
		inputNames = append(inputNames, inputName)
	}
	sort.Strings(inputNames)

	for _, inputName := range inputNames {
		segments = append(segments, staticSegment{
			Kind:  "input",
			Name:  inputName,
			Value: step.Inputs[inputName],
		})
	}

	return segments
}

type staticSegment struct {
	Kind  string
	Name  string
	Value string
}
