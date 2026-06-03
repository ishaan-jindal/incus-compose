# Zed Agent Notes for incus-compose

This file is a local Zed-oriented conversion of `.claude/settings*.json` and `.claude/commands/*`.

## Context to load first

For a new task, load these files before implementing:

- `.rules`
- `README.md`
- `CONTRIBUTING.md`
- `docs/architecture.md`
- `docs/testing.md`

Then lazy-load additional docs only when the current task touches them, unless the task explicitly asks for all docs.

## Workflow constraints converted from Claude settings

Zed does not use Claude's `permissions.allow` / `permissions.deny` JSON model. Treat those settings as these workflow rules:

- Prefer `just` recipes over raw `go` commands.
- Do not run raw `go build` or `go run`; use `just build`, `just run`, `just run-debug`, or another existing recipe.
- Do not run direct `incus` or `incus-compose` commands unless the user explicitly asks. Use `just incus`, `just run`, or `./bin/incus-compose` after `just build` when appropriate.
- Read-only shell inspection commands are fine: `git --no-pager ...`, `ls`, `cat`, `head`, `tail`, `wc`, `find`, `fd`, `grep`, `rg`, `diff`.
- When using Git from the terminal, include `--no-pager` for read-only commands.
- Keep edits accepted directly; do not ask the user to manually copy patches.

## Extra local directories from Claude settings

Claude had additional local read roots:

- `~/vendor/go/incus/`
- `~/vendor/go/docker/compose/`
- `~/projects/go/bketelsen/incus-compose`

In Zed, use these only if available and relevant. Do not assume they exist.

## Converted prompts

The old Claude commands were converted to local Zed prompt-library style files in `.zed/prompts/`:

- `hello.md`
- `issue-plan.md`
- `issue-direct.md`
- `feedback.md`
