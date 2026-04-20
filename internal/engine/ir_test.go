package engine

import (
	"encoding/json"
	"testing"
	"time"
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

func TestLogicalHashIgnoresDescription(t *testing.T) {
	t.Parallel()

	// Description is pure metadata for tool-discovery surfaces. Changing
	// it must not invalidate admission fingerprints or break audit trails.
	skillA := sampleV1Skill()
	skillB := sampleV1Skill()
	skillB.Description = "A completely different description that should not affect behavior."

	if skillA.GetLogicalHash() != skillB.GetLogicalHash() {
		t.Fatal("hash changed when only Description changed; Description must not participate in the hash")
	}
}

func TestLogicalHashIgnoresProvenance(t *testing.T) {
	t.Parallel()

	// Provenance captures migration metadata and review state — pure
	// audit context, not behavior. Two skills with identical behavior
	// but different provenance must produce the same logical hash so
	// moving a reviewed skill between workspaces (or flipping Reviewed
	// via accept-migration) does NOT invalidate earlier admission
	// fingerprints.
	skillA := sampleV1Skill()
	skillB := sampleV1Skill()
	skillB.Provenance = &Provenance{
		Origin:     "openclaw-migrate",
		Mode:       ProvenanceModeLLMAssisted,
		Model:      "claude-sonnet-4-6",
		SourcePath: "skills/demo.md",
		SourceHash: "deadbeef",
		Reviewed:   true,
	}

	if skillA.GetLogicalHash() != skillB.GetLogicalHash() {
		t.Fatal("hash changed when only Provenance changed; Provenance must not participate in the hash")
	}
}

func TestCanonicalBodyHashExcludesReviewState(t *testing.T) {
	t.Parallel()

	// ReviewerSignature is computed over the body EXCLUDING itself and
	// the Reviewed/ReviewedAt fields. Otherwise either (a) the signature
	// would self-reference (impossible to compute) or (b) flipping
	// Reviewed during accept-migration would invalidate the signature
	// it just wrote. Verify: flipping Reviewed on an otherwise-identical
	// skill must NOT change CanonicalBodyHash.
	now := time.Now().UTC()

	base := sampleV1Skill()
	base.Provenance = &Provenance{
		Origin:     "openclaw-migrate",
		Mode:       ProvenanceModeMechanical,
		SourcePath: "skills/demo.md",
		SourceHash: "abc123",
		MigratedAt: now,
	}

	hash1, err := CanonicalBodyHash(base)
	if err != nil {
		t.Fatalf("CanonicalBodyHash(unreviewed) error = %v", err)
	}

	reviewed := *base
	reviewedProv := *base.Provenance
	reviewedProv.Reviewed = true
	reviewedAt := now.Add(time.Hour)
	reviewedProv.ReviewedAt = &reviewedAt
	reviewedProv.ReviewerSignature = "would-not-be-this-without-exclusion"
	reviewed.Provenance = &reviewedProv

	hash2, err := CanonicalBodyHash(&reviewed)
	if err != nil {
		t.Fatalf("CanonicalBodyHash(reviewed) error = %v", err)
	}

	if hash1 != hash2 {
		t.Fatalf("CanonicalBodyHash differs across review state: unreviewed=%q reviewed=%q (must exclude Reviewed/ReviewedAt/ReviewerSignature)", hash1, hash2)
	}
}

func TestCanonicalBodyHashChangesOnBodyEdit(t *testing.T) {
	t.Parallel()

	// Sanity check the other direction: if any reviewable field
	// changes (Capabilities, ExecutionDAG, Parameters, Description,
	// non-review provenance fields), the hash MUST change. Otherwise
	// someone could hand-edit a reviewed skill's behavior and keep the
	// old signature.
	base := sampleV1Skill()
	base.Provenance = &Provenance{
		Origin:     "openclaw-migrate",
		Mode:       ProvenanceModeMechanical,
		SourcePath: "skills/demo.md",
		SourceHash: "abc123",
	}
	hash1, err := CanonicalBodyHash(base)
	if err != nil {
		t.Fatalf("CanonicalBodyHash(base) error = %v", err)
	}

	modified := *base
	modified.Capabilities = []Capability{
		{Kind: CapKindVFSWrite, Scope: "elsewhere/"},
	}
	hash2, err := CanonicalBodyHash(&modified)
	if err != nil {
		t.Fatalf("CanonicalBodyHash(modified) error = %v", err)
	}

	if hash1 == hash2 {
		t.Fatal("CanonicalBodyHash unchanged after editing Capabilities; signature would fail to detect tampering")
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
