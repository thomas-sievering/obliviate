---
name: obliviate
description: Run a Ralph-style fresh-context task loop for a project. Use when decomposing feature specs into structured tasks, adding tasks in canonical JSONL format, assigning model hints (codex/claude sonnet/opus), and executing tasks one-by-one with fresh agent context, verification gates, status tracking, and learnings capture.
---

# Obliviate Skill

Use `obliviate.exe` as the single writer and runner for task-loop state.

## Tool Resolution Rules

- Always execute `./obliviate.exe` first (sibling of this SKILL.md).
- Only fall back to `obliviate.exe` from `PATH` if the skill-local path is missing.
- Do not use recursive workspace search to discover binaries when the skill-local path is known.
- If multiple copies exist, prefer the skill-local copy and report the chosen path before running commands.

## Milestone sizing

Before decomposing work into tasks, ask the user how big each task should be. Use one of these tiers:

| Tier | Scope per task | Typical agent time | When to use |
|---|---|---|---|
| **Standard** | One focused feature slice (a few files + tests) | 10-30 min | Quick iterative work, tight feedback loops |
| **Large** (default) | A full feature or subsystem across many files | 30-60 min | Overnight or long unattended runs |
| **Full send** | Entire spec crammed into as few tasks as possible | 60+ min | Maximum unattended runtime, fire-and-forget |

**Default is Large.** If the user doesn't specify, decompose into large tasks.

When asking, phrase it as: *"How chunky should each task be? Standard (10-30 min each), Large (30-60 min, default), or Full send (60+ min, fewest tasks)?"*

Apply the chosen tier when writing `spec` fields: larger tiers should have more detailed specs with multiple sub-objectives per task, while Standard should be tightly scoped to one concern.

## Canonical workflow

1. `obliviate.exe init <instance> --workdir <path>`
2. Ask the user about milestone sizing (see above)
3. Add tasks with `obliviate.exe add` or `obliviate.exe add-batch`
4. **Hand off** `go` to the user (see below)
5. Check progress with `obliviate.exe status [instance]`
6. Inspect/recover via `show`, `runs`, `reset`, `skip`

## Running the loop — DO NOT run `go` inside an LLM agent

**Your job as the orchestrating agent stops after adding tasks.** Do NOT execute `obliviate.exe go` yourself. Instead, give the user the ready-to-paste command and tell them to run it in a separate terminal:

```
obliviate.exe go <instance> --agent-timeout 20m --cooldown 10s --json
```

Why:
- `go` is a long-running standalone process (often hours). LLM agents will timeout, cancel, or waste tokens polling.
- The binary handles its own retries, backoff, and graceful shutdown (Ctrl+C). No wrapper needed.
- Running inside an agent risks orphaning in_progress tasks if the agent session dies.

After handing off, you can still help the user check status, inspect runs, skip/reset tasks, or add more tasks. Just don't run the loop itself.

## Task schema

Each line in `.obliviate/state/<instance>/tasks.jsonl` is one JSON object:

- `id`: string (`OB-001`)
- `title`: string
- `spec`: string
- `verify`: string array of shell commands
- `status`: `todo | in_progress | done | failed | blocked`
- `model_hint`: string, **required** (`codex`, `claude-sonnet`, `claude-opus`, etc)
- `priority`: string (`low | med | high`)
- `attempts`: number
- `last_error`: string
- `created_at`: RFC3339 UTC timestamp
- `updated_at`: RFC3339 UTC timestamp

## Batch add input

`add-batch` accepts:

- JSON array of task objects
- JSONL (one task object per line)

For input objects, required fields are `title`, `spec`, `verify` (string or array of strings), and `model_hint`.

## Instance state files

- `.obliviate/state/<instance>/prompt.md`: instance runtime prompt/rules
- `.obliviate/state/<instance>/spec.md`: feature source spec
- `.obliviate/state/<instance>/tasks.jsonl`: task queue
- `.obliviate/state/<instance>/learnings.md`: instance learnings
- `.obliviate/state/<instance>/runs.jsonl`: append-only execution log
- `.obliviate/state/<instance>/cycle.log`: one-line summary per `go` cycle
- `.obliviate/state/<instance>/instance.json`: metadata (`workdir`)

## Operational commands

- `obliviate.exe show <instance> <task-id> [--json]`
- `obliviate.exe runs <instance> [--limit N] [--task-id OB-001] [--json]`
- `obliviate.exe reset <instance> <task-id> [--json]`
- `obliviate.exe skip <instance> <task-id> [--reason "..."] [--json]`

## Execution model

`obliviate.exe go` builds each task prompt from:

- `SKILL.md`
- `prompt.md`
- `spec.md`
- current task JSON
- global + instance learnings

Then it spawns a fresh non-interactive agent process for that task, runs verify gates, and updates task status.





