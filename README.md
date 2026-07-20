# geodrill

A Telegram bot that trains [GeoGuessr](https://www.geoguessr.com/) skills, starting
with **language identification**: it shows you a real sentence and one inline
button per candidate language. You tap your guess; the bot marks it in place
(❌ on a wrong tap, ✅ on the correct answer), then schedules the skill for review
with **FSRS** (the Free Spaced Repetition Scheduler) so you drill exactly the
languages you keep confusing — Romance, CJK, Slavic Latin vs. Cyrillic, Nordic,
SE-Asia.

Sentences come from [Tatoeba](https://tatoeba.org/) (CC-BY). The spaced-repetition
engine is the standalone [`engram`](https://github.com/supercakecrumb/engram)
library; geodrill is the adapters around it (Telegram + PostgreSQL + Tatoeba).

- **Architecture & API contract:** `Projects/wiki/tech/geodrill-architecture.md`
  in the companion wiki (engram public API §3, SQL schema §4, /train flow §5,
  ingest spec §6). That document is the source of truth.

## How it works

- A **skill** ("recognise Portuguese in the Romance group") is the FSRS-tracked
  unit — not a fixed card. Every review samples a fresh sentence for that skill,
  excluding your last ~50 seen, so it never feels rote.
- **Decks** are small confusion groups (4–6 languages). Intervals stretch fast,
  which is correct SRS; sentence variety keeps each review novel.
- Rating policy v1: a wrong tap → `Again`, a correct tap → `Good`.

## Bot commands

| Command | What it does |
|---------|--------------|
| `/start` | Register and open the deck picker. |
| `/decks` | Toggle decks on/off, adjust the daily new-skill cap, toggle reminders. |
| `/train` | Get the next due exercise. Answering edits the keyboard in place and sends the next one. |
| `/practice` | Endless, **unscheduled** practice (does not affect your FSRS schedule). |
| `/stats` | Reviews today/week, accuracy per deck, streak, 7-day due forecast, and your top confusion pairs. |

A daily, timezone-aware reminder ("N reviews due today") is sent to users who
leave reminders on.

## Layout

```
cmd/bot/          wire config → pg pool → engram scheduler → telegram adapter
cmd/ingest/       Tatoeba TSV → filtered sentence pools; seeds decks/skills
internal/telegram/ telebot v4 handlers behind a thin, testable Session interface
internal/train/    engram orchestration: due queue, quiz, grading, scheduling, stats
internal/storage/  pgx/v5 + sqlc; app queries + engram store adapters (engramstore)
internal/content/  Tatoeba download / parse / filter (length, script, dedupe, cap)
internal/config/   env config + slog
migrations/        golang-migrate SQL (also embedded for migrate-on-startup)
seeds/decks.yaml   the six confusion-group decks
```

## Requirements

- **Go 1.26** (Go 1.25.x works too: the module declares `go 1.26` and the
  toolchain auto-downloads `go1.26.x` via `GOTOOLCHAIN=auto`).
- **Docker** (for PostgreSQL 18) — or your own PostgreSQL 18 instance.
- During parallel development, the sibling [`engram`](https://github.com/supercakecrumb/engram)
  module is resolved through the committed **`go.work`** (`use ../../Packages/engram`).
  Build locally with the workspace active (plain `go build`); do not run
  `go mod tidy` while engram is unpublished (it would try to fetch it from the
  network). Once engram is published and tagged, drop `go.work` and
  `go get github.com/supercakecrumb/engram`.

## Configuration

Set via environment (see `.env.example`):

| Var | Required | Default | Notes |
|-----|----------|---------|-------|
| `TELEGRAM_TOKEN` | bot only | — | from [@BotFather](https://t.me/BotFather). |
| `DATABASE_URL` | yes | — | e.g. `postgres://geodrill:geodrill@localhost:5432/geodrill?sslmode=disable` |
| `LOG_LEVEL` | no | `info` | `debug` \| `info` \| `warn` \| `error` (slog). |
| `FSRS_RETENTION` | no | `0.9` | FSRS target retention in `(0,1)`. |
| `SNAGBOX_URL` / `SNAGBOX_INGEST_TOKEN` | no | — | `/feedback` files reports into snagbox; unset → "not available". |
| `GARAGE_S3_ENDPOINT` | no | — | Garage S3 endpoint for city-map images, e.g. `http://garage:3900`. Unset → city maps degrade to text. |
| `GARAGE_S3_REGION` | no | `garage` | S3 region for Garage. |
| `GARAGE_ACCESS_KEY_ID` / `GARAGE_SECRET_ACCESS_KEY` | no | — | Garage credentials (server-side; from OpenBao `kv/apps/geodrill/s3_*`). |
| `GARAGE_BUCKET` | no | `apps-geodrill` | Garage bucket holding `citymaps/<key>.png`. |

### Getting a bot token

1. Open [@BotFather](https://t.me/BotFather) in Telegram, send `/newbot`.
2. Choose a name and a username ending in `bot` (e.g. `geodrill_bot`).
3. Copy the token it gives you into `.env` as `TELEGRAM_TOKEN`.

## Run it

```bash
cp .env.example .env          # then edit TELEGRAM_TOKEN

# 1. start PostgreSQL 18
docker compose up -d db       # (or: docker-compose up -d db)

# 2. migrate + seed decks + ingest a few small languages
#    (bot and ingest both apply migrations on startup, so no separate step)
DATABASE_URL='postgres://geodrill:geodrill@localhost:5432/geodrill?sslmode=disable' \
  go run ./cmd/ingest -langs isl,mkd,khm

# 3. run the bot (long polling — no public URL needed)
export $(grep -v '^#' .env | xargs)     # load .env into your shell
go run ./cmd/bot
```

Then open your bot in Telegram and send `/start`.

### Full ingest (all 30 languages)

```bash
DATABASE_URL='postgres://geodrill:geodrill@localhost:5432/geodrill?sslmode=disable' \
  go run ./cmd/ingest         # every language across all decks in seeds/decks.yaml
```

`cmd/ingest` flags: `-langs spa,por,…` (subset; default = all), `-cap 5000`
(max rows/language), `-min 20 -max 120` (sentence length in runes), `-seed 42`
(deterministic sampling), `-skip-download` (use the gitignored `data/` cache),
`-seed-only` (upsert decks/skills only). Dumps are cached in `data/`.

### Cities map-based topic (data + image pipeline)

The **Cities** topic shows a self-rendered map with a red dot marking a city and asks you to
type the city's name; each newly-discovered city first appears as an info card (country,
region, population, elevation, and — for the biggest cities — a short fact). The maps are
label-free (a label would give away the answer), rendered offline from **Natural Earth**
public-domain vectors, and served from **Garage S3** (read once per image on first send, then
cached as a Telegram `file_id`). All data steps are one-time offline compilations — no runtime
API calls.

Pipeline (each step is idempotent; raw downloads land in the gitignored `data/`):

```bash
# 1. City dataset: GeoNames cities15000 + admin1 -> seeds/cities.yaml
#    (population/tier + lat/lng/region/elevation; committed, derived, CC-BY GeoNames)
./scripts/fetch-cities.sh && go run ./cmd/citygen

# 2. Facts for the biggest cities (tier 0-2) -> seeds/city_facts.yaml
#    (Wikidata match by GeoNames ID + Wikipedia page-summary extracts, CC BY-SA 4.0)
go run ./cmd/cityfacts

# 3. Render the map PNGs offline (no tile servers) -> data/citymaps/
./scripts/fetch-naturalearth.sh && go run ./cmd/citymaps          # -only <key> for one city

# 4. Upload the PNGs to Garage + register them in media_files (needs GARAGE_* env)
go run ./cmd/citymapsync

# 5. (Re)seed the topic — reads media_files to set map_image only for uploaded cities,
#    folds in facts, and (first run over a legacy DB) renames the old city->country topic
#    in place and resets per-user city progress so cities re-introduce biggest-first.
go run ./cmd/ingest -seed-topics
```

> **Operational cutover** (moving a live/dev DB from the old `city-to-country` quiz to the map
> question): `pg_dump` first, then run steps 3→4→5 with Garage configured, and restart the bot
> (as a built binary). `cmd/ingest`'s seed performs the topic rename + progress reset exactly
> once (idempotent — it no-ops once the legacy `cities/city-to-country` path is gone). The
> `user_tier_progress` cache is intentionally left to self-heal on each user's next answer
> rather than recomputed (recomputing would re-lock tiers earned partly through cities). The
> `apps-geodrill` Garage bucket + `kv/apps/geodrill/s3_*` key are provisioned via the platform
> tooling (see the companion wiki's `deployment`/`maintain-infra`).

### Everything in Docker

The `bot` and `ingest` services build from the multi-stage distroless
`Dockerfile`:

```bash
docker compose --profile ingest run --rm ingest -langs isl,mkd,khm
docker compose up -d bot
```

> **Note:** the Docker image builds geodrill as a standalone module, so it
> requires `engram` to be **published** (the `go.work` workspace isn't available
> in the build context). While engram is unpublished, run the bot locally with
> `go run ./cmd/bot` as shown above.

## Development

```bash
go build ./...          # workspace active; auto-downloads the go1.26 toolchain
go vet ./...
gofmt -l .              # should print nothing
go test ./...           # unit tests (integration tests skip without a test DB)
```

Integration tests (storage round-trips, migrate up/down/up, the full train loop)
run only when a throwaway database is provided, and should run serially so they
don't reset the shared schema concurrently.

> ⚠️ **These tests DROP EVERY TABLE (migrate down).** `GEODRILL_TEST_DATABASE_URL`
> must point at a disposable database — **never** the `geodrill` database your
> running bot uses, or you will wipe all decks, content, and user progress. Use a
> dedicated `geodrill_test` database:

```bash
docker compose up -d db
# one-time: create the throwaway test database
docker compose exec db createdb -U geodrill geodrill_test   # (idempotent; ignore "already exists")

GEODRILL_TEST_DATABASE_URL='postgres://geodrill:geodrill@localhost:5432/geodrill_test?sslmode=disable' \
  go test -p 1 ./...
```

### Manual end-to-end (needs a real token)

There is no automated bot E2E (it needs Telegram). To exercise it by hand:

1. `docker compose up -d db` and ingest at least one deck's languages.
2. Put a real `TELEGRAM_TOKEN` in `.env`, `go run ./cmd/bot`.
3. In Telegram: `/start` → enable a deck in the picker → `/train` → answer ~10
   exercises. Verify the tapped-wrong button turns ❌, the correct one ✅, and
   the buttons go inert; that a stale tap on an old message shows an "already
   answered" toast; and that `/stats` reflects your answers and streak.

## License

Sentence content is from Tatoeba under **CC-BY**; each `content_items` row keeps
its `tatoeba#<id>` attribution.
