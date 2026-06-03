# Execute an issue directly

Initialize context as in `.zed/prompts/hello.md`.

Then execute issue `#$ARGUMENTS` directly.

Before editing, read all docs needed for the touched area. If broad context is needed, read `docs/**/*.md`; otherwise lazy-load only relevant docs.

Use existing project patterns, keep changes direct, and validate with the most specific relevant `just` command first.
