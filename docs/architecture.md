# Loom Gateway Architecture

## Positioning

`loom-cli` is not an API gateway. It is a **deterministic governance plane for AI skills**: a control surface that treats every incoming intent — from agents, markdown skills, MCP calls, or future protocols — as untrusted, and forces that intent through a sequence of typed, shrinking trust boundaries before any real side effect is permitted.

Its fundamental bet is that **skills and prompts are long-lived assets, while agents and LLMs churn**. The internal representation is therefore the stable center of the system; ingress adapters and LLM-facing tooling are the replaceable edges.

The design rule that makes this possible:

> Every layer assumes the previous layer can fail.

This is not defense-in-depth as a slogan. It is the concrete reason why no single component — not the parser, not the validator, not the sanitizer, not even the shadow filesystem — is trusted to be the last line of safety.

## Core Design Philosophy

Five principles run through every module. They are load-bearing; violating any one of them compromises the security story.

### 1. Typed ambiguity becomes security ambiguity

The internal IR must never use `any`, `map[string]interface{}`, or free-form strings as semantic carriers. A `Step.Action string` would force validators into regex scans for intent; a `Spec map[string]any` would push the same untyped payload one level deeper. Both collapse the distinction between "legal" and "safe." The IR uses typed tagged unions (`StepKind` + `StepArgs` interface with per-kind structs) so every validator and executor branch operates on a concrete type, not a parse of a string.

### 2. Capabilities carry scope

A bare capability name ("vfs.write") is an unbounded grant. Every capability in the IR binds a `Kind` to a `Scope` (e.g. `vfs.write` + `out/`). Declared capabilities are an **upper bound** on effective capabilities — the validator computes the derived capability set from each Step's typed Args and rejects the skill if any derived scope is not prefix-covered by a declaration of the same kind. Declarations can only narrow, never expand.

### 3. Admission before execution

Nothing that can mutate state runs before the admission decision is sealed. Validation, sanitization, and capability-ceiling checks all complete — and a Receipt with a canonical logical hash is written — before the executor dispatches a single step. Admission converts "intent" into an auditable fact.

### 4. Isolation before mutation

No execution path can mutate the real workspace. All writes resolve through `ShadowVFS` into an isolated shadow tree. The **commit gate** is a separate boundary that inspects the shadow manifest and, in the current spike, only prints it. Promotion to the real workspace is a distinct deliberate action, not a side effect of "the skill finished."

### 5. Stable core, replaceable edges

The IR is the stable contract. Parsers, agents, and protocol adapters live at the edges and translate into it. New ingress formats add a parser; they do not edit core types. New step verbs register into `argsRegistry`; they do not edit the hash function or a central dispatch switch. This is how the system absorbs a fast-evolving agent ecosystem without core-level churn.

## Layered Trust Reduction

The pipeline is a sequence of trust-reducing stages. Each stage takes input from a lower-trust region and emits something that the next stage may consume slightly more confidently. Nothing is fully trusted end-to-end.

1. **Protocol ingress** — CLI in [`cmd/main.go`](../cmd/main.go), MCP sidecar in [`cmd/serve.go`](../cmd/serve.go). Future: webhooks, git hooks.
2. **Parser frontends** — [`internal/engine/parser`](../internal/engine/parser). OpenClaw markdown (v0 legacy), V1 JSON (`.loom.json`). Router in [`parser.go`](../internal/engine/parser/parser.go) dispatches by extension. Untrusted external formats become typed `LoomSkill`.
3. **Typed semantic IR** — [`internal/engine/ir.go`](../internal/engine/ir.go). `SchemaVersion`, `StepKind`, `StepArgs`, `Capability{Kind, Scope}`. The canonical logical hash binds execution to audited structure; `SchemaVersion` is written to the hasher first so two otherwise-identical skills under different versions never collide.
4. **Static validator** — [`internal/engine/validator.go`](../internal/engine/validator.go). Dataflow integrity, capability-ceiling enforcement, dangerous-command and SSRF scans (the last two still cover v0 legacy steps and any static content that appears in v1 args).
5. **Input sanitizer** — [`internal/engine/sanitizer.go`](../internal/engine/sanitizer.go). Type coercion, required/default semantics, shell-injection markers, and `SanitizeShadowRelPath` for path scope. The sanitizer is defense-in-depth: the validator's capability-ceiling check usually rejects path escapes first, but the sanitizer is the second line and is never skipped.
6. **Shadow workspace isolation** — [`internal/engine/vfs.go`](../internal/engine/vfs.go). All writes resolve through `ShadowVFS.ResolveWritePath`. Tombstones record deletions. The real workspace is a read-only baseline until the commit gate explicitly promotes bytes.
7. **Compiler / admission controller** — [`internal/engine/compiler.go`](../internal/engine/compiler.go). Orchestrates validate → sanitize → shadow allocation → receipt. The Receipt carries `SchemaVersion`, `LogicalHash`, granted `Capabilities`, and shadow path.
8. **Executor** — [`internal/engine/executor.go`](../internal/engine/executor.go). Types-dispatched handlers for `read_file` and `write_file` (v1 spike). All I/O routes through `ShadowVFS`. Atomic step failure: any error aborts the run and the shadow is never promoted.
9. **Commit gate** — [`internal/engine/commit_gate.go`](../internal/engine/commit_gate.go). Today: reads `ShadowVFS.Manifest()` and prints it. Tomorrow: diff, conflict detection, approval, and the only path to real-workspace mutation.

## Trust Boundaries

Each boundary is where a specific class of failure is contained.

- **B1: External syntax → Internal IR.** Markdown, JSON, or agent text is not execution intent. Only a successfully parsed `LoomSkill` crosses.
- **B2: IR → Admissible skill.** Syntactic validity doesn't imply semantic safety. Structural dataflow, capability ceilings, and static rules must all pass.
- **B3: Raw caller input → Typed runtime values.** `SanitizeInput` refuses undeclared parameters, invalid coercions, and shell markers. `SanitizeShadowRelPath` refuses path escapes and rooted paths.
- **B4: Logical approval → Filesystem side effects.** Sanitized calls still cannot touch the real workspace. `ShadowVFS` is the physical isolation boundary.
- **B5: Shadow completion → Real mutation.** The commit gate owns this gap. In the current spike the gap is uncrossable — promotion is explicitly unimplemented, by design.

## Why This Is Defense-in-Depth

Each layer protects against a different class of failure, and the next layer assumes the previous might fail:

- If the parser misclassifies a fragment, the validator should still reject undeclared dependencies, uncovered capabilities, or static dangerous content.
- If the validator misses a case, the sanitizer should still reject hostile runtime strings and path escapes.
- If sanitization misses a case, `ShadowVFS` should still prevent mutation of the real workspace.
- If execution produces dangerous state, the commit gate should still stop promotion.
- If an attacker finds an admission bypass, the receipt's logical hash should still expose drift after the fact.

No single layer is expected to be perfect.

## The IR Version Contract

The IR has an explicit `SchemaVersion`. `CurrentSchemaVersion = "v1"` is the only version the executor accepts.

- **v0** (empty `SchemaVersion`) — produced by the OpenClaw markdown parser. Instruction text is natural language; no typed Kind can be inferred mechanically. v0 skills are parseable and `verify`-able but are **rejected by the executor** with a clear error. They are not auto-upgraded; semantic upgrade would require inference the parser cannot safely perform.
- **v1** — authored as `.loom.json`. Every Step carries `Kind` + typed `Args`. Every capability carries a scope. Executable end-to-end.

Future versions extend the contract by registering new step kinds in `argsRegistry` and, if the IR shape itself changes, bumping `SchemaVersion` and having parsers upgrade older documents before the IR layer sees them. The IR layer never handles legacy — that is strictly a parser responsibility.

## Current Status (Core Spike)

Delivered:
- v1 typed IR with streaming canonical hash
- Capability model with scope + ceiling enforcement
- Executor for `read_file` / `write_file`, routed through `ShadowVFS`
- Commit gate that prints the shadow manifest (no promotion)
- v1 JSON parser frontend; v0 markdown parser retained for compatibility
- End-to-end integration test that verifies the four sandbox invariants

Deliberately out of scope:
- `os_command` / `http_call` step kinds
- Real commit/promote (shadow → workspace)
- MCP executor integration (sidecar remains interception-only)
- Admission cache keyed on logical hash
- Plugin registries beyond `argsRegistry`

## Roadmap

The roadmap is shaped by what the spike exposed.

### Phase A — Strengthen isolation and promotion
- Deletion semantics in `ShadowVFS` exposed to v1 step kinds
- Shadow manifests used for diff and conflict detection
- Commit gate gains a promotion path with explicit approval

### Phase B — Broaden the executor
- `os_command` with argv-typed args (no shell string), sandbox profile
- `http_call` with URL allowlists composed from capabilities
- All I/O boundaries apply `RedactOutput` mandatorily

### Phase C — Permissions derived, not declared
- Effective capabilities fully derived from Kind + Args where possible; declarations become a narrow-only ceiling
- Split "admission hash" and "execution hash" so runtime-computed content doesn't destabilize pre-admission fingerprints

### Phase D — Operator-grade commit gate
- Shadow diff rendering
- Mutation review with explicit approval
- Audit manifest attached to the Receipt

## Summary

`loom-cli` is a **layered deterministic skill governance plane**:

- multiple ingress forms translating into one typed IR
- multiple pre-execution defense layers with non-redundant responsibilities
- physical isolation between approved intent and real mutation
- auditable admission decisions whose hashes bind behavior to structure

The core is small, stable, and hostile to ambiguity. The edges are where the agent ecosystem's churn is absorbed. That separation is the architectural identity of the project.
