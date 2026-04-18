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

	if err := auditCapabilities(skill); err != nil {
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

// auditCapabilities enforces the Capability-based security contract.
//
// First pass: any declared scope that maps to a high-risk filesystem root
// (/, /etc, /root, /var) is refused outright. This matches pre-v1 policy so
// high-risk declarations cannot smuggle through the schema change.
//
// Second pass (v1 only): for every Step, the capability set derived from
// its Kind+Args must be covered by the declared capabilities. Declared
// capabilities are an upper bound — they may only narrow. Any derived cap
// whose scope is not prefix-covered by a declared cap of the same Kind is a
// security violation.
func auditCapabilities(skill *LoomSkill) error {
	for _, capability := range skill.Capabilities {
		if isHighRiskPermissionScope(capability.Scope) {
			return &SecurityError{
				Field:  string(capability.Kind),
				Reason: fmt.Sprintf("requests high-risk filesystem scope %q", capability.Scope),
			}
		}
	}

	if skill.SchemaVersion != CurrentSchemaVersion {
		return nil
	}

	for _, step := range skill.ExecutionDAG {
		for _, derived := range DefaultCapabilitiesFor(step) {
			if !capabilityCovered(skill.Capabilities, derived) {
				return &SecurityError{
					Field:  step.StepID,
					Reason: fmt.Sprintf("step requires capability %s:%s that is not declared", derived.Kind, derived.Scope),
				}
			}
		}
	}

	return nil
}

func capabilityCovered(declared []Capability, derived Capability) bool {
	for _, decl := range declared {
		if decl.Kind != derived.Kind {
			continue
		}
		if ScopeCovers(decl.Scope, derived.Scope) {
			return true
		}
	}
	return false
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

// collectStaticSegments returns the string surfaces of a Step that static
// scanners (dangerous-command, SSRF) should inspect. For v0 legacy steps the
// natural-language action text is included. For v1 typed args, the path and
// content of file ops are included so existing rules still catch literal
// dangerous tokens smuggled through a file-write.
func collectStaticSegments(step Step) []staticSegment {
	segments := make([]staticSegment, 0, len(step.Inputs)+2)

	switch args := step.Args.(type) {
	case LegacyStepArgs:
		segments = append(segments, staticSegment{
			Kind:  "action",
			Name:  "action",
			Value: args.Action,
		})
	case ReadFileArgs:
		segments = append(segments, staticSegment{
			Kind:  "args",
			Name:  "path",
			Value: args.Path,
		})
	case WriteFileArgs:
		segments = append(segments, staticSegment{
			Kind:  "args",
			Name:  "path",
			Value: args.Path,
		})
		segments = append(segments, staticSegment{
			Kind:  "args",
			Name:  "content",
			Value: args.Content,
		})
	}

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
