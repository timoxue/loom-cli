# loom

> The secure Tools-and-Guardrails runtime for production AI agent harnesses.

Loom sits at one specific layer of the agent stack: it turns untrusted execution intent into capability-bounded, sandboxed, auditable filesystem changes, and refuses to promote them to the real workspace without explicit human sign-off. It is **not** an agent framework, **not** a memory system, **not** a prompt-construction layer — it is the execution substrate those systems can plug into.

**Status**: working prototype, day 7. v1 typed IR is stable. MCP sidecar and OpenClaw migration path are usable but expect rough edges.

---

## Why

AI agents now have hands. Cursor writes your code, Claude Code edits your configs, Devin opens your PRs. Two extremes fail equally badly:

- **"Trust the agent"** — one prompt injection or one hallucinated `rm -rf` and your workspace is gone.
- **"Lock the agent in Docker"** — the sandbox is too coarse, has no admission concept, no audit trail, no "this step OK but that step not."

Loom fills the gap with five load-bearing principles, enforced by code, not policy:

1. **Typed ambiguity becomes security ambiguity.** The IR carries no `any` or `map[string]interface{}`; every step is a typed tagged union with closed-enum kinds.
2. **Capabilities carry scope.** `vfs.write` alone is unbounded; `vfs.write` + `out/` is a verifiable claim the validator enforces against every step's args.
3. **Admission before execution.** A receipt with a canonical logical hash is written before any step dispatches.
4. **Isolation before mutation.** All writes resolve through `ShadowVFS` into a disposable tree. The real workspace is read-only until a human explicitly promotes.
5. **Stable core, replaceable edges.** The IR is the stable contract; parsers, agents, and protocols sit at the edges.

Full architecture: [docs/architecture.md](docs/architecture.md) (EN) / [docs/architecture.zh-CN.md](docs/architecture.zh-CN.md) (中文).

---

## Quick start

```bash
# Build
go build -o loom ./cmd

# Execute a v1 skill into the shadow workspace
./loom run test_skills/templated_write.loom.json --input msg=world

# Review the manifest, then promote
./loom commit <session-id> --yes

# Or: start the MCP sidecar so agents can reach it
./loom serve --skills-dir test_skills --port 8080
```

Run `./loom --help` for the full command list. Individual subcommand help lives behind `./loom <cmd> --help`.

---

## Loom and the knowledge-marketplace ecosystem

A question that comes up frequently is whether loom should become a skill marketplace — a place where agents publish validated fixes and discover each other's solutions, in the style of [EvoMap](https://evomap.ai), Hugging Face Skills, or similar consensus-driven knowledge networks.

**The answer is no, and the reason is positioning.** A marketplace and a runtime live on different floors of the stack:

| Layer | Concern | Example |
|---|---|---|
| Knowledge marketplace | "What skill should I use for this problem?" | EvoMap's Capsule + reputation + bounty economy |
| Agent harness | "How do I plan, remember, call tools, verify?" | Claude Code, OpenAI Agents SDK, LangGraph, Hermes |
| **Tools + Guardrails runtime** | **"Is this tool call safe to execute, and can it be audited?"** | **loom** |
| Operating system | "Can this process write this file?" | Linux kernel, seccomp, capability OSes |

Loom occupies exactly one row. Agent harnesses sit above and orchestrate. Knowledge marketplaces sit above *those* and trade validated artifacts across agents. Each layer should own its concerns and compose cleanly with the others.

### What this means concretely

- **Loom does not publish, fetch, or consume skills from any network**. It reads and writes `.loom.json` files on the local filesystem, end of list.
- **Loom does not run a reputation system, a bounty economy, or agent-to-agent protocols**. Those are marketplace concerns.
- **Loom's Receipt format is designed to feed marketplaces, not to be one**. Every execution produces a structured `Receipt` carrying the skill's logical hash, input digest, granted capabilities, and shadow manifest. A marketplace-layer tool can trivially wrap this into its own "proof-of-execution" format (EvoMap's `EvolutionEvent`, for example) without loom knowing anything about that marketplace.
- **A thin outbound `loom publish` command may land later** — one that serializes a Receipt into a target marketplace's wire format and POSTs it. That's an *export adapter*, not a protocol rewrite. Loom remains marketplace-agnostic.

### Why this separation matters

Marketplaces optimize for **discovery and reputation**: "which skill has been validated by many agents?"

Loom optimizes for **execution safety**: "does this skill only do what it says on the tin, can I see what it changed, and can I veto before it lands?"

These are complementary but independent bets. A marketplace without a runtime is a bag of JSON nobody will trust to execute. A runtime without a marketplace is still useful for every team that has its own skill library. Collapsing them would make loom worse at both jobs and wed it to one marketplace's economic assumptions.

### How to compose

```
┌─ Knowledge marketplace (EvoMap, internal registry, git repo of skills...)
│     ↓  fetch a validated skill as .loom.json
├─ Agent harness (Claude Code, OpenAI Agents, Hermes, your own...)
│     ↓  tool call over MCP, or direct loom run
├─ loom   ← YOU ARE HERE
│     ↓  Receipt, manifest, audit trail
└─ marketplace again, for proof-of-execution / EvolutionEvent publish
```

If you want loom to be a **drop-in execution substrate** for a marketplace product you're building, read [docs/architecture.md](docs/architecture.md) and [CLAUDE.md](CLAUDE.md). The Receipt struct in [internal/engine/compiler.go](internal/engine/compiler.go) is the contract your integration should consume.

---

## Layout

- [internal/engine/](internal/engine/) — IR, validator, sanitizer, executor, ShadowVFS, compiler, commit gate
- [internal/engine/parser/](internal/engine/parser/) — v0 OpenClaw markdown parser + v1 JSON parser
- [internal/migrator/](internal/migrator/) — OpenClaw → v1 migration tool with provenance-signed review
- [cmd/](cmd/) — CLI subcommands (`verify`, `run`, `commit`, `serve`, `migrate-openclaw`, `accept-migration`)
- [test_skills/](test_skills/) — reference v1 skills + negative fixtures pinned to trust boundaries
- [toys/](toys/) — 100-line Python probe that drives loom via MCP from a real Claude session
- [docs/](docs/) — architecture (EN + 中文), migration guide, blog-journey

## Testing

```bash
go test -count=1 ./...
```

The fixture matrix in [cmd/fixtures_test.go](cmd/fixtures_test.go) pins every negative case to the specific layer it must be caught at — parse, validate, sanitize, or execute. A regression that lets (say) a path escape slip past the validator and be caught later by the sanitizer fails the test even though both would still protect the workspace. That discipline is the point.

## Contributing

If you're thinking about touching the IR or the executor, read [CLAUDE.md](CLAUDE.md) first — it captures the conventions that are not negotiable (e.g. `any` is banned from the core, commit is human-gated, Description and Provenance are excluded from the logical hash).

## License

TBD. This is early-stage code; pick an appropriate OSS license before first external use.
