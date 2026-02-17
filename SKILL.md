---
name: obliviate
description: Run a Ralph-style fresh-context task loop for a project. Use when decomposing feature specs into structured tasks, adding tasks in canonical JSONL format, assigning model hints (codex/claude sonnet/opus), and executing tasks one-by-one with fresh agent context, verification gates, status tracking, and learnings capture.
---

# Obliviate Skill

Use `obliviate.exe` as the single writer and runner for task-loop state.

## Canonical workflow

1. `obliviate.exe init <instance> --workdir <path>`
2. Add tasks with `obliviate.exe add` or `obliviate.exe add-batch`
3. Run loop with `obliviate.exe go <instance>`
4. Check progress with `obliviate.exe status [instance]`
5. Inspect/recover via `show`, `runs`, `reset`, `skip`

## Task schema

Each line in `state/<instance>/tasks.jsonl` is one JSON object:

- `id`: string (`OB-001`)
- `title`: string
- `spec`: string
- `verify`: string array of shell commands
- `status`: `todo | in_progress | done | failed | blocked`
- `model_hint`: string (`codex`, `claude-sonnet`, `claude-opus`, etc)
- `priority`: string (`low | med | high`)
- `attempts`: number
- `last_error`: string
- `created_at`: RFC3339 UTC timestamp
- `updated_at`: RFC3339 UTC timestamp

## Batch add input

`add-batch` accepts:

- JSON array of task objects
- JSONL (one task object per line)

For input objects, required fields are `title`, `spec`, and `verify` (string or array of strings).

## Instance state files

- `state/<instance>/prompt.md`: instance runtime prompt/rules
- `state/<instance>/spec.md`: feature source spec
- `state/<instance>/tasks.jsonl`: task queue
- `state/<instance>/learnings.md`: instance learnings
- `state/<instance>/runs.jsonl`: append-only execution log
- `state/<instance>/cycle.log`: one-line summary per `go` cycle
- `state/<instance>/instance.json`: metadata (`workdir`)

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

