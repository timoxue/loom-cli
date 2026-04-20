# CLAUDE.md

This file guides Claude Code when working on loom-cli. It captures the load-bearing decisions that aren't obvious from reading the code.

## What loom is (and is not)

**Loom is the secure Tools-and-Guardrails runtime for production agent harnesses.** It occupies one specific layer of the Harness stack — Tools + Guardrails + auditable execution — and intentionally does nothing else.

**Loom is NOT:**
- An agent framework (no orchestration loop, no multi-agent support)
- A memory system (harnesses own their own memory)
- A prompt-construction layer (harnesses own that)
- A generalized sandbox (it's specifically a *typed-IR skill* sandbox)

When you're tempted to add features that look "agent-like," stop. Every harness-layer feature loom grows is a line of code that will rot when the harness ecosystem evolves. Loom's value is being the **stable, focused, composable** piece that every harness can plug into.

## Positioning Lock (2026-04-20)

This section is the output of a week of stress-testing loom against four adjacent concepts: agent harness (Claude Code / LangGraph), knowledge marketplace (EvoMap), microkernel / OS-level security (seccomp / WASM), and industry compliance engine (HIPAA / HITRUST). The conclusions below survived every stress test. **Do not revise this section without a plan-mode discussion. The real danger is not wrong positioning — it is positioning drift.**

### The one sentence (internal truth, don't expose verbatim)

**Loom is the deterministic gate between an AI agent's intent and its side effects** — a typed-IR + capability-sandbox admission layer, and a shadow-VFS + human-commit promotion layer.

### The four-character product principle: 先拦，后录

`Block before. Record after.` This is the feature decision filter. Every proposed addition must clearly strengthen one or the other; if it strengthens neither, reject it as out-of-scope (it belongs to harness / marketplace / network-gateway layers instead).

| Proposal | Verdict |
|---|---|
| Automated verification loops (run tests before commit gate promotes) | ✅ strengthens **拦** |
| Richer `execution_history` on Receipt (EvolutionEvent-style process metadata) | ✅ strengthens **录** |
| Cross-session skill usage analytics | ❌ belongs to marketplace |
| Multi-step planning / memory / context compression | ❌ belongs to harness |
| HTTP rate limiting / auth | ❌ belongs to network gateway |
| "Call an LLM" as a step kind | ❌ belongs to harness (Prompt Construction layer) |

### The four NEVER-SAY red lines

Each of these, if spoken, dilutes the core positioning. Avoid in README, pitch, marketing, and casual conversation with stakeholders:

| ❌ Never say | Why |
|---|---|
| "AI 时代的微内核" (AI-era microkernel) | Overreaches. Loom is not an OS; it's closer to seccomp than to Mach/L4. |
| "取代 Kong/Nginx" (Replaces Kong/Nginx) | Wrong category. Kong is a network gateway (traffic, auth, routing); loom is an intent gateway (typed tool calls, capability, sandbox). Adjacent, not competing. |
| "最后一道防线" (The last line of defense) | Compliance officers filter this out as marketing language. Loom is one layer in a stack, not the final bulwark. |
| "通用 AI 治理平台" (Universal AI governance platform) | Too abstract. Loom is specifically the Tools+Guardrails layer — not orchestration, not memory, not marketplace. |

### Audience-specific phrasing (same thing, different words)

The core meaning must stay identical across all audiences. Only the words change.

| Audience | Phrasing |
|---|---|
| **Self / team (internal)** | "Loom 是 AI agent 意图到副作用之间的确定性闸门" — never use verbatim externally; too abstract |
| **Public tagline (README, site)** | "AI agent 的执行审计闸门 — 先拦，后录" / "Execution audit gate for AI agent actions — block before, record after" |
| **Engineers (docs, blog)** | "Typed IR + capability-bounded sandbox for agent tool calls" |
| **CTO / architect** | "Last-mile execution gate — forces LLM intent through type check + sandbox + human commit before any real side effect" |
| **Security / compliance officer** | "AI agent 的执行审计闸门。和堡垒机的关系是 — 堡垒机管人，我们管 AI." (actively name 堡垒机 to neutralize the category collision) |
| **Investor** | Do not pitch at day-N < 60. Build 3 real PoCs first, then craft from real adoption data. |

### The real threat model (not obvious from the positioning)

Loom's existential risk is **NOT** being absorbed by agent harnesses (modeled as "less likely" since harnesses are getting thinner, not thicker — Anthropic is actively deleting rule steps from Claude Code's harness). The real threat is **being absorbed by the layer below**:

1. Linux kernel native `CAP_AI_*` support via BPF
2. WASM runtimes (Wasmtime / WasmEdge) gaining capability-based security primitives
3. Cloud vendors shipping native agent guardrails (AWS Bedrock Agent Guardrails, etc.)
4. Kubernetes Pod Security Standards + admission webhooks covering loom's niche

**Mitigation strategy**: design IR + Capability schema + Receipt format as potential public standards, not as loom-proprietary internals. If absorption happens, loom wants to be the spec that gets adopted — not the implementation that gets replaced.

## Architecture in one minute

Nine trust-reducing stages from untrusted external intent to real-workspace mutation:

```
Agent → Parser → IR → Validator → Sanitizer → Shadow VFS
                                                  ↓
                                              Executor
                                                  ↓
                                        Commit Gate (human-gated)
                                                  ↓
                                         Real workspace
```

Each layer assumes the previous layer can fail. Five load-bearing principles:

1. **Typed ambiguity becomes security ambiguity** — no `any`, no `map[string]interface{}` in the IR core. `StepKind` + `StepArgs` interface with per-kind typed structs.
2. **Capabilities carry scope** — `Capability{Kind, Scope}`, declared capabilities are an upper bound the validator enforces.
3. **Admission before execution** — the logical hash + input digest are computed and a Receipt is written *before* any step dispatches.
4. **Isolation before mutation** — all writes go through `ShadowVFS`; the real workspace is read-only until commit gate explicitly promotes.
5. **Stable core, replaceable edges** — IR is the stable contract; parsers/agents/protocols are the replaceable edges.

Full rationale: [docs/architecture.md](docs/architecture.md) (EN) and [docs/architecture.zh-CN.md](docs/architecture.zh-CN.md).

## Where to make changes

### Adding a new step kind
- [internal/engine/ir.go](internal/engine/ir.go) — define `Args` struct, implement `stepKind()`, `writeCanonical()`, `substituteInputs()`. Add to `argsRegistry`.
- [internal/engine/executor.go](internal/engine/executor.go) — add a handler, dispatch case.
- Capability derivation: `DefaultCapabilitiesFor` in [internal/engine/ir.go](internal/engine/ir.go).
- Canonical hash: your `writeCanonical` must prefix its bytes with the Kind tag.

### Adding a new parser frontend
- Create a file in [internal/engine/parser/](internal/engine/parser/), implement `Parse(raw []byte) (*engine.LoomSkill, error)`.
- Wire into `ParseFile` router by file extension.
- v0 output uses `StepKindLegacy` + `LegacyStepArgs`; v1 uses the typed kinds.

### Adding a new validator rule
- [internal/engine/validator.go](internal/engine/validator.go). Branch on `skill.SchemaVersion` if the rule is v1-specific.
- Capability-ceiling enforcement is the primary gate for path-escape and scope violations — use that before falling back to string scanning.

### Adding a new CLI subcommand
- [cmd/main.go](cmd/main.go) — add to `newRootCmd()`.
- Follow the pattern: flag parsing, delegate to `internal/` package, print success/error.
- Do not put business logic in `cmd/`; it's a thin wrapper.

## Conventions that are NOT negotiable

- **All writes go through `ShadowVFS`.** No direct `os.WriteFile` into the workspace from the executor, ever.
- **Commit is human-gated.** Only `cmd.newCommitCmd` + `--yes` invokes `ShadowVFS.Commit()`. The MCP sidecar and the executor never promote bytes.
- **Provenance `reviewed: false` is the only safe default** for migrator output. `loom run` refuses unreviewed drafts unless `--accept-draft` or `LOOM_DRAFT_POLICY=allow`.
- **`Description` and `Provenance` are excluded from `GetLogicalHash`** — they're metadata, not behavior. Tests enforce this invariant.
- **`ReviewerSignature` uses `CanonicalBodyHash`** which excludes `Reviewed`, `ReviewedAt`, and the signature itself. This is how we detect hand-edited "reviewed: true" flags.
- **Receipt cache layout is flat**: `~/.loom/cache/<session>/receipt.json`. O(1) lookup by session id.

## Testing discipline

- Every trust boundary has a negative fixture that pins which layer the rejection happens in. See [cmd/fixtures_test.go](cmd/fixtures_test.go). Tests assert the *stage* (parse / validate / execute), not just that an error occurred — because "rejected somewhere later" silently weakens the architecture.
- All tests run with `go test -count=1 ./...` from the repo root. No network, no API key required.
- Executor tests use real `ShadowVFS` + `Compiler.CompileAndSetup` against `t.TempDir()` — not mocks. Shadow directory actually exists and gets populated.
- LLM integration (migrator/llm.go) is tested via a `fixedLLM` stub (see [internal/migrator/migrator_test.go](internal/migrator/migrator_test.go)) — no API key needed for CI.

When adding tests, prefer:
1. Table-driven negative cases that pin a specific boundary
2. Integration tests using real `Compiler` + `Executor` + `ShadowVFS`
3. Assertions on exact error substrings when the message is the UX contract (`"reviewed"`, `"capability"`, `"stub"`, `"not executable"`)

## Running loom

```bash
# Verify a skill compiles
loom verify test_skills/demo_cleaner.md

# Execute v1 skill into shadow
loom run test_skills/templated_write.loom.json --input msg=world

# Review manifest, then promote
loom commit <session-id> --yes

# Start MCP sidecar (for agents)
loom serve --skills-dir test_skills --port 8080

# Migrate OpenClaw skills (produces unreviewed drafts)
loom migrate-openclaw ~/.openclaw/skills --out ./skills/openclaw-imports --yes

# Accept a migrated skill after review
loom accept-migration ./skills/openclaw-imports/foo.loom.json --source-root ~/.openclaw/skills
```

## Current roadmap context (as of 2026-04)

Completed:
- Core Spike (v1 typed IR + shadow executor + commit gate manifest)
- Phase A+C (parameter substitution + real commit gate + cache flattening)
- Phase B-Forced-A (MCP sidecar wiring, tools/list, tools/call, toy Python agent)
- OpenClaw migration purifier (3-tier classifier + provenance signatures)

Open:
- **Loose parser**: real OpenClaw skills don't conform to the strict `## Parameters / ## Permissions / ## Instructions` dialect. 896 of 896 medical skills failed to parse in reality check. Loose mode would preserve unknown body content as stub material.
- **MCP `initialize` handshake**: needed for Hermes's production MCP client to connect. ~20 lines of Go.
- **Automated verification loops**: the commit gate today is human-only. Skills should be able to declare `verification: [...]` steps that run in shadow before promote — test suite, linter, policy check. This maps to Anthropic's "give the model rule-based feedback" recommendation.
- **Execution history on Receipt**: today's `Provenance` records only how a skill came to exist (`mechanical` / `llm-assisted` / `stub`). EvoMap's `EvolutionEvent` shape captures richer process data — `mutations_tried`, `total_cycles`, failure counts, outcome scores across retries. Adopting that structure for loom's Receipt would make the audit trail feed knowledge-marketplace formats without transformation, and would give reputation/learning layers above loom something substantial to work with. Status: shaped, not built. Cost: ~50 lines on [internal/engine/compiler.go](internal/engine/compiler.go) + tests pinning "execution_history does not affect logical_hash". This is pure metadata enrichment — it must NOT turn loom into a marketplace client; it just makes loom's Receipt more nutritious for whatever layer sits above it.
- **`http_call` step kind**: shaped but not built; SSRF surface needs careful design.
- **`os_command` step kind**: deferred — argv vs shell, env isolation, signal handling are all load-bearing decisions that need real skill pressure first.

**Not on roadmap (deliberately):**
- `llm_call` step kind — "call an LLM" belongs in the harness's Prompt Construction layer, not in loom's tools runtime. Harnesses that want to call LLMs already have that machinery.
- Memory / context management — harnesses own this.
- Orchestration — harnesses own this.

## Two things to re-read before large changes

1. [docs/architecture.zh-CN.md](docs/architecture.zh-CN.md) — the five core design principles. If your change violates one, think twice.
2. [internal/engine/ir.go](internal/engine/ir.go) — the IR is the stable contract. Breaking changes here propagate through parsers, validator, sanitizer, executor, compiler, and commit gate all at once.

## When in doubt

### Before changing product scope or public messaging, pass these three checks

1. **Does today's code support this framing?** If not, it's a vision, not positioning. Visions belong in roadmap notes, not in README / pitch / tagline.
2. **Is this consistent with every public statement made in the past 30 days?** If not, you are drifting, not evolving. Drift is fatal for infra products because the only moat is accumulated trust in a stable narrative.
3. **Does this framing require new work, or is it "same code, new words"?** If the latter, that's narrative inflation. Reject it — it dilutes the positioning without delivering value.

### Before adding a feature, run it through the 先拦/后录 filter

See the Positioning Lock section above. Any feature must clearly strengthen either admission (拦) or audit (录). If it does neither, it belongs in a layer above (harness, marketplace) or adjacent (network gateway). Push it there instead of absorbing it into loom.

### Code-review red flags

- **Agent-like feature creep**: if adding a feature feels like it's making loom more agent-like (memory, planning, LLM-as-worker), stop — that's harness territory
- **Hash canonicalization touch**: changing `GetLogicalHash` or `CanonicalBodyHash` is a de-facto audit-contract change — require a version-bump discussion before merging
- **Typed-IR erosion**: adding `any` or `map[string]interface{}` to the core IR is the most common architectural slip. Revisit the plan file if you find yourself doing this
- **Marketplace protocol drift**: if a PR adds `publish` / `subscribe` / `reputation` / remote networking into loom core, push back — that's a marketplace adapter that belongs outside loom
