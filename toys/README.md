# Toy Agent

A ~100-line probe that speaks MCP to a local `loom serve` instance and lets
Claude drive. Its only job is to stress-test the gateway end-to-end: does a
real agent discover loom's skills, pick the right one, produce valid
arguments, and recover from loom's errors when it doesn't?

This script is **not** a product. It has no retries, no pretty UI, no
persistence. It exists to surface the ugly parts of the integration so they
get fixed in `cmd/serve.go`.

## Setup

```bash
pip install anthropic
export ANTHROPIC_API_KEY=sk-ant-...
```

Build loom and start the sidecar in one terminal:

```bash
go build ./cmd && ./cmd.exe serve --skills-dir test_skills
# (on macOS/Linux: ./cmd serve --skills-dir test_skills)
```

## Run

In a second terminal, from the repo root:

```bash
python toys/agent.py "please write hello world to out/greeting.txt"
```

Expected output shape (abridged):

```
[toy] loom exposes 3 tool(s): demo_cleaner, reject_path_escape, templated_write

[toy] --- turn 1 ---
[claude] I'll use the templated_write tool to write "hello world" to out/greeting.txt.
[toy] dispatching tool_use to loom: templated_write({'msg': 'world'})
[loom:OK] Executed skill "templated_write" in a sandboxed shadow workspace.
Session: verify-1776568980332667800-abcd1234
Pending changes:
  write  out/greeting.txt
Real workspace is unchanged. To promote, the user must run:
  loom commit verify-1776568980332667800-abcd1234 --yes

[toy] --- turn 2 ---
[claude] Done. The skill ran successfully. To actually apply the changes
to your workspace, please run:
  loom commit verify-1776568980332667800-abcd1234 --yes

SUCCESS: To promote the shadow changes to your real workspace, run:
  loom commit verify-1776568980332667800-abcd1234 --yes
```

Then, after reviewing the manifest, run the commit command the script
printed. That is the **only** path from shadow to real workspace — the
agent has no way to trigger it itself.

## What this probe is for

Three questions the script answers that code review cannot:

1. **Does Claude pick the right tool from descriptions?** If descriptions
   are empty or vague, Claude guesses. The `LoomSkill.Description` field
   exists because this probe proved it necessary.
2. **Does Claude produce well-formed arguments?** We emit JSON Schema via
   `tools/list`; whether Claude honors it reliably is a model-behavior
   question that changes with model versions.
3. **Can Claude recover from loom's error messages?** Run with a vague
   prompt that doesn't name a target file:

   ```bash
   python toys/agent.py "write something useful somewhere"
   ```

   Over up to 3 turns, Claude may ask clarifying questions, retry with
   different arguments, or correctly give up. Whatever it does is the
   answer to whether our error messages are agent-friendly — capture a
   trace here when you notice something interesting.

## Environment overrides

- `LOOM_URL` — defaults to `http://127.0.0.1:8080/v1/mcp`.
- `LOOM_TOY_MODEL` — defaults to `claude-sonnet-4-6`. Change to try other
  models (e.g. `claude-opus-4-7` for comparison).

## Out of scope

- CI integration (the script needs a real API key).
- Multi-tool orchestration across skills.
- Any form of persistence or session management.
- Auto-commit. Ever. Commit is the mutation boundary; only a human on the
  host runs `loom commit`.
