# Hermes × Loom: The Teacher-Student Model

## One-Line Summary

> **Hermes lets the AI know what to do. Loom ensures the AI can only do what it said it would do, and leaves proof.**

The core pattern: a capable model (the **teacher**) figures out how to do a task once, loom records the verified execution as typed IR, and a cheaper model (the **student**) runs that IR reliably forever — bounded, auditable, cheap.

---

## What Hermes Is

Hermes is an AI agent harness. Its job is orchestration: building system prompts, managing memory, dispatching tool calls, and persisting conversation history. Skills (OpenClaw-format `SKILL.md` files) are its unit of procedural knowledge — they tell the agent *how to approach a class of task*.

Key entry points:
- `run_agent.py` — Core `AIAgent` loop: system prompt assembly → LLM call → tool dispatch → loop
- `tools/skill_manager_tool.py` — Agent-driven skill lifecycle (create / edit / patch)
- `tools/mcp_tool.py` — MCP client integration (1050+ lines; stdio + HTTP transports)
- `hermes_cli/claw.py` — `hermes claw migrate` OpenClaw import

---

## Hermes's Self-Evolution Mechanism

Hermes can improve itself across sessions. The mechanism has three layers:

### Layer 1: Skill Creation (Procedural Memory)

When the agent successfully completes a novel task, it can permanently encode that knowledge:

```python
skill_manage(action="create", name="deseq2-workflow", category="bioinformatics",
             content="---\nname: deseq2-workflow\n...\n---\n## How to run\n...")
```

- Writes `~/.hermes/skills/bioinformatics/deseq2-workflow/SKILL.md`
- Runs `skills_guard.py` security scan immediately after write
- **No human review gate** — if the scan passes, the skill is live in the next conversation
- The skill injects into the LLM's context as a user message, preserving prompt caching

### Layer 2: Skill Improvement (Patch / Edit)

Skills can be updated in-place based on experience:

```python
skill_manage(action="patch", name="deseq2-workflow",
             old_content="--num_recycles 3",
             new_content="--num_recycles 5  # better accuracy on multimers")
```

`fuzzy_match.py` handles whitespace/indentation drift. Atomic writes with rollback on scan failure.

### Layer 3: Cross-Session User Modeling (Honcho)

`optional-skills/autonomous-ai-agents/honcho/` adds dialectic multi-turn reflection across sessions. User model stored at `~/.hermes/memories/USER.md`.

---

## Where Loom Fits

### The Stack

```
┌──────────────────────────────────────────────────────┐
│  Hermes (harness)                                     │
│  Planning · Memory · Skill creation · Tool dispatch   │
├──────────────────────────────────────────────────────┤
│  Loom (execution gate)          ← YOU ARE HERE        │
│  Typed IR · Capability sandbox · ShadowVFS · Receipt  │
├──────────────────────────────────────────────────────┤
│  Real filesystem / real workspace                     │
└──────────────────────────────────────────────────────┘
```

### What Each Layer Owns

| Concern | Owner |
|---|---|
| What task to do, how to plan | Hermes |
| Which skill to invoke | Hermes |
| Is this skill invocation safe to execute? | **Loom** |
| What did this invocation actually change? | **Loom** |
| Should those changes land in the real workspace? | **Loom + human** |
| Compliance audit record | **Loom** (Receipt) |
| Learn from this task for next time | Hermes (skill patch) |

---

## The Teacher-Student Pattern

This is loom's primary value proposition for Hermes users — not "safety gate" but **cost reduction and consistency**.

### The Problem

Hermes users run the same workflows repeatedly:
- Every run costs Opus-level tokens
- Results vary run to run (LLM non-determinism)
- Team members can't reliably reuse each other's workflows

### The Solution

```
Teacher (Claude Opus + Hermes)
  → Executes task once inside ShadowVFS
  → loom --record captures every step as typed IR
  → Human reviews: converts literals to ${params}, confirms scope
  → loom accept-migration signs the .loom.json

Student (Claude Haiku / any cheap model)
  → No reasoning needed — just fills parameters
  → loom run skill.loom.json --input patient=xxx
  → Executes the fixed IR deterministically
  → Receipt written automatically
```

**Why side effects are eliminated:** The student model has no decision authority. It executes a pre-validated, human-reviewed DAG. The capability bounds come from observed teacher execution, not inference.

**Economics:** Teacher runs once (expensive, exploratory). Student runs N times (cheap, deterministic). Loom is the bridge that makes the student's run trustworthy.

### The One Hard Problem: Parameterization

When the teacher executes, loom records literals:
```
write_file(path="out/report_patient_001.txt", content="BRCA1 analysis...")
```

Before the student can reuse this, a human must mark which values are parameters:
```
write_file(path="out/report_${patient_id}.txt", content="${gene} analysis...")
```

This review step is not automatable — it is the human's judgment call about what varies between runs. The `loom accept-migration` gate is exactly where this happens.

---

## Side Effects: What Loom Governs and What It Doesn't

Loom's positioning is **tools + guardrails**. It cannot and should not try to intercept every agent action.

| Side effect | Loom today | Rationale |
|---|---|---|
| Filesystem writes | ✅ ShadowVFS + commit gate | Core loom territory |
| Unauthorized reads | ✅ Capability scope | Core loom territory |
| Skill creation/edit | ❌ Bypasses loom | Hermes's domain — self-evolution is its value |
| Memory modification | ❌ Bypasses loom | Hermes's domain |
| Shell execution | ⚠️ Stub (gap recorded) | Out of scope until `os_command` is built |
| HTTP calls | ⚠️ Stub (gap recorded) | Out of scope until `http_call` is built |
| Agent delegation | ❌ Bypasses loom | Harness concern |

**The clean line:** Hermes evolves freely (skill creation, memory, planning). Loom intercepts exactly when execution intent touches the real filesystem.

Hermes's skill creation bypasses loom by design — the `skills_guard.py` text scan is Hermes's gate. Loom's gate fires later, at execution time, when that skill's actions would touch real data.

---

## Integration Paths

### Path 1 (Today, Working)

One config line connects Hermes to loom:

```yaml
# ~/.hermes/config.yaml
mcp_servers:
  loom:
    url: "http://localhost:8080/v1/mcp"
    timeout: 120
```

Skills in loom's `--skills-dir` appear as tools in Hermes's registry. Zero other changes needed. Users who want zero friction set `LOOM_DRAFT_POLICY=allow`. Regulated deployments use `refuse`.

### Path 2 (Recording Mode, Near Term)

Teacher executes → loom records → student reuses:

```bash
# Teacher: execute with recording
loom run skill.md --record --output recorded.loom.json

# Human reviews: add ${params}, confirm scope
loom accept-migration recorded.loom.json

# Student: run the recorded IR
loom run recorded.loom.json --input patient=xxx
```

### Path 3 (Context Skills, Future)

Skills that are pure LLM context documents (not file-operation recipes) run through loom as `skill_context` steps. Every SOP consultation becomes an auditable Receipt event — mandatory in regulated environments.

---

## `hermes claw migrate` vs `loom migrate-openclaw`

| Command | What it does | Output | Gate |
|---|---|---|---|
| `hermes claw migrate` | Copy `SKILL.md` → `~/.hermes/skills/` | `SKILL.md` unchanged | `skills_guard.py` text scan |
| `loom migrate-openclaw` | Translate `SKILL.md` → typed v1 `.loom.json` | Unreviewed draft | Parse → validate → human sign-off |

Run both. `hermes claw migrate` imports skills into Hermes's context system. `loom migrate-openclaw` compiles the same skills into governed IR that loom enforces capability bounds on.

---

## OpenClaw Medical Skills: `allowed-tools` Is Already a Capability Declaration

869 skills in OpenClaw-Medical-Skills carry `allowed-tools` in frontmatter — Hermes's own capability declaration:

```yaml
allowed-tools: Read, Edit, Write, Bash, WebFetch, WebSearch
```

Loom's loose parser maps this directly, zero NLP:

| `allowed-tools` | Loom capability | Notes |
|---|---|---|
| `Read`, `Glob`, `Grep` | `vfs.read: /` | Reviewer narrows scope |
| `Write`, `Edit` | `vfs.write: /` | Reviewer narrows scope |
| `Bash` | STUB: `os_command` | Capability gap |
| `WebFetch`, `WebSearch` | STUB: `http_call` | Capability gap |
| `Task` | STUB: `agent_call` | Not yet in loom |

343 of 869 skills have `allowed-tools` → **39% mechanically convertible** to loom capability declarations, no LLM needed.

---

## Quickstart

```bash
# 1. Start loom
docker run -d -p 8080:8080 \
  -v ./skills:/loom/skills:ro \
  -v ./audit-log:/home/loom/.loom \
  timoxue/loom serve --skills-dir /loom/skills --port 8080

# 2. Connect Hermes
echo "mcp_servers:\n  loom:\n    url: http://localhost:8080/v1/mcp" >> ~/.hermes/config.yaml

# 3. Run — Hermes selects loom tool, executes in shadow, returns manifest
hermes "run deseq2-workflow with input file=data.csv"
# → loom commit <session-id> --yes   (when ready to promote)
```
