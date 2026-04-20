# Hermes × Loom: Self-Evolution with a Safety Gate

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
- **No human review gate** — if the scan passes, the skill becomes available in the next conversation
- The skill injects into the LLM's context as a user message (not system prompt), preserving prompt caching

### Layer 2: Skill Improvement (Patch / Edit)

Skills can be updated in-place based on experience:

```python
skill_manage(action="patch", name="deseq2-workflow",
             old_content="--num_recycles 3",
             new_content="--num_recycles 5  # better accuracy on multimers")
```

`fuzzy_match.py` handles whitespace/indentation drift. Full rewrites use `action="edit"`. Atomic writes with rollback on scan failure.

### Layer 3: Cross-Session User Modeling (Honcho)

`optional-skills/autonomous-ai-agents/honcho/` adds dialectic multi-turn reflection:
- Observes patterns across sessions
- Tunes recall and summarization
- User model stored at `~/.hermes/memories/USER.md`

---

## How Hermes Calls External Tools

### Built-in Registry

`tools/registry.py` is a singleton. Every `tools/*.py` file calls `registry.register()` at import. `model_tools.py:handle_function_call()` dispatches by name, with parallel execution for independent calls.

### MCP Integration (`tools/mcp_tool.py`)

Hermes is a full MCP client:

```yaml
# ~/.hermes/config.yaml
mcp_servers:
  loom:
    command: "docker"
    args: ["run", "--rm", "-i", "-v", "./skills:/loom/skills:ro",
           "timoxue/loom", "serve", "--port", "8080"]
    timeout: 120
```

On startup, Hermes connects to each configured MCP server, calls `initialize` + `tools/list`, and merges discovered tools into its registry alongside built-in tools. When the LLM selects a loom tool, Hermes calls `tools/call` and feeds the result back as a tool message.

**This is the working integration point today.** Any skill in loom's `--skills-dir` becomes a tool available to Hermes's LLM.

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

Hermes owns the *intelligence* layer. Loom owns the *execution safety* layer. Neither replaces the other.

### What Each Layer Owns

| Concern | Owner |
|---|---|
| What task to do, how to plan | Hermes |
| Which skill to invoke | Hermes |
| Is this skill invocation safe to execute? | **Loom** |
| What did this invocation actually change? | **Loom** |
| Should those changes land in the real workspace? | **Loom + human** |
| Should we record this for compliance? | **Loom** (Receipt) |
| Learn from this task for next time | Hermes (skill patch) |

---

## The Current Gap: Skill Creation Bypasses Loom

Hermes's `skill_manage(action="create")` writes directly to `~/.hermes/skills/` — no loom involvement. The only gate is `skills_guard.py`'s security scan.

**Why this matters:** A skill is code. A skill that says "write patient data to `/tmp/export.csv` then `curl` it out" would pass a text-scan but is exactly what loom's capability model is designed to stop. Today, Hermes's self-evolution loop can produce skills that, when later executed, have no capability bounds.

```
Hermes creates skill → skills_guard scan → ~/.hermes/skills/  ← no loom
Later: Hermes invokes skill → loom MCP → typed IR + capability check  ← loom
```

The gap is between *creation* and *execution*. Loom catches the problem at execution time, but only if the skill was authored as a `.loom.json`. If it stays as a `SKILL.md` context document, loom never sees the individual operations.

---

## Integration Paths

### Path 1 (Today, Working)

Loom runs as an MCP server. Hermes connects via `mcp_servers` config. Skills in loom's `--skills-dir` are exposed as tools. Hermes-generated skills that have been migrated and accepted by `loom accept-migration` are fully governed.

```
hermes claw migrate          # import OpenClaw skills to ~/.hermes/skills/
loom migrate-openclaw        # translate to typed v1 .loom.json (unreviewed)
loom accept-migration        # human signs off
loom serve --skills-dir ./   # expose as MCP tools
hermes → loom MCP → governed execution
```

### Path 2 (Near Term)

Hermes creates a new skill → instead of writing to `~/.hermes/skills/` directly, it calls `loom migrate-openclaw` on the generated content → loom produces an unreviewed draft → human runs `loom accept-migration` → skill becomes available. Self-evolution is preserved; execution safety is added.

This requires configuring `skill_manage`'s write target to point at a directory that loom watches, or implementing a thin adapter that wraps `skill_manage` output as a loom MCP call.

### Path 3 (Future)

`skill_context` step kind (see `docs/loose-parser-design.md`): Hermes skills that are pure LLM context documents (not file-operation recipes) run through loom as `skill_context` steps. Every SOP consultation is an auditable Receipt event — required in regulated environments.

---

## `hermes claw migrate` vs `loom migrate-openclaw`

These two commands look similar but operate at different layers:

| Command | What it does | Output | Safety gate |
|---|---|---|---|
| `hermes claw migrate` | Copy `SKILL.md` files to `~/.hermes/skills/openclaw-imports/` | `SKILL.md` (unchanged) | `skills_guard.py` text scan |
| `loom migrate-openclaw` | Translate `SKILL.md` → typed v1 `.loom.json` | Unreviewed `.loom.json` | Parse → validate → `accept-migration` human sign-off |

They are complementary. Run `hermes claw migrate` to import skills into Hermes's context system. Run `loom migrate-openclaw` to compile the same skills into governed, executable IR that loom can enforce capability bounds on.

---

## Practical Quickstart: Hermes → Loom

```bash
# 1. Start loom MCP sidecar
docker run -d -p 8080:8080 \
  -v ./skills:/loom/skills:ro \
  -v ./audit-log:/home/loom/.loom \
  timoxue/loom serve --skills-dir /loom/skills --port 8080

# 2. Configure Hermes to use loom
cat >> ~/.hermes/config.yaml << 'EOF'
mcp_servers:
  loom:
    url: "http://localhost:8080/v1/mcp"
    timeout: 120
EOF

# 3. Hermes now sees loom skills as tools
hermes "run the deseq2-workflow skill with input file=data.csv"
# → Hermes selects loom tool, calls tools/call
# → Loom: parse → validate → ShadowVFS → Receipt
# → Hermes sees manifest in tool result
# → User runs: loom commit <session-id> --yes
```

---

## OpenClaw Medical Skills and `allowed-tools`

The 869 skills in OpenClaw-Medical-Skills have a key frontmatter field: `allowed-tools`. This is Hermes's capability declaration — it tells the harness which tools the LLM is permitted to call while using this skill.

```yaml
allowed-tools: Read, Edit, Write, Bash, WebFetch, WebSearch
```

Loom's loose parser (see `docs/loose-parser-design.md`) maps this directly to loom capability declarations:

| `allowed-tools` value | Loom capability | Notes |
|---|---|---|
| `Read`, `Glob`, `Grep` | `vfs.read: /` | Scope narrowed during human review |
| `Write`, `Edit` | `vfs.write: /` | Scope narrowed during human review |
| `Bash` | STUB: `os_command` | Capability gap — skill becomes stub |
| `WebFetch`, `WebSearch` | STUB: `http_call` | Capability gap |
| `Task` | STUB: `agent_call` | Not yet in loom |

343 of 869 skills have `allowed-tools` → 39% can be mechanically converted to loom capability declarations without LLM assistance.
