# loom-cli Architecture

## Positioning

`loom-cli` is not a single gateway process. It is a defense-in-depth control plane for AI skills.

Its purpose is to take ambiguous external intent from agents, markdown skills, MCP requests, or future protocol adapters, and force that intent through a deterministic chain of typed validation, sanitization, isolation, and audit before any side effect is allowed to reach the real workspace.

The core design rule is simple:

> Every layer assumes the previous layer can fail.

That rule is what makes the system a layered defense model rather than a thin API gateway.

## System Model

The current system is built as a sequence of trust-reducing stages:

1. Protocol ingress
   - CLI in `cmd/main.go`
   - MCP sidecar in `cmd/serve.go`
   - Future ingress points may include git hooks, webhooks, or other RPC adapters

2. Parser frontends
   - `internal/engine/parser`
   - Translates untrusted external formats into internal typed IR
   - Eliminates syntax ambiguity before semantic checks begin

3. Typed semantic IR
   - `internal/engine/ir.go`
   - Represents the skill contract in strong Go types
   - Produces a canonical logical hash to bind runtime behavior to audited structure

4. Static validator
   - `internal/engine/validator.go`
   - Verifies graph structure, dataflow references, permission scope, static dangerous commands, and SSRF indicators
   - Rejects malformed or obviously hostile skills before runtime

5. Input sanitizer
   - `internal/engine/sanitizer.go`
   - Enforces input contract typing and required/default semantics
   - Blocks hostile string payloads before they become runtime values

6. Shadow workspace isolation
   - `internal/engine/vfs.go`
   - Forces writes into an isolated shadow tree
   - Prevents real workspace mutation before explicit commit

7. Compiler / admission controller
   - `internal/engine/compiler.go`
   - Orchestrates validation, sanitization, shadow allocation, and receipt emission
   - Converts "allowed to run" into an auditable event

8. Runtime ingress response
   - Current runtime shape is interception-only
   - `cmd/serve.go` returns verified metadata instead of executing side effects

## Trust Boundaries

### Boundary 1: External syntax to internal IR

The parser layer is the first hard boundary. External formats are not trusted.

- Markdown is not execution intent
- JSON is not execution intent
- Agent text is not execution intent

Only a successfully parsed `engine.LoomSkill` crosses this boundary.

### Boundary 2: IR to admissible skill

A syntactically valid skill is still untrusted. It must pass:

- structural graph validation
- permission review
- static security scanning

Only a validated skill may proceed to input binding.

### Boundary 3: Raw caller input to typed runtime values

Even a validated skill does not imply safe runtime parameters.

`SanitizeInput` is responsible for converting raw user-supplied strings into typed values and rejecting:

- missing required parameters
- unknown parameters
- invalid type coercions
- obvious injection markers

### Boundary 4: Logical approval to filesystem side effects

A sanitized request still cannot touch the real workspace.

All writes are redirected into `ShadowVFS.ShadowDir`. This is the physical isolation boundary. Real workspace bytes must remain unchanged until a later explicit commit gate approves promotion.

### Boundary 5: Pre-commit to real mutation

Commit is the final mutation boundary.

Today the system copies isolated results from shadow into workspace. This gate is the natural place for:

- final output review
- deletion approval
- conflict detection
- audit manifest comparison

## Why This Is Defense in Depth

Each layer protects against a different class of failure:

- Parser protects against format ambiguity
- IR protects against semantic drift
- Validator protects against malformed or obviously hostile plans
- Sanitizer protects against hostile runtime values
- ShadowVFS protects against mistaken or malicious writes
- Compiler protects against bypass of the admission sequence
- Receipts protect auditability and post-incident attribution

No single layer is expected to be perfect.

Examples:

- If the parser misclassifies text, the validator should still reject undeclared dependencies or dangerous static content.
- If the validator misses a case, the sanitizer should still reject hostile runtime strings.
- If sanitization misses a case, the shadow workspace should still prevent direct real-workspace corruption.
- If execution generates dangerous output, a later commit gate should still be able to stop promotion.

## Current Strengths

The current repository already has a strong skeleton:

- canonical logical hashing for behavior-defining IR
- strongly typed policy loading with fail-fast regex validation
- typed input sanitization
- static security validation
- shadow workspace isolation
- deterministic compile/admission receipts
- MCP sidecar ingress and CLI verification ingress

This is already more than a conventional API gateway. It is an admission and isolation system.

## Current Gaps

The major missing pieces are runtime-completion features, not architectural direction:

1. Structured execution runtime
   - No executor yet
   - No step dispatcher yet

2. Richer IR
   - `Step.Action` still carries natural-language strings
   - Execution semantics are not yet fully typed

3. Commit gate maturity
   - Needs full deletion semantics
   - Needs conflict detection
   - Needs final change manifest and approval hooks

4. Stronger final audit
   - Output review and mutation review should happen before workspace promotion

5. Broader parser support
   - MCP JSON/native skill formats are not yet implemented

## Near-Term Roadmap

### Phase A: Strengthen isolation and promotion

- add deletion semantics to `ShadowVFS`
- track shadow manifests
- add commit-time conflict checks

### Phase B: Introduce runtime executor

- add typed step execution contracts
- route all IO through `ShadowVFS`
- make output redaction mandatory on all boundary crossings

### Phase C: Upgrade IR from descriptive to executable

- replace natural-language `Action` strings with typed step kinds and typed arguments
- make permissions derivable from step semantics, not only declared metadata

### Phase D: Add final commit gate

- compute shadow diff
- audit proposed mutations
- only then allow promotion to workspace

## Design Principles

### 1. Strong types over dynamic fallback

The internal model must continue to avoid `map[string]interface{}` or `any` as semantic escape hatches for core logic. Typed ambiguity becomes security ambiguity.

### 2. Fail fast

Do not carry partially invalid state deeper into the system.

### 3. Admission before execution

All costly or dangerous work happens after compile-time admission checks, not before.

### 4. Isolation before mutation

No execution path should be able to mutate the real workspace without first passing through the shadow layer.

### 5. Auditability as a first-class property

Every approved session should be explainable after the fact.

## Summary

`loom-cli` should be understood as a layered deterministic skill governance plane:

- multiple ingress forms
- one typed internal contract
- multiple pre-execution defense layers
- isolated side effects
- auditable admission decisions

That is the core architectural identity of the project.
