package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

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
	Type         ParameterType `json:"type"`          // Governs which primitive domain untrusted input may occupy.
	DefaultValue string        `json:"default_value"` // Freezes implicit behavior when a caller omits this argument.
	Required     bool          `json:"required"`      // Forces explicit caller intent for inputs that must not be inferred.
}

// Step is a single node in the execution DAG.
type Step struct {
	StepID  string            `json:"step_id"` // Stable node identity for audit trails, references, and dependency checks.
	Action  string            `json:"action"`  // Names the deterministic executor verb bound to this node.
	Inputs  map[string]string `json:"inputs"`  // Declares explicit dataflow from parameters, literals, or upstream outputs.
	Outputs []string          `json:"outputs"` // Declares the only variables this node is allowed to emit downstream.
}

// LoomSkill is the strongly typed semantic contract of a governed skill.
type LoomSkill struct {
	SkillID      string               `json:"skill_id"`      // Human-stable skill identity; excluded from logical hashing to isolate behavior.
	Parameters   map[string]Parameter `json:"parameters"`    // Freezes the skill's accepted input surface.
	ExecutionDAG []Step               `json:"execution_dag"` // Defines the allowed execution graph and dataflow edges.
	Permissions  map[string][]string  `json:"permissions"`   // Binds actions to least-privilege capability scopes.
}

// GetLogicalHash returns a stable SHA-256 fingerprint of the behavior-defining fields.
func (s *LoomSkill) GetLogicalHash() string {
	if s == nil {
		s = &LoomSkill{}
	}

	var builder strings.Builder
	builder.Grow(256)
	builder.WriteByte('{')
	builder.WriteString(`"parameters":`)
	writeParametersJSON(&builder, s.Parameters)
	builder.WriteString(`,"execution_dag":`)
	writeExecutionDAGJSON(&builder, s.ExecutionDAG)
	builder.WriteString(`,"permissions":`)
	writePermissionsJSON(&builder, s.Permissions)
	builder.WriteByte('}')

	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

func writeParametersJSON(builder *strings.Builder, parameters map[string]Parameter) {
	builder.WriteByte('{')

	keys := sortedParameterKeys(parameters)
	for index, key := range keys {
		if index > 0 {
			builder.WriteByte(',')
		}

		writeJSONString(builder, key)
		builder.WriteByte(':')
		writeParameterJSON(builder, parameters[key])
	}

	builder.WriteByte('}')
}

func writeParameterJSON(builder *strings.Builder, parameter Parameter) {
	builder.WriteByte('{')
	builder.WriteString(`"type":`)
	writeJSONString(builder, string(parameter.Type))
	builder.WriteString(`,"default_value":`)
	writeJSONString(builder, parameter.DefaultValue)
	builder.WriteString(`,"required":`)
	if parameter.Required {
		builder.WriteString("true")
	} else {
		builder.WriteString("false")
	}
	builder.WriteByte('}')
}

func writeExecutionDAGJSON(builder *strings.Builder, steps []Step) {
	builder.WriteByte('[')

	for index, step := range steps {
		if index > 0 {
			builder.WriteByte(',')
		}

		writeStepJSON(builder, step)
	}

	builder.WriteByte(']')
}

func writeStepJSON(builder *strings.Builder, step Step) {
	builder.WriteByte('{')
	builder.WriteString(`"step_id":`)
	writeJSONString(builder, step.StepID)
	builder.WriteString(`,"action":`)
	writeJSONString(builder, step.Action)
	builder.WriteString(`,"inputs":`)
	writeInputsJSON(builder, step.Inputs)
	builder.WriteString(`,"outputs":`)
	writeStringSliceJSON(builder, step.Outputs)
	builder.WriteByte('}')
}

func writeInputsJSON(builder *strings.Builder, inputs map[string]string) {
	builder.WriteByte('{')

	keys := sortedInputKeys(inputs)
	for index, key := range keys {
		if index > 0 {
			builder.WriteByte(',')
		}

		writeJSONString(builder, key)
		builder.WriteByte(':')
		writeJSONString(builder, inputs[key])
	}

	builder.WriteByte('}')
}

func writePermissionsJSON(builder *strings.Builder, permissions map[string][]string) {
	builder.WriteByte('{')

	keys := sortedPermissionKeys(permissions)
	for index, key := range keys {
		if index > 0 {
			builder.WriteByte(',')
		}

		writeJSONString(builder, key)
		builder.WriteByte(':')
		writeSortedStringSliceJSON(builder, permissions[key])
	}

	builder.WriteByte('}')
}

func writeStringSliceJSON(builder *strings.Builder, values []string) {
	builder.WriteByte('[')

	for index, value := range values {
		if index > 0 {
			builder.WriteByte(',')
		}

		writeJSONString(builder, value)
	}

	builder.WriteByte(']')
}

func writeSortedStringSliceJSON(builder *strings.Builder, values []string) {
	sortedValues := append([]string(nil), values...)
	sort.Strings(sortedValues)
	writeStringSliceJSON(builder, sortedValues)
}

func writeJSONString(builder *strings.Builder, value string) {
	builder.WriteString(strconv.Quote(value))
}

func sortedParameterKeys(parameters map[string]Parameter) []string {
	keys := make([]string, 0, len(parameters))
	for key := range parameters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedInputKeys(inputs map[string]string) []string {
	keys := make([]string, 0, len(inputs))
	for key := range inputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedPermissionKeys(permissions map[string][]string) []string {
	keys := make([]string, 0, len(permissions))
	for key := range permissions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
