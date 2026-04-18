package engine

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// CurrentSchemaVersion is the only schema version the executor accepts today.
// v0 (empty) skills can still be parsed and statically verified, but they are
// rejected at the executor boundary because their Steps carry no typed Kind.
const CurrentSchemaVersion = "v1"

// ParameterType constrains the primitive domain accepted by a skill parameter.
type ParameterType string

const (
	ParameterTypeString ParameterType = "string"
	ParameterTypeInt    ParameterType = "int"
	ParameterTypeBool   ParameterType = "bool"
	ParameterTypeFloat  ParameterType = "float"
)

// Parameter defines a caller-facing input contract for a skill.
type Parameter struct {
	Type         ParameterType `json:"type"`
	DefaultValue string        `json:"default_value"`
	Required     bool          `json:"required"`
}

// StepKind is the closed-enum verb a Step dispatches to. New kinds are added
// through argsRegistry, never by re-introducing a free-form string.
type StepKind string

const (
	StepKindReadFile  StepKind = "read_file"
	StepKindWriteFile StepKind = "write_file"
	// StepKindLegacy carries v0 markdown instruction text. It is never executable.
	StepKindLegacy StepKind = "legacy"
)

// StepArgs is the typed payload bound to a StepKind. Each implementation owns
// its canonical byte form so the logical hash stays stable without a central
// type switch that every new kind has to edit.
type StepArgs interface {
	stepKind() StepKind
	writeCanonical(w io.Writer) error
}

// ReadFileArgs names a single path inside the shadow tree to read.
type ReadFileArgs struct {
	Path string `json:"path"`
}

func (ReadFileArgs) stepKind() StepKind { return StepKindReadFile }

func (a ReadFileArgs) writeCanonical(w io.Writer) error {
	return writeCanonicalFields(w, string(StepKindReadFile), map[string]string{"path": a.Path})
}

// WriteFileArgs names a single path and the literal content to write into the
// shadow tree. Content is part of the logical hash so two skills that write
// different files cannot collide.
type WriteFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (WriteFileArgs) stepKind() StepKind { return StepKindWriteFile }

func (a WriteFileArgs) writeCanonical(w io.Writer) error {
	return writeCanonicalFields(w, string(StepKindWriteFile), map[string]string{
		"path":    a.Path,
		"content": a.Content,
	})
}

// LegacyStepArgs preserves v0 markdown instruction text so the validator can
// still run string-based dangerous-command and SSRF scans. The executor
// refuses to run any Step carrying these args.
type LegacyStepArgs struct {
	Action string `json:"action"`
}

func (LegacyStepArgs) stepKind() StepKind { return StepKindLegacy }

func (a LegacyStepArgs) writeCanonical(w io.Writer) error {
	return writeCanonicalFields(w, string(StepKindLegacy), map[string]string{"action": a.Action})
}

// argsRegistry is the dispatch table for Step.UnmarshalJSON. Any new StepKind
// must register here before it can be decoded from wire format.
var argsRegistry = map[StepKind]func() StepArgs{
	StepKindReadFile:  func() StepArgs { return &ReadFileArgs{} },
	StepKindWriteFile: func() StepArgs { return &WriteFileArgs{} },
	StepKindLegacy:    func() StepArgs { return &LegacyStepArgs{} },
}

// CapabilityKind is the closed-enum axis of the capability model. Declared
// caps may only narrow the scope derived from a Step's Kind.
type CapabilityKind string

const (
	CapKindVFSRead  CapabilityKind = "vfs.read"
	CapKindVFSWrite CapabilityKind = "vfs.write"
)

// Capability binds a capability kind to a path scope. A bare kind without a
// scope would be an unbounded grant, which defeats the point of a sandbox.
type Capability struct {
	Kind  CapabilityKind `json:"kind"`
	Scope string         `json:"scope"`
}

// Step is a single node in the execution DAG. Kind + Args form a typed
// tagged union; Action-as-string has been retired.
type Step struct {
	StepID  string            `json:"step_id"`
	Kind    StepKind          `json:"kind"`
	Args    StepArgs          `json:"args"`
	Inputs  map[string]string `json:"inputs"`
	Outputs []string          `json:"outputs"`
}

// stepWire is the private wire shape used to decode Args via the registry.
type stepWire struct {
	StepID  string            `json:"step_id"`
	Kind    StepKind          `json:"kind"`
	Args    json.RawMessage   `json:"args"`
	Inputs  map[string]string `json:"inputs"`
	Outputs []string          `json:"outputs"`
}

// UnmarshalJSON peeks at Kind and dispatches Args decoding through argsRegistry.
func (s *Step) UnmarshalJSON(data []byte) error {
	var wire stepWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	s.StepID = wire.StepID
	s.Kind = wire.Kind
	s.Inputs = wire.Inputs
	s.Outputs = wire.Outputs

	factory, ok := argsRegistry[wire.Kind]
	if !ok {
		return fmt.Errorf("unknown step kind %q", wire.Kind)
	}

	args := factory()
	if len(wire.Args) > 0 && string(wire.Args) != "null" {
		if err := json.Unmarshal(wire.Args, args); err != nil {
			return fmt.Errorf("decode args for kind %q: %w", wire.Kind, err)
		}
	}

	s.Args = derefArgs(args)
	return nil
}

// MarshalJSON emits the step in the same shape stepWire expects, so a
// round-trip through JSON is stable.
func (s Step) MarshalJSON() ([]byte, error) {
	var argsBytes json.RawMessage
	if s.Args != nil {
		raw, err := json.Marshal(s.Args)
		if err != nil {
			return nil, err
		}
		argsBytes = raw
	}

	return json.Marshal(stepWire{
		StepID:  s.StepID,
		Kind:    s.Kind,
		Args:    argsBytes,
		Inputs:  s.Inputs,
		Outputs: s.Outputs,
	})
}

// derefArgs unwraps a pointer-to-concrete-type into the concrete value so
// later type switches on StepArgs produce the documented types.
func derefArgs(args StepArgs) StepArgs {
	switch concrete := args.(type) {
	case *ReadFileArgs:
		return *concrete
	case *WriteFileArgs:
		return *concrete
	case *LegacyStepArgs:
		return *concrete
	default:
		return args
	}
}

// LoomSkill is the strongly typed semantic contract of a governed skill.
type LoomSkill struct {
	SchemaVersion string               `json:"schema_version"`
	SkillID       string               `json:"skill_id"`
	Parameters    map[string]Parameter `json:"parameters"`
	ExecutionDAG  []Step               `json:"execution_dag"`
	Capabilities  []Capability         `json:"capabilities"`
}

// GetLogicalHash returns a stable SHA-256 fingerprint of the behavior-defining
// fields. SchemaVersion is the first thing written to the hasher so two
// skills that differ only in schema version produce different hashes.
// SkillID is excluded so identical logic under different names hashes alike.
func (s *LoomSkill) GetLogicalHash() string {
	if s == nil {
		s = &LoomSkill{}
	}

	hasher := sha256.New()

	// Length-prefixed schema version goes first; this binds every other
	// byte to the schema it was written under.
	writeLengthPrefixed(hasher, []byte(s.SchemaVersion))

	writeParametersCanonical(hasher, s.Parameters)
	writeStepsCanonical(hasher, s.ExecutionDAG)
	writeCapabilitiesCanonical(hasher, s.Capabilities)

	return hex.EncodeToString(hasher.Sum(nil))
}

// DefaultCapabilitiesFor returns the capabilities a Step would consume given
// its Kind and typed Args. The validator compares this derived set against
// the declared Capabilities; declared may only narrow, never expand.
func DefaultCapabilitiesFor(step Step) []Capability {
	switch args := step.Args.(type) {
	case ReadFileArgs:
		return []Capability{{Kind: CapKindVFSRead, Scope: args.Path}}
	case WriteFileArgs:
		return []Capability{{Kind: CapKindVFSWrite, Scope: args.Path}}
	default:
		return nil
	}
}

// ScopeCovers reports whether declaredScope is a prefix-cover of derivedScope
// under path semantics. An exact match covers; a directory-form declared
// scope (ending with "/") covers any descendant. No implicit coverage: "out"
// does NOT cover "output/..." unless declared as "out/".
func ScopeCovers(declaredScope, derivedScope string) bool {
	if declaredScope == derivedScope {
		return true
	}
	if declaredScope == "" {
		return false
	}
	suffix := declaredScope
	if !strings.HasSuffix(suffix, "/") {
		suffix += "/"
	}
	return strings.HasPrefix(derivedScope, suffix)
}

func writeParametersCanonical(w io.Writer, parameters map[string]Parameter) {
	writeLengthPrefixed(w, []byte("params"))
	writeVarUint(w, uint64(len(parameters)))

	keys := sortedKeys(parameters)
	for _, key := range keys {
		param := parameters[key]
		writeLengthPrefixed(w, []byte(key))
		writeLengthPrefixed(w, []byte(param.Type))
		writeLengthPrefixed(w, []byte(param.DefaultValue))
		if param.Required {
			_, _ = w.Write([]byte{1})
		} else {
			_, _ = w.Write([]byte{0})
		}
	}
}

func writeStepsCanonical(w io.Writer, steps []Step) {
	writeLengthPrefixed(w, []byte("steps"))
	writeVarUint(w, uint64(len(steps)))

	for _, step := range steps {
		writeLengthPrefixed(w, []byte(step.StepID))
		writeLengthPrefixed(w, []byte(step.Kind))

		if step.Args != nil {
			_ = step.Args.writeCanonical(w)
		} else {
			writeLengthPrefixed(w, []byte("<nil-args>"))
		}

		inputKeys := sortedKeys(step.Inputs)
		writeVarUint(w, uint64(len(inputKeys)))
		for _, key := range inputKeys {
			writeLengthPrefixed(w, []byte(key))
			writeLengthPrefixed(w, []byte(step.Inputs[key]))
		}

		writeVarUint(w, uint64(len(step.Outputs)))
		for _, output := range step.Outputs {
			writeLengthPrefixed(w, []byte(output))
		}
	}
}

func writeCapabilitiesCanonical(w io.Writer, capabilities []Capability) {
	writeLengthPrefixed(w, []byte("caps"))

	sorted := append([]Capability(nil), capabilities...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Kind != sorted[j].Kind {
			return sorted[i].Kind < sorted[j].Kind
		}
		return sorted[i].Scope < sorted[j].Scope
	})

	writeVarUint(w, uint64(len(sorted)))
	for _, cap := range sorted {
		writeLengthPrefixed(w, []byte(cap.Kind))
		writeLengthPrefixed(w, []byte(cap.Scope))
	}
}

func writeCanonicalFields(w io.Writer, kindTag string, fields map[string]string) error {
	writeLengthPrefixed(w, []byte("args:"+kindTag))

	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	writeVarUint(w, uint64(len(keys)))
	for _, key := range keys {
		writeLengthPrefixed(w, []byte(key))
		writeLengthPrefixed(w, []byte(fields[key]))
	}

	return nil
}

func writeLengthPrefixed(w io.Writer, data []byte) {
	writeVarUint(w, uint64(len(data)))
	_, _ = w.Write(data)
}

func writeVarUint(w io.Writer, value uint64) {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], value)
	_, _ = w.Write(buf[:n])
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
