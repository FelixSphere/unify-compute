# Contributing

This repo is worked on by both humans and multiple AI coding agents. Project
conventions live in one place — [AGENTS.md](AGENTS.md) — and every agent
config file points to it, so start there regardless of which tool you use.

## Setup

```bash
npm ci                      # site
(cd compute-api && go build ./...)   # service, Go 1.24+
cp deploy/compose/.env.example deploy/compose/.env   # then fill in secrets
```

## Workflow

1. Branch off `main`: `<type>/<short-description>`.
2. Keep the change scoped to one thing.
3. Run the hand-off checks listed at the end of AGENTS.md.
4. Open a PR against `main` using the template. CI must pass before merge.

## Deployment

Merging does not deploy. See "Who may deploy" in AGENTS.md and the procedure
in `.claude/agents/cicd.md`.
