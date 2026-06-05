# Domain Docs

How engineering-oriented agent skills should consume this repo's domain documentation when exploring the codebase.

## Before exploring, read these

- `CONTEXT.md` at the repo root when it exists
- `docs/adr/` when it exists

If either does not exist, proceed silently. Do not flag the absence as an error and do not suggest creating these files unless the current task specifically calls for domain-documentation work.

## File structure

This repo is configured as a single-context repo.

Expected layout:

```text
/
├── CONTEXT.md
├── docs/adr/
└── ...
```

## Use the glossary's vocabulary

When domain terminology is defined in `CONTEXT.md`, prefer that vocabulary in issue writeups, refactor proposals, hypotheses, and tests.

If `CONTEXT.md` does not yet exist, do not invent a glossary on the fly. Use existing repo terminology and note true vocabulary gaps only when they materially affect the task.

## Flag ADR conflicts

If a proposed change conflicts with an ADR in `docs/adr/`, surface that conflict explicitly instead of silently overriding it.
