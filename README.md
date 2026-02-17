# obliviate

An opinionated Go implementation of the Ralph loop pattern for fresh-context task execution.

This project builds on the idea shared by Geoffrey Huntley (Ralph loop), adapted for a pragmatic local CLI workflow.

## What It Does

- Runs one-task-at-a-time execution loops with fresh agent context.
- Stores canonical task state per instance in `<project>/.obliviate/state/<instance>/tasks.jsonl`.
- Records task runs in `<project>/.obliviate/state/<instance>/runs.jsonl`.
- Appends one-line cycle summaries to `<project>/.obliviate/state/<instance>/cycle.log`.
- Supports optional commit enforcement with `obliviate go --require-commit`.

## Core Commands

```powershell
obliviate init <instance> --workdir <project-path>
obliviate add <instance> --title "..." --spec "..." --verify "..."
obliviate go <instance> [--limit N] [--dry-run] [--require-commit] [--json]
obliviate status [instance] [--json]
obliviate runs <instance> [--limit N] [--task-id OB-001] [--json]
```

## Loop Semantics

- Tasks move through: `todo -> in_progress -> done|failed|blocked`.
- Verification commands gate completion.
- Failed tasks retry up to `maxAttempts` then become `blocked`.
- With `--require-commit`, successful runs must create a new Git commit or the task is treated as failed.

## Files

- `main.go`: CLI and loop runtime.
- `main_test.go`: focused unit tests.
- `SKILL.md`: agent-facing operating guide.

## Attribution

Conceptual inspiration: Geoffrey Huntley’s Ralph loop guidance.
Implementation: opinionated Go CLI for local project execution and stateful loops.
