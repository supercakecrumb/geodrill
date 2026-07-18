# Design: Country profiles (`quiz_kind='country_profile'`)

Architecture-only design (orchestrator brief item C2). Cross-references are to `vibe/geodrill-v2-architecture.md` by section number. No data fill: no fact values, no per-country tiers, no religion/language datasets.

## 1. Goal

Entirely fact-driven country trivia: "what language is spoken in Kenya?", "what is Nigeria's main religion?", "which region is Brazil in?" — the generator builds every question FROM `country_facts` rows at generation time (architecture §2.7); no item hand-authors a question or an answer. Adding a new profile question type later is a `fact_defs` insert plus one Go map entry (§6), never a migration or a bespoke item shape.

## 2. Topic tree and quiz_kind

New shared root, reused by `design-tlds.md` (§2 there) for the same reason: both are "ask something about a country" quizzes over the typed-fact store, and living under one root gives `/topics` a coherent "Countries" section instead of two unrelated root-level entries.

```
countries (container, base_tier=0)
└── country-profiles (quiz_kind='country_profile', base_tier=0, exercise_modes={single,set})
```

`RootSlug="countries"`, `TopicSlug="country-profiles"`, `Kind="country_profile"`. The `countries` root is created idempotently (same `UpsertTopic` upsert-by-slug pattern `roadside.Seed` already uses for `roads`) — whichever of profiles/TLDs seeds first creates it, the other just upserts the same slug and gets the existing row back.

## 3. fact_defs this topic introduces

Three, registered via the existing `store.UpsertFactDef` (architecture §2.7, `internal/storage/facts.go`, no new storage method needed):

| key | value_type | cardinality | example question this powers |
|---|---|---|---|
| `region` | text | single | "Which region is {country} in?" |
| `main_religion` | text | single | "What is {country}'s main religion?" |
| `languages_spoken` | text | multi | "What language(s) are spoken in {country}?" |

`dataset='baseline'` for all three. This is the zero-DDL seam in action (architecture §2.7's own promise): a fourth fact (`gdp_per_capita`, `lgbt_legal`, whatever Aurora wants next) is a new `fact_defs` row plus one entry in the prompt-template map (§6) — no schema change, no new item shape.

## 4. Item shape — one item per (country, fact_def) pair

The key design decision: items are **not** one-per-country. A single "France" item can't encode "which fact question is this" without either duplicating hand-authored content (forbidden by the goal) or overloading `items.payload` with the actual fact value (which would make the item a cache, not fact-driven — and would drift from `country_facts` the moment a value is corrected, exactly the failure mode architecture §2.7 exists to avoid).

Instead: `items.key = "<iso_a2>:<fact_key>"` (e.g. `"KE:languages_spoken"`), `items.country_id` set, `items.payload` holds only *which* fact this item asks about — never the value:

```json
{ "fact_key": "languages_spoken" }
```

Three items per country that has profile facts (one per fact_def above), all in the same topic. This keeps "items = countries linked by country_id" true (task brief's own framing) while letting the topic grow new question types without new item shapes.

## 5. Where data lives — seed file sketch

`seeds/country_profiles.yaml` registers the fact_defs and the item list; it does **not** carry fact values (those come from a separate future data-ingestion task per fact — economics/religion/language datasets are out of this doc's scope). Illustrative shape only:

```yaml
fact_defs:
  - key: region
    label: Region
    value_type: text
    cardinality: single
  - key: main_religion
    label: Main religion
    value_type: text
    cardinality: single
  - key: languages_spoken
    label: Languages spoken
    value_type: text
    cardinality: multi

items:
  - country: KE
    facts: [region, main_religion, languages_spoken]
  - country: FR
    facts: [region, main_religion, languages_spoken]
```

A country's actual `region`/`main_religion`/`languages_spoken` *values* are inserted directly as `country_facts` rows by whatever data task populates each dataset (architecture §2.7 shows the shape: `InsertCountryFact(ctx, countryID, factDefID, valText, ...)`), independently of this topic's item seeding — the item list only decides which countries get *asked about*, not what the answers are.

## 6. Loader steps

1. Upsert the `countries` root topic and `country-profiles` child topic (§2).
2. For each of the three fact_defs in the yaml, `store.UpsertFactDef` (idempotent, keyed on `key`).
3. For each `items` entry, resolve `country` via `store.GetCountryByISO` (countries already seeded by `roadside`/its `countries.yaml`, architecture §6.2 — this loader never creates countries).
4. For each `facts` entry, upsert one item: `key = iso+":"+factKey`, `payload = {"fact_key": factKey}`, `country_id` set, `tier` from the shared tier helper (§7).
5. Actual fact *values* are seeded separately (out of scope) — this loader can run before any `country_facts` rows exist; the audit (§8) is what catches an item with no backing value.

## 7. Generator behavior per mode

`Generator` (package `internal/topics/countryprofiles`) needs DB-backed fact reads, which the `internal/topics` package doc explicitly anticipates (its "content access stays out of this package" section, `internal/topics/registry.go`): declare a narrow interface the concrete generator carries as a field, injected at `cmd/bot` wiring time — and it turns out **no new storage method is needed at all**, `*storage.Store` already implements both calls verbatim:

```go
type FactReader interface {
    ListFactsForCountry(ctx context.Context, countryID uuid.UUID) ([]storage.CountryFact, error)
    ListCountryFactsByDefKey(ctx context.Context, factKey string) ([]storage.CountryFact, error)
}
```

A small hardcoded map drives the prompt template and mode per `fact_key` (adding a fourth fact later = one new map entry):

| fact_key | mode | prompt template |
|---|---|---|
| `region` | ModeSingle | "Which region is {country} in?" |
| `main_religion` | ModeSingle | "What is {country}'s main religion?" |
| `languages_spoken` | ModeSet | "What language(s) are spoken in {country}?" |

- **ModeSingle** (`region`, `main_religion`): `ListFactsForCountry(item.CountryID)` gets the target's value; `ListCountryFactsByDefKey(fact_key)` gets every country's value for that fact, from which up to 3 distinct-value distractors are sampled (same small-N convention as `roadside`/`specialchars`). `ErrNoContent` (architecture §8's own sentinel, `internal/topics/registry.go`) is returned when the target country has no fact row yet — callers skip to the next candidate exactly like any other topic's empty-content case.
- **ModeSet** (`languages_spoken`): target = `quiz.CanonSet` of the country's `languages_spoken` rows; distractor sets built by the same swap-one-member algorithm as `specialchars.buildDistractorSets` (`internal/topics/specialchars/generator.go`) over the pool from `ListCountryFactsByDefKey("languages_spoken")` — this is the third occurrence of this exact pattern across the five docs (specialchars' subgroup letters, flags' confusable groups, this), worth factoring into a shared `internal/topics/setquiz` helper (`CanonSet` + swap-based distractor building) if a third consumer lands before a fourth — not required for this wave, just flagged as a recognized duplication.
- **BuildIntro**: renders the fact's label + a short "you'll be asked to recall this" blurb; text-only (no media in this topic).

## 8. Tier mapping rule

Reuses a new shared helper, **`internal/topics/countrytier`**, exposing the architecture §4 rubric's **Countries** column as a plain function (not duplicated per-topic): tier 0 = US/UK/France/Germany/Japan; tier 1 = rest of Europe + major economies; tier 2 = all UN members; tier 3 = dependencies/low-coverage states; tier 4 = microstates/disputed/partial recognition. This is deliberately a *different* hardcoded ISO set than `roadside`'s existing G20-based tier function (`internal/topics/roadside/registry.go` `countryTier`) — road-side familiarity ("countries whose driving you'd recognize") and general country familiarity are different signals, and `roadside` is existing, working code this task must not touch. The shared helper is new and used only by the topics this wave adds (country-profiles, TLDs — `vibe/design-tlds.md` §6 — and referenced, not required, by flags/rivers-mountains).

All three items for a given country (one per fact_key) get the **same** tier — the country's tier, not a per-fact-question tier — since difficulty here tracks "how well do I know this country" rather than "how hard is this specific fact."

## 9. Audit method

`internal/topics/countryprofiles/audit_test.go`, gated on `GEODRILL_TEST_DATABASE_URL` (mirrors `internal/tips/audit_test.go`'s pattern):
1. Every item's `payload.fact_key` is a key that exists in `fact_defs`.
2. No duplicate `(country_id, fact_key)` pair across items (the seed's own uniqueness invariant).
3. For ModeSingle items, the target country has at least one `country_facts` row for that fact_def (otherwise the item is a dead question — flagged, not necessarily fatal, since data ingestion may lag item seeding per §6 step 5).
4. For `languages_spoken` items, the target country has at least one row (multi facts with zero rows are indistinguishable from "not yet populated").

## 10. Files to create

- `internal/topics/countryprofiles/{seed.go,generator.go,seed_test.go,generator_test.go,audit_test.go,integration_test.go}`
- `internal/topics/countrytier/tier.go` (+ `tier_test.go`) — shared helper, §8
- `seeds/country_profiles.yaml` (structure + fact_defs registration only)

No new migration — `fact_defs`/`country_facts`/`items`/`topics` all exist from `000005_v2_core`.

## 11. Verification commands

```
go test ./internal/topics/countryprofiles/... ./internal/topics/countrytier/...
GEODRILL_TEST_DATABASE_URL=postgres://…/geodrill_test go test -p 1 ./internal/topics/countryprofiles/...
```
