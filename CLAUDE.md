# geodrill

Telegram bot ([@geodriller_bot](https://t.me/geodriller_bot)) that trains GeoGuessr skills with FSRS spaced repetition. Owner: Aurora (`supercakecrumb`). Co-developed with the engram SRS library (`../../Packages/engram`) via the committed `go.work`.

## Git workflow

- Remote: `https://github.com/supercakecrumb/geodrill` (**public**), branch `master`.
- **One atomic commit per logical change, pushed immediately.** Never batch unrelated changes into one commit. Every commit must build and keep tests green on its own.
- **changie** manages the changelog: add a `changie new` fragment in the same commit for any user-visible change; `changie batch` + `changie merge` at release tags. Docs/CI-meta changes don't need fragments.
- Never rewrite pushed history.

## Hard safety rules

- **Public repo — no secrets, ever.** `.env` and `*.log` are gitignored; keep it that way. Never commit tokens, DB dumps, or logs. Scan staged files before every push.
- **Do NOT run `go mod tidy`** while co-developing unreleased engram changes — engram resolves via the committed `go.work`. Manage deps with `go get`. (Pinning a published engram version and dropping `go.work` is a deliberate, Aurora-approved step, not routine.)
- **Test databases only:** any integration-test DB URL must contain "test" (`testDSN` has a code fuse). Tests/migrations against the live dev DB wiped it once (2026-07-15) — never again. `pg_dump` the dev DB before any destructive schema work.

## Build & run

- Build: `go build ./...`. Run as a single built binary `./bin/geodrill-bot` — never `go run` (stale children double-poll Telegram). Verify: exactly one process in `pgrep`, "bot starting" in the log.
- Tests: `go test ./...`; integration: `GEODRILL_TEST_DATABASE_URL=... go test -p 1 ./...`.
- Lint: `golangci-lint run`.

## CI (being introduced with v2)

GitHub Actions in `.github/workflows/`: lint, test, build on every push; release on tag → container to `ghcr.io/supercakecrumb/geodrill` + GitHub Release with changie notes. **Validate workflow changes locally with `act` before pushing.** Deploy workflows: not yet — deployment method TBD (direction: self-hosted PaaS pulling GHCR images).

## Context

- Plans/design docs live in `vibe/`. Current: `vibe/geodrill-orchestrator-brief.md` — the multi-topic v2 restructure brief.
- Knowledge base: Obsidian vault at `../../Projects/wiki/` (read `hot.md` first, then the geodrill page).
- Build history: `PROGRESS.md`.
