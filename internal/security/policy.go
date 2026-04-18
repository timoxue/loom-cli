package security

import (
	"bytes"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// RegexAction defines the governance response once a rule matches.
type RegexAction string

const (
	RegexActionDeny   RegexAction = "deny"
	RegexActionRedact RegexAction = "redact"
)

// RegexRule defines a pattern-driven security control.
type RegexRule struct {
	Name    string      `json:"name" yaml:"name"`       // Stable rule identity for audit trails and operator reasoning.
	Pattern string      `json:"pattern" yaml:"pattern"` // Match expression enforced by validators and sanitizers.
	Action  RegexAction `json:"action" yaml:"action"`   // Required governance response when the pattern is hit.

	compiledPattern *regexp.Regexp // Prepared once at policy load time to avoid runtime recompilation.
}

// SSRFPolicy defines outbound network destinations that must never be reached.
type SSRFPolicy struct {
	BlockedCIDRs   []string `json:"blocked_cidrs" yaml:"blocked_cidrs"`     // Rejects egress into privileged address ranges such as loopback and metadata services.
	BlockedDomains []string `json:"blocked_domains" yaml:"blocked_domains"` // Rejects hostnames reserved for internal or high-risk targets.
}

// SecurityPolicy is the top-level, strongly typed policy bundle for runtime defenses.
type SecurityPolicy struct {
	DangerousCommands []RegexRule `json:"dangerous_commands" yaml:"dangerous_commands"` // Denies command patterns that can damage or overexpose the host.
	Credentials       []RegexRule `json:"credentials" yaml:"credentials"`               // Detects secrets that must be denied or redacted before crossing trust boundaries.
	SSRF              SSRFPolicy  `json:"ssrf" yaml:"ssrf"`                             // Blocks outbound destinations associated with SSRF and metadata exfiltration.
}

// LoadPolicy parses a YAML policy document and rejects invalid rules before runtime.
func LoadPolicy(configData []byte) (*SecurityPolicy, error) {
	if len(bytes.TrimSpace(configData)) == 0 {
		return nil, fmt.Errorf("security policy is empty")
	}

	decoder := yaml.NewDecoder(bytes.NewReader(configData))
	decoder.KnownFields(true)

	var policy SecurityPolicy
	if err := decoder.Decode(&policy); err != nil {
		return nil, fmt.Errorf("decode security policy: %w", err)
	}

	var trailingDocument struct{}
	if err := decoder.Decode(&trailingDocument); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("security policy must contain exactly one YAML document")
		}
		return nil, fmt.Errorf("decode trailing security policy document: %w", err)
	}

	if err := policy.validate(); err != nil {
		return nil, err
	}

	return &policy, nil
}

// DefaultPolicy returns the built-in baseline policy that enforces the minimum safety floor.
func DefaultPolicy() *SecurityPolicy {
	policy := &SecurityPolicy{
		DangerousCommands: []RegexRule{
			{
				Name:    "rm_rf",
				Pattern: `(?i)\brm\s+-rf\b`,
				Action:  RegexActionDeny,
			},
			{
				Name:    "chmod_777",
				Pattern: `(?i)\bchmod\s+777\b`,
				Action:  RegexActionDeny,
			},
		},
		Credentials: []RegexRule{
			{
				Name:    "openai_style_key",
				Pattern: `sk-[A-Za-z0-9]{48}`,
				Action:  RegexActionRedact,
			},
			{
				Name:    "bearer_token",
				Pattern: `Bearer\s+\S+`,
				Action:  RegexActionRedact,
			},
		},
		SSRF: SSRFPolicy{
			BlockedCIDRs: []string{
				"169.254.169.254/32",
				"127.0.0.0/8",
			},
			BlockedDomains: []string{},
		},
	}

	if err := policy.validate(); err != nil {
		panic("security: invalid default policy: " + err.Error())
	}

	return policy
}

func (p *SecurityPolicy) validate() error {
	if p == nil {
		return fmt.Errorf("security policy is nil")
	}

	if err := validateRegexRules("dangerous_commands", p.DangerousCommands); err != nil {
		return err
	}
	if err := validateRegexRules("credentials", p.Credentials); err != nil {
		return err
	}
	if err := validateSSRFPolicy(p.SSRF); err != nil {
		return err
	}

	return nil
}

func validateRegexRules(fieldName string, rules []RegexRule) error {
	for index := range rules {
		rule := &rules[index]
		rulePath := fmt.Sprintf("%s[%d]", fieldName, index)

		if strings.TrimSpace(rule.Name) == "" {
			return fmt.Errorf("%s.name must not be empty", rulePath)
		}
		if strings.TrimSpace(rule.Pattern) == "" {
			return fmt.Errorf("%s.pattern must not be empty", rulePath)
		}
		if !rule.Action.isValid() {
			return fmt.Errorf("%s.action %q is invalid", rulePath, rule.Action)
		}
		compiledPattern, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return fmt.Errorf("%s.pattern %q is invalid: %w", rulePath, rule.Pattern, err)
		}
		rule.compiledPattern = compiledPattern
	}

	return nil
}

func validateSSRFPolicy(policy SSRFPolicy) error {
	for index, cidr := range policy.BlockedCIDRs {
		if strings.TrimSpace(cidr) == "" {
			return fmt.Errorf("ssrf.blocked_cidrs[%d] must not be empty", index)
		}
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return fmt.Errorf("ssrf.blocked_cidrs[%d] %q is invalid: %w", index, cidr, err)
		}
	}

	for index, domain := range policy.BlockedDomains {
		if strings.TrimSpace(domain) == "" {
			return fmt.Errorf("ssrf.blocked_domains[%d] must not be empty", index)
		}
	}

	return nil
}

func (a RegexAction) isValid() bool {
	switch a {
	case RegexActionDeny, RegexActionRedact:
		return true
	default:
		return false
	}
}

// CompiledPattern returns the precompiled regular expression prepared during policy validation.
func (r *RegexRule) CompiledPattern() *regexp.Regexp {
	if r == nil {
		return nil
	}

	return r.compiledPattern
}
