# Domain Docs

How the engineering skills should consume this repo's domain documentation when exploring the codebase.

## Before exploring, read these

- **[docs/CONTEXT.md](../CONTEXT.md)**: the glossary. In this repo it lives under `docs/`, not the root, and CLAUDE.md declares it binding.
- **[docs/adr/](../adr/)**: read the ADRs that touch the area you're about to work in.

This is a single-context repo. There is no `CONTEXT-MAP.md` and no per-package context.

## File structure

```
/
├── docs/
│   ├── CONTEXT.md            <- binding glossary
│   ├── adr/                  <- fourteen decisions and the options they beat
│   ├── features/             <- one requirements.md per capability
│   ├── PRD.md
│   └── BUILD-ORDER.md
├── main.go                   <- repo root, per ADR-0011
└── internal/
```

## Use the glossary's vocabulary

When your output names a domain concept (in an issue title, a refactor proposal, a hypothesis, a test name), use the term as defined in `docs/CONTEXT.md`. Don't drift to synonyms the glossary explicitly avoids: **Purge**, never "bulk-delete". **Cache** means a GitHub Actions Cache; our on-disk store is **local-store**. **Run**, **Workflow**, **Job**, **Step**, **Artifact** and **Attempt** carry their GitHub Actions meanings and no other.

If the concept you need isn't in the glossary yet, that's a signal: either you're inventing language the project doesn't use (reconsider) or there's a real gap (note it for `/domain-modeling`).

## Flag ADR conflicts

If your output contradicts an existing ADR, surface it explicitly rather than silently overriding:

> _Contradicts ADR-0007 (adaptive delete throttle), but worth reopening because..._

Amending an ADR in place to close a risk is established practice here (see CLAUDE.md). If you resolve a risk, update the PRD's risk table in the same commit.
