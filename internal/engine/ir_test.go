package engine

import (
	"encoding/json"
	"testing"
)

func TestLogicalHashIsDeterministic(t *testing.T) {
	t.Parallel()

	skill := sampleV1Skill()
	first := skill.GetLogicalHash()
	second := skill.GetLogicalHash()

	if first != second {
		t.Fatalf("hash not deterministic: first=%q second=%q", first, second)
	}
}

func TestLogicalHashDiffersOnSchemaVersion(t *testing.T) {
	t.Parallel()

	skillA := sampleV1Skill()
	skillB := sampleV1Skill()
	skillB.SchemaVersion = ""

	if skillA.GetLogicalHash() == skillB.GetLogicalHash() {
		t.Fatal("hash collides across schema versions; version must bind every byte that follows")
	}
}

func TestLogicalHashIgnoresSkillID(t *testing.T) {
	t.Parallel()

	skillA := sampleV1Skill()
	skillB := sampleV1Skill()
	skillB.SkillID = "different-name"

	if skillA.GetLogicalHash() != skillB.GetLogicalHash() {
		t.Fatal("hash changed when only SkillID changed; SkillID must not participate in the hash")
	}
}

func TestLogicalHashIgnoresCapabilityOrdering(t *testing.T) {
	t.Parallel()

	skillA := sampleV1Skill()
	skillB := sampleV1Skill()
	skillB.Capabilities = []Capability{
		{Kind: CapKindVFSWrite, Scope: "out/"},
		{Kind: CapKindVFSRead, Scope: "in/"},
	}
	skillA.Capabilities = []Capability{
		{Kind: CapKindVFSRead, Scope: "in/"},
		{Kind: CapKindVFSWrite, Scope: "out/"},
	}

	if skillA.GetLogicalHash() != skillB.GetLogicalHash() {
		t.Fatal("hash changed under capability re-ordering; canonicalization must sort")
	}
}

func TestLogicalHashStableAcrossJSONRoundTrip(t *testing.T) {
	t.Parallel()

	skill := sampleV1Skill()
	originalHash := skill.GetLogicalHash()

	raw, err := json.Marshal(skill)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var decoded LoomSkill
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if decoded.GetLogicalHash() != originalHash {
		t.Fatalf("hash changed over JSON round-trip: before=%q after=%q", originalHash, decoded.GetLogicalHash())
	}
}

func TestScopeCovers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		declared, derived string
		want              bool
	}{
		{"out/", "out/hello.txt", true},
		{"out/", "out/nested/file.txt", true},
		{"out/hello.txt", "out/hello.txt", true},
		{"out/", "other/file.txt", false},
		{"out", "output/file.txt", false},
		{"", "anything", false},
	}
	for _, tc := range cases {
		got := ScopeCovers(tc.declared, tc.derived)
		if got != tc.want {
			t.Errorf("ScopeCovers(%q, %q) = %v, want %v", tc.declared, tc.derived, got, tc.want)
		}
	}
}

func sampleV1Skill() *LoomSkill {
	return &LoomSkill{
		SchemaVersion: CurrentSchemaVersion,
		SkillID:       "sample",
		Parameters: map[string]Parameter{
			"msg": {Type: ParameterTypeString, Required: true},
		},
		ExecutionDAG: []Step{
			{
				StepID: "s1",
				Kind:   StepKindWriteFile,
				Args:   WriteFileArgs{Path: "out/hello.txt", Content: "hi"},
			},
		},
		Capabilities: []Capability{
			{Kind: CapKindVFSWrite, Scope: "out/"},
		},
	}
}
