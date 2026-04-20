# Migrating OpenClaw Skills into Loom

Loom speaks natively to the typed v1 `.loom.json` format, but most legacy skill assets in the wild are written in OpenClaw-flavored markdown. This document walks through the `loom migrate-openclaw` + `loom accept-migration` pipeline, which is designed as a **safe compiler** for that format gap.

## Why this is a real compiler, not a file copy

Hermes's `hermes claw migrate` command can do its job with `shutil.copytree()` because Hermes's target skill format *equals* OpenClaw's source format — it's really just a location and registration change. Loom's situation is different: OpenClaw skill actions are natural-language (`"Validate ${target_path}"`), and v1 IR requires a closed-enum `StepKind` with typed `Args`. Translating across that gap is a real semantic operation.

Because the translation is semantic and (often) LLM-assisted, every output is treated as a **draft until a human has signed off**. The executor refuses to run unreviewed drafts by default. This is the operational form of the "isolation before mutation" principle we built loom around.

## The pipeline at a glance

```
OpenClaw .md         →  loom migrate-openclaw  →  unreviewed .loom.json
unreviewed           →  human review           →  loom accept-migration
reviewed             →  loom run               →  sandboxed execution
sandbox complete     →  loom commit            →  real workspace
```

Four boundaries, each traceable through the skill's `_provenance` block.

## Step 1: Migrate (preview, then execute)

```bash
# Preview what would happen — no files written
loom migrate-openclaw ~/.openclaw/skills --out ./skills/openclaw-imports

# Actually produce drafts
loom migrate-openclaw ~/.openclaw/skills --out ./skills/openclaw-imports --yes
```

The migrator walks the source directory for `.md` files and classifies each into one of three tiers:

| Tier | Description | Example output |
|---|---|---|
| `mechanical` | Regex matcher recognized a single-verb literal action (`write X to Y`, `read Z`). No LLM call. | `{ "mode": "mechanical", ... }` |
| `llm-assisted` | Action text was ambiguous or multi-step; Claude was asked to produce v1 JSON. Requires `ANTHROPIC_API_KEY`. | `{ "mode": "llm-assisted", "model": "claude-sonnet-4-6", ... }` |
| `stub` | Action text requires a capability loom doesn't support (shell, HTTP) or LLM was unavailable. Empty `execution_dag`. Never executable. | `{ "mode": "stub", "stub_reason": "...", ... }` |

All three modes produce `reviewed: false`. Mechanical is an **efficiency claim** (we saved an LLM call), not a **trust claim**.

### Relevant flags

- `--mechanical-mode=conservative|aggressive|off` — default `conservative`. Conservative only hits single-verb literal patterns; aggressive also tries conditionals and multi-step (not yet implemented).
- `--on-conflict=skip|rename|overwrite` — default `skip`. Overwrite requires `--force-re-migrate-reviewed` to destroy a target that has already been human-reviewed.
- `--no-llm` — even with `ANTHROPIC_API_KEY` set, bypass LLM entirely. Every non-mechanical skill becomes a stub. Useful for air-gapped environments.
- `--yes` — without it, the command is dry-run only.

### Output

A `<out-dir>/_migration-report.json` records per-skill provenance and aggregates a `capability_gaps` map keyed by the step kind the stubbed skills would have needed. This is the machine-readable signal for "what does the community actually want loom to support next?"

## Step 2: Review

Open each generated `.loom.json` and verify:

1. **`execution_dag`** — do the typed steps match what the OpenClaw skill actually did? LLM output is particularly worth double-checking here.
2. **`capabilities`** — are the declared scopes tight enough? The migrator's default scope is "parent directory of path", which is usually too wide. Narrow to the actual file if possible.
3. **`parameters`** — same as source. Usually correct; worth confirming.

The `_provenance` block is informational; you don't need to edit it.

**Do not hand-edit `"reviewed": true`.** The executor recomputes `reviewer_signature` from the canonical body and will refuse skills where the signature doesn't match. Hand-editing loses the audit trail.

## Step 3: Accept

```bash
loom accept-migration ./skills/openclaw-imports/mytool.loom.json \
    --source-root ~/.openclaw/skills
```

This command:

1. Re-reads the original markdown at `_provenance.source_path` (resolved against `--source-root`)
2. Recomputes its sha256 and compares to `_provenance.source_hash`
3. **Refuses** if the source has been edited since migration — you must re-run `loom migrate-openclaw` against the new source
4. **Refuses** stubs — they have no runnable body; they must be rewritten by hand
5. **Refuses** already-reviewed skills — no double-signing
6. Otherwise: sets `Reviewed: true`, stamps `ReviewedAt` and `ReviewerSignature`, writes back atomically

## Step 4: Run

```bash
loom run ./skills/openclaw-imports/mytool.loom.json --input msg=hello
```

From here the flow is identical to a hand-authored v1 skill. The executor verifies `reviewer_signature` on every run — a hand-edit that sets `reviewed: true` without a matching signature fails loudly.

## Emergency escape hatches

| Situation | Escape |
|---|---|
| Need to run an unreviewed draft once (e.g. debugging) | `loom run <path> --accept-draft` |
| Want `warn-then-run` as the default for CI | `export LOOM_DRAFT_POLICY=warn` (or `allow`) |
| Want to re-migrate a reviewed skill to pick up source changes | `loom migrate-openclaw ... --on-conflict=overwrite --force-re-migrate-reviewed` — destroys the human-review audit trail; think carefully |

## What migration cannot do for you

- **Convert shell wrappers.** If an OpenClaw skill's "action" text is really "run this shell command," loom doesn't yet have an `os_command` step kind. The migrator emits a stub with the reason recorded. Check `_migration-report.json.capability_gaps` for the aggregate count — this is input to roadmap prioritization, not autopilot.
- **Generate capabilities you didn't declare.** The migrator mirrors the source skill's declared capabilities into v1 shape. If OpenClaw was sloppy and the skill "really" needed more, the validator will catch it at run time; you'll fix it during review.
- **Guarantee LLM output is correct.** Claude is asked to produce a restricted JSON shape and the response is shape-validated, but reviewing the `execution_dag` and `capabilities` remains the reviewer's job. The `reviewer_signature` exists precisely because we cannot pretend the machine is infallible.

## Troubleshooting

**"reviewer_signature does not match canonical body"**
Someone (possibly you via `sed`) set `"reviewed": true` without running `accept-migration`. Delete the offending reviewed flags and run `accept-migration` properly.

**"stub skills are never executable"**
The migrator couldn't produce a runnable body. Look at `_provenance.stub_reason` and rewrite the `execution_dag` by hand — or wait for loom to support the needed capability.

**"source has changed since migration"**
The original `.md` file was edited after you ran migrate. Re-run `loom migrate-openclaw` against the new source, then re-review.

**Everything is a stub, nothing is mechanical**
Check `--mechanical-mode` (default `conservative` is deliberately narrow) and whether `ANTHROPIC_API_KEY` is set. The conservative matcher only hits single-verb literal actions — this is a feature, not a bug.
