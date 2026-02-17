# obliviate

An opinionated Go implementation of the Ralph loop pattern for fresh-context task execution.

This project builds on the idea shared by Geoffrey Huntley (Ralph loop), adapted for a pragmatic local CLI workflow.

![Obliviate demo](https://media1.giphy.com/media/v1.Y2lkPTc5MGI3NjExZ3kyMTZoY285djJjeDd6cnZna2FtdXc1Y3lxN2EycTNwanU0M3dldiZlcD12MV9pbnRlcm5hbF9naWZfYnlfaWQmY3Q9Zw/PcfozPlZSzARO/giphy.gif)

## What It Does

- Runs one-task-at-a-time execution loops with fresh agent context.
- Stores canonical task state per instance in `<project>/.obliviate/state/<instance>/tasks.jsonl`.
- Records task runs in `<project>/.obliviate/state/<instance>/runs.jsonl`.
- Appends one-line cycle summaries to `<project>/.obliviate/state/<instance>/cycle.log`.
- Supports optional commit enforcement with `obliviate go --require-commit`.
- Graceful Ctrl+C shutdown: interrupted tasks reset to `todo`, not orphaned as `in_progress`.
- Transient provider failures (rate limits, service unavailable) retry with exponential backoff without burning attempts.
- Per-task locking: the lock is released during agent execution so `status`, `skip`, and `reset` remain usable.

## Core Commands

```powershell
obliviate init <instance> --workdir <project-path>
obliviate add <instance> --title "..." --spec "..." --verify "..."
obliviate go <instance> [--limit N] [--dry-run] [--require-commit] [--agent-timeout 15m] [--cooldown 0s] [--max-attempts 2] [--max-transient-retries 3] [--json]
obliviate status [instance] [--json]
obliviate runs <instance> [--limit N] [--task-id OB-001] [--json]
```

## Loop Semantics

- Tasks move through: `todo -> in_progress -> done|failed|blocked`.
- Stale `in_progress` tasks are recovered to `todo` at the start of each `go` run.
- Verification commands gate completion.
- Failed tasks retry up to `--max-attempts` (default 2) then become `blocked`.
- Transient provider failures (rate limits, service unavailable) retry with exponential backoff (30s, 60s, 120s) up to `--max-transient-retries` (default 3) without incrementing attempts.
- With `--require-commit`, successful runs must create a new Git commit or the task is treated as failed.
- `--cooldown` adds a sleep between tasks to avoid back-to-back agent launches.

## Files

- `main.go`: CLI and loop runtime.
- `main_test.go`: focused unit tests.
- `SKILL.md`: agent-facing operating guide.

## Attribution

Conceptual inspiration: Geoffrey Huntley�s Ralph loop guidance.
Implementation: opinionated Go CLI for local project execution and stateful loops.

