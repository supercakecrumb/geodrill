# Adding a new topic — playbook & status

How to add a quiz topic to geodrill. Since the engine refactor (2026-07-19) a topic is **a descriptor plus data**, not a package of copy-pasted Go: the generic generator and seeder live in `internal/topics/engine/`, and a topic package supplies only what is genuinely its own. Status and backlog at the bottom.

## The pattern

A topic is: **a seed dataset** (`seeds/<topic>.yaml`) + **a small package** (`internal/topics/<name>/`) declaring an `engine.Descriptor` + **two wiring lines** (`cmd/ingest`, `cmd/bot`). The framework (`internal/topics/registry.go`, `internal/study`, `internal/telegram`) and the engine never need edits for a new topic.

### 1. Seed dataset — `seeds/<topic>.yaml`

- One YAML file, flat list of entries. Keep the format dumb enough that a cheap agent can compile it.
- **Anti-fabrication is the house rule**: only claims you can defend; omit when unsure. If claims are checkable against the ingested Tatoeba corpus (letters, words), an audit test will prune failures later — but conservative beats pruned.
- Assign `tier` per the rubric (see `vibe/geodrill-architecture.md` §4): 0 universally known … 4 advanced/rare, 5 reserved for plonkit-style meta. **Country-linked topics don't hand-assign tiers at all**: link items via `ItemSeed.CountryISO`, set `SeedData.TierFromCountry`, and the seeder applies the shared rubric in `internal/topics/countrytier` (tier 0 = {US,GB,FR,DE,JP,CA,AU,IT,ES}, 1 = rest of G20, 2 = UN member with GG coverage, 3 = UN member without, 4 = everything else).

### 2. Package — `internal/topics/<name>/`

Two files plus tests. Read `words/` first (the smallest full port: descriptor-only, ~no custom code), then `roadside/` (fixed options + countries/facts), then `specialchars/` (custom set mode) for escalating examples.

- **`generator.go`** — the descriptor and its `Parse` func:
  - `parseCard(raw []byte) (engine.Card, error)` — decode + validate your payload struct, then map it to a `Card`: `Keys` (answer key(s); >1 keys = set-shaped item), `Group` (distractor-compatibility group), `Subject` (the string slotted into prompt templates), `Intro` (fully rendered intro-card text). Keep your own malformed-payload sentinel; the engine propagates it.
  - `var descriptor = engine.Descriptor{...}` — quiz kind, topic path (`Topic` nodes, parents first), `Parse`, `Labels` (key → display label; missing keys fall back to `ToUpper`), `PromptSingle`/`PromptText` (fmt templates with one `%s` = `Subject`), and either `Distractors` (sampled MCQ: `Max` cap + `SameGroup`) or `FixedOptions` (static, never-shuffled option list, correct = `Keys[0]`).
  - `func New() *engine.Generator { return engine.New(descriptor) }` — no init() self-registration; `New` panics on an invalid descriptor at wiring time.
  - **Distractor closeness is a `Group` decision.** `SameGroup` today filters by whatever `Parse` puts in `Card.Group` (script for words/specialchars). To make distractors *closer* — same language family, same region — change only how `Group` is derived (payload field or static table); no engine or option-building change. A ranked nearest-neighbour policy would be a new `DistractorPolicy` field, added once in the engine when a topic actually needs it.
  - **Custom hooks, only where truly custom**: `Accept func(key) []string` enables ModeText (see specialchars' alias table); `BuildSet func(rng, card, siblings)` handles set-shaped items (specialchars' one-member-swap sets). A topic needing DB-backed content (sentence/media sampling) declares its own narrow interface per the registry doc's contract and injects it into whatever hook needs it — the engine doesn't forbid that, it just hasn't been needed yet.
- **`seed.go`** — yaml structs + `Seed(ctx, store) error`:
  - Load your yaml (typed structs — the schema mapping stays honest Go, not reflection), build `[]engine.ItemSeed{Key, Label, Tier, Payload, CountryISO}` (payload = your payload struct; `Tier` nil inherits `base_tier` or the country rubric), and call `engine.Seed(ctx, store, descriptor, engine.SeedData{...})`. Topic-path upserts (parents first), positions, idempotency — all engine.
  - Country-linked topics: `engine.LoadCountriesFile` + `engine.SeedCountries` (shared `seeds/countries.yaml`, two-pass GB-subdivision parenting), then `engine.SeedCountryFacts` for facts (`fact_defs` + `country_facts` are the source of truth; item payload only caches — consistency asserted in tests). See roadside.
- **Tests**:
  - Unit: your `Parse` mapping, prompt/intro texts, any custom hook. Engine mechanics (dedupe/sort/shuffle/cap, determinism, dispatch) are covered by `engine`'s own tests — don't re-test them, but a determinism spot-check on your topic is cheap insurance.
  - Seed integration: env-gated on `GEODRILL_TEST_DATABASE_URL`, copy the `testDSN` fuse (DB name must contain "test") + fresh-schema pattern from an existing topic package. Assert counts vs the yaml, spot checks, fact/payload consistency.
  - Corpus audit (when claims are checkable): env-gated on `<TOPIC>_AUDIT_DATABASE_URL`, **SELECT-only**, run against the live corpus DB; fail listing violators; prune the yaml until clean. Precedent: `internal/topics/specialchars/audit_test.go`, `words/audit_test.go`.

### What stayed manual (deliberately)

- The yaml schema and its mapping to `[]ItemSeed` — typed per topic, ~15 lines.
- `Parse` (payload validation + Card mapping) and the intro-text rendering inside it — intro cards vary too much (conditional notes, list joins) to be a template.
- Prompt strings, label tables, error sentinels.
- Custom mechanics behind hooks: set-choice building, accepted spellings, (future) content sampling.
- Audit tests — they encode per-dataset judgment, not mechanics.

A seed-only tree (no generator) is also just descriptors: see `guesslang/seed.go` (three-node path per deck, `is_quizzable=false` groups).

### 3. Wiring (two lines)

- `cmd/ingest/seed_topics.go` — add the package's `Seed` to the seed-topics run.
- `cmd/bot/main.go` — `topics.Register(<pkg>.New())`.

### 4. Ship it

```
./scripts/pre-commit.sh        # the ONLY gate: fmt, build, vet, unit+integration, lint, changie, secrets scan
git commit / git push          # author Aurora, no AI trailers, changie fragment in the same commit
./bin/ingest -seed-topics      # seed the dev DB (idempotent)
pkill -f geodrill-bot && go build -o bin/geodrill-bot ./cmd/bot && <restart>   # single process, clean log
```

Don't watch GitHub Actions; the local gate is the gate. Never `go mod tidy` casually (engram is a pinned published dep now, but tidy is still not a reflex).

### Media topics (flags and beyond)

Photo questions are **photo messages from birth** (Telegram can't edit text→photo): send photo + caption + keyboard, edit caption/markup in place. `media_files` stores `local_path`/`sha256`/`telegram_file_id` (cache the file_id after first upload). Two gaps to close with the first media topic (see `vibe/design-flags-quiz.md`): a media path on answer results (schema stores only `is_media` today) plus a `MediaPath` on `engine.Card`, and image sourcing — **downloading an image set needs Aurora's explicit OK first**. The flags design doc includes an emoji-only fallback mode that needs no downloads.

## What's done (as of 2026-07-19)

- Framework: topic tree, introduction-before-review lifecycle, tiers 0–5 with global gating, typed country facts, single migration `000001_init`, `scripts/pre-commit.sh` gates, CI in both repos.
- **Topic engine**: `internal/topics/engine/` (descriptor-driven generic generator + seeder) and `internal/topics/countrytier/` (shared tier rubric); specialchars, words, roadside, and guesslang's seed all ported onto it, behavior-preserving.
- Releases: engram **v0.3.0**, geodrill **v0.1.0** (+ `ghcr.io/supercakecrumb/geodrill:v0.1.0`); engram pinned as a normal module.
- Topics live (fresh DB: 14 topics, 411 items, ~148k sentences): guess-the-language (8 groups / 41 languages, game zone), special characters (39 items, corpus-audited), road sides (252 countries), common words (79 items, corpus-audited).
- Design docs ready in `vibe/`: flags, country profiles, TLDs, cities, rivers & mountains, plonkit topics (+ working scraper prototype `cmd/plonkit`).
- Datasets committed, awaiting implementation: `seeds/tlds.yaml` (248 entries), `seeds/country_profiles.yaml` (247 entries).

## What's next (easiest → hardest)

1. **TLD topic** — data ready; reconcile the in-flight `internal/topics/tld/` against the engine (descriptor + `SeedData.TierFromCountry`), wire, seed. Per `vibe/design-tlds.md` (both directions as sibling topics).
2. **Country profiles topic** — data ready; facts `languages_spoken`/`main_religion`/`region` via `engine.SeedCountryFacts`, country→language quiz per `vibe/design-country-profiles.md`.
3. **Enrich existing datasets** — special chars is thin (39; Vietnamese/Icelandic long tail untapped) and words lean (79); extend + re-run the corpus audits.
4. **Flags** — emoji mode first (no downloads); real images after Aurora approves a public-domain set; includes the media answer-path schema addition.
5. **Rivers & mountains**, then **cities** — per their design docs.
6. **plonkit tier-5 topics** (bollards, poles, plates…) — blocked until scrape scope is reconfirmed with the site developer (robots.txt is default-deny).

Ops still pending: rotate the Telegram bot token.
