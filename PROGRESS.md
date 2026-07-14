# geodrill — build progress (Agent B, Phases 3–6)

Status: **complete and green.** The app compiles against the real `engram`
(resolved via `go.work`), `go vet` and `gofmt` are clean, all unit tests pass,
and the integration suite passes against a dockerized PostgreSQL 18. No bot
token exists, so live Telegram E2E is deferred to Aurora (manual steps in the
README).

Repo: `/Users/val-kiel/Personal/PersonalProjectsMD/PersonalProjects/geodrill`
(local git only — no remote, per instructions). Module
`github.com/supercakecrumb/geodrill`, `go 1.26`.

## What was built

| Phase | Area | Result |
|-------|------|--------|
| 3 | Scaffold, compose, migrations, sqlc, storage adapters | done |
| 4 | Tatoeba ingest + seeds | done, verified against real dumps |
| 5 | Bot: /start /train /decks /stats + reminders | done, handler logic unit-tested |
| 6 | Dockerfile, README, /practice mode | done |

Package map:

- `cmd/bot` — wires env config → pgx pool → engram scheduler → telegram; applies
  migrations on startup; graceful shutdown via `signal.NotifyContext`.
- `cmd/ingest` — per-language Tatoeba pipeline + deck/skill seeding.
- `internal/config` — env (`TELEGRAM_TOKEN`, `DATABASE_URL`, `LOG_LEVEL`,
  optional `FSRS_RETENTION`) + slog.
- `internal/content` — download/parse/filter (length 20–120 runes → script check
  → dedupe → seeded cap 5000) + `seeds/decks.yaml` loader.
- `internal/storage` — pgx/v5 + sqlc (`internal/storage/db`); engram-free `Store`
  with clean app types; app queries (users, deck toggles, single-use exercise
  guard, content sampling excluding last-50, stats aggregations).
- `internal/storage/engramstore` — per-user adapters implementing
  `engram.SkillStore` + `engram.ReviewStore` (contract §3).
- `internal/train` — the engram orchestration: due-queue build, `quiz.Generate`,
  single-use grading, FSRS reschedule + full review append, `/practice`,
  `/stats` view model. Pure callback-parse + keyboard-mark helpers.
- `internal/telegram` — telebot v4 handlers behind a thin, testable `Session`
  interface; deck picker; tz-aware daily reminder goroutine.
- `migrations` — golang-migrate SQL (contract §4 verbatim) + `//go:embed` for
  migrate-on-startup.

## Key decisions (and why)

- **Go toolchain:** kept `go 1.26`. Go 1.25.3 is installed but `GOTOOLCHAIN=auto`
  auto-downloads `go1.26.x` (cached locally, builds are fast). No fallback to
  1.25 was needed.
- **engram dependency via `go.work` only, not a `go.mod` require.** A versioned
  `require github.com/supercakecrumb/engram` forces network resolution of the
  unpublished module and breaks every `go` command. The committed `go.work`
  (`use ../../Packages/engram`) makes it importable without a require. A NOTE in
  `go.mod` documents this. Consequence: **`go mod tidy` must not be run** until
  engram is published (it would try to fetch engram). Deps are managed with
  `go get` instead; the `// indirect` markers on a couple of transitive deps are
  therefore cosmetic.
- **sqlc** (v1.31.1, `pgx/v5`, `google/uuid` overrides) generates `internal/storage/db`;
  a hand-written `Store` maps the generated pgtype-heavy rows to clean app types
  so the rest of the codebase never touches pgtype.
- **engramstore is a separate package** (not folded into `storage`) so
  `internal/storage` stays engram-free and its app-query integration tests can
  run without engram.
- **Review write path.** `engram.ReviewStore.Append` only carries FSRS fields
  (no answer keys/content), but the `reviews` table merges `engram.Review` +
  `quiz.Attempt`. So `/train` persists full rows via `storage.Store.InsertReview`
  directly; the `engramstore` adapter still implements `Append`/`Log` faithfully
  for contract compliance (and `Log` is used for stats).
- **Grading** uses `engram.RatingForAnswer(chosen == correctKey)` (identical
  policy to `quiz.Exercise.Grade`), with the correct key read from the target
  skill — avoids threading option `SkillID`s through the persisted options JSON.
- **/practice** reuses the exercise/guard machinery but with a `prac:` callback
  prefix; answering grades + edits the keyboard but records **no** review and
  does **not** reschedule (verified by test).
- **Single-use guard** is an atomic `UPDATE exercises SET answered_at=$now WHERE
  id=$1 AND answered_at IS NULL RETURNING id`; a no-row result = stale tap →
  "already answered" toast.
- **Telegram testability:** handlers are written against a small `Session`
  interface plus narrow `trainer`/`userStore` interfaces, so the grading/render
  flow is unit-tested with fakes (no token, DB, or network). The real telebot
  adapter sets only `Text`+`Data` on inline buttons (no `Unique`) so callback
  data round-trips verbatim for `train.ParseCallback`.
- **Reminders** dedupe per user-local day in memory (no schema column for
  last-reminded); documented, acceptable for v1.
- **postgres:18 volume:** mounted at `/var/lib/postgresql` (not `.../data`) —
  pg18 changed the data-dir convention and the old path errors on boot.
- **Docker image** builds geodrill as a standalone module (`GOWORK=off`), so it
  only builds once engram is published; noted in the Dockerfile and README.
  During parallel dev, run the bot with `go run ./cmd/bot` (workspace active).

## engram integration status

**Compiles and passes end-to-end against the real engram.** Agent A finished
engram during this run; its public API (checked via `go doc`) matches contract
§3 exactly (`Scheduler`, `NextDue`, `CountNewIntroduced`, `Accuracy`/`Streak`/
`DueForecast`, `SkillStore`/`ReviewStore`, `quiz.Generate`/`Grade`/`Confusion`).
The `internal/train` end-to-end test drives NextExercise → Answer → reschedule →
review-append → /stats → /practice against real engram + PG18 and passes.

## Test summary

`go build ./...`, `go vet ./...`, `gofmt -l .` — all clean.

Unit tests (`go test ./...`):
```
ok  	.../internal/content
ok  	.../internal/storage       (integration tests skip w/o GEODRILL_TEST_DATABASE_URL)
ok  	.../internal/telegram
ok  	.../internal/train
```

Integration suite against dockerized PG18 (`go test -p 1 -count=1 ./...` with
`GEODRILL_TEST_DATABASE_URL` set):
```
ok  	.../internal/content   0.415s
ok  	.../internal/storage   0.459s   (migrate up/down/up; store + engram-adapter round-trips; single-use guard)
ok  	.../internal/telegram  0.335s
ok  	.../internal/train     0.439s   (full train loop vs real engram + PG18)
```

Ingest verified against **real Tatoeba dumps** (small languages isl, mkd, khm):
```
lang  candidates  pool_size  status
isl   5000        5000       ok      (capped)
khm   1156        1156       ok
mkd   5000        5000       ok      (capped)
```
- **Idempotent:** re-running (`-skip-download`) leaves pool sizes identical.
- **Script purity** (SQL over `content_items`): isl 5000/5000 Latin, 0 Cyrillic;
  khm 1156/1156 Khmer, 0 Latin; **mkd 5000/5000 Cyrillic, 0 Latin** — the
  srp-style Cyrillic-only filter works.

Docker was available; containers were stopped and the volume removed after
verification.

## Known gaps / what needs Aurora

- **Bot token.** No `TELEGRAM_TOKEN` exists, so there was no live Telegram run.
  Create a bot via @BotFather, put the token in `.env`, and follow the manual
  E2E in the README (`/start` → enable a deck → `/train` → answer ~10 → `/stats`).
- **GitHub remotes.** Local repos only, per instructions. When you publish
  `engram` and tag it, drop `geodrill/go.work`, add
  `require github.com/supercakecrumb/engram <tag>`, and then the Docker image and
  `go mod tidy` work normally.
- **Commit hygiene note.** A repo-wide `git add -A` during the parallel build
  swept the telegram implementation files into the commit labeled "Add train
  unit + end-to-end integration tests" (`3de5bb5`). Everything is present and
  correct; only the label is slightly off. History was left intact rather than
  rewritten.
- **Phase 7 (Mini App)** is out of scope for this run, as planned.
