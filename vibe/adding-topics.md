# Adding a new topic — playbook & status

How to add a quiz topic to geodrill, distilled from the four topics already live. Follow this and a topic lands as two or three commits with no surprises. Status and backlog at the bottom.

## The pattern

A topic is: **a seed dataset** (`seeds/<topic>.yaml`) + **a package** (`internal/topics/<name>/`) + **two wiring lines** (`cmd/ingest`, `cmd/bot`). Nothing else changes — the framework (`internal/topics/registry.go`, `internal/study`, `internal/telegram`) never needs edits for a new topic.

### 1. Seed dataset — `seeds/<topic>.yaml`

- One YAML file, flat list of entries. Keep the format dumb enough that a cheap agent can compile it.
- **Anti-fabrication is the house rule**: only claims you can defend; omit when unsure. If claims are checkable against the ingested Tatoeba corpus (letters, words), an audit test will prune failures later — but conservative beats pruned.
- Assign `tier` per the rubric (see `vibe/geodrill-architecture.md` §4): 0 universally known … 4 advanced/rare, 5 reserved for plonkit-style meta. Country-linked topics compute tier from the country instead (rubric: tier 0 = {US,GB,FR,DE,JP,CA,AU,IT,ES}, 1 = rest of G20, 2 = UN member with GG coverage, 3 = UN member without, 4 = everything else) — that logic currently lives unexported in `internal/topics/roadside`; extract it to a shared `internal/topics/countrytier` package the first time a second topic needs it.

### 2. Package — `internal/topics/<name>/`

Three files plus tests, mirroring `internal/topics/roadside/` (simplest) or `specialchars/` (multi-mode):

- **`generator.go`** — implement `topics.Generator`:
  - `Kind() string` — the `quiz_kind` string; must match what `seed.go` writes on the topic row. The registry dispatches on this; no code anywhere switches on slugs.
  - `BuildExercise(ctx, rng, topics.ExerciseRequest) (topics.Exercise, error)` — deterministic given the injected rng. Fill `Options` (single-choice), `OptionSets` (set-choice), or `Accept` (free text) per mode; always set `CorrectAnswer`. Distractors come from `req.Siblings` (same-topic items), same-script/same-region where that matters.
  - `BuildIntro(ctx, item) (topics.IntroCard, error)` — the teaching card ("🚗 🇯🇵 Japan drives on the LEFT.").
  - Constructor is `New(...)` with narrow injected interfaces (e.g. guesslang's `ContentSampler`). **Prefer no DB access at all**: cache flag/name/answer into `items.payload` at seed time (roadside does this) so the generator is pure.
  - Tips are optional: implement `topics.TipProvider` (returns a `quiz.TipProvider`) — see guesslang.
- **`seed.go`** — `Seed(ctx, store *storage.Store) error`, idempotent (pure upserts, safe to re-run):
  - Upsert the topic path: parent containers first (`languages`, `roads`, …), then the quizzable topic (slug, name, `quiz_kind`, `base_tier`, `exercise_modes`, `is_quizzable`, `config`).
  - Upsert items: `key` unique within the topic (prefix with language/country when spellings collide, e.g. `pol:ulica`), `label`, `tier` (or NULL to inherit `base_tier`), `payload` jsonb, `country_id` when country-linked, `position` = file order.
  - Country data goes through the fact store: `fact_defs` + `country_facts` are the source of truth; the item payload only caches. Consistency is asserted in tests.
- **`intro.go`** — if the intro text deserves its own file.
- **Tests**:
  - Unit: option composition, determinism (same seed ⇒ same output), malformed payload, intro text.
  - Seed integration: env-gated on `GEODRILL_TEST_DATABASE_URL`, copy the `testDSN` fuse (DB name must contain "test") + fresh-schema pattern from an existing topic package. Assert counts vs the yaml, spot checks, fact/payload consistency.
  - Corpus audit (when claims are checkable): env-gated on `<TOPIC>_AUDIT_DATABASE_URL`, **SELECT-only**, run against the live corpus DB; fail listing violators; prune the yaml until clean. Precedent: `internal/topics/specialchars/audit_test.go`, `words/audit_test.go`.

### 3. Wiring (two lines)

- `cmd/ingest/seed_topics.go` — add the package's `Seed` to the seed-topics run.
- `cmd/bot/main.go` — `topics.Register(<pkg>.New(...))`.

### 4. Ship it

```
./scripts/pre-commit.sh        # the ONLY gate: fmt, build, vet, unit+integration, lint, changie, secrets scan
git commit / git push          # author Aurora, no AI trailers, changie fragment in the same commit
./bin/ingest -seed-topics      # seed the dev DB (idempotent)
pkill -f geodrill-bot && go build -o bin/geodrill-bot ./cmd/bot && <restart>   # single process, clean log
```

Don't watch GitHub Actions; the local gate is the gate. Never `go mod tidy` casually (engram is a pinned published dep now, but tidy is still not a reflex).

### Media topics (flags and beyond)

Photo questions are **photo messages from birth** (Telegram can't edit text→photo): send photo + caption + keyboard, edit caption/markup in place. `media_files` stores `local_path`/`sha256`/`telegram_file_id` (cache the file_id after first upload). Two gaps to close with the first media topic (see `vibe/design-flags-quiz.md`): a media path on answer results (schema stores only `is_media` today), and image sourcing — **downloading an image set needs Aurora's explicit OK first**. The flags design doc includes an emoji-only fallback mode that needs no downloads.

## What's done (as of 2026-07-19)

- Framework: topic tree, introduction-before-review lifecycle, tiers 0–5 with global gating, typed country facts, single migration `000001_init`, `scripts/pre-commit.sh` gates, CI in both repos.
- Releases: engram **v0.3.0**, geodrill **v0.1.0** (+ `ghcr.io/supercakecrumb/geodrill:v0.1.0`); engram pinned as a normal module.
- Topics live (fresh DB: 14 topics, 411 items, ~148k sentences): guess-the-language (8 groups / 41 languages, Romance tips), special characters (39 items, corpus-audited), road sides (252 countries), common words (79 items, corpus-audited).
- Design docs ready in `vibe/`: flags, country profiles, TLDs, cities, rivers & mountains, plonkit topics (+ working scraper prototype `cmd/plonkit`).
- Datasets committed, awaiting implementation: `seeds/tlds.yaml` (248 entries), `seeds/country_profiles.yaml` (247 entries).

## What's next (easiest → hardest)

1. **TLD topic** — data ready; extract `countrytier`, build `internal/topics/tld/` per `vibe/design-tlds.md` (both directions as sibling topics), wire, seed.
2. **Country profiles topic** — data ready; facts `languages_spoken`/`main_religion`/`region`, country→language quiz per `vibe/design-country-profiles.md`; uses `countrytier`.
3. **Enrich existing datasets** — special chars is thin (39; Vietnamese/Icelandic long tail untapped) and words lean (79); extend + re-run the corpus audits.
4. **Flags** — emoji mode first (no downloads); real images after Aurora approves a public-domain set; includes the media answer-path schema addition.
5. **Rivers & mountains**, then **cities** — per their design docs.
6. **plonkit tier-5 topics** (bollards, poles, plates…) — blocked until scrape scope is reconfirmed with the site developer (robots.txt is default-deny).

Ops still pending: rotate the Telegram bot token.
