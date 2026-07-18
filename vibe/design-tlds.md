# Design: Domains / TLDs (`quiz_kind='tld_country'`)

Architecture-only design (orchestrator brief item C3). Cross-references are to `vibe/geodrill-architecture.md` by section number, and to `vibe/design-country-profiles.md` where the two topics share infrastructure. No data fill: no TLD-to-country table.

## 1. Goal

Country-code top-level domain quizzes in both directions: `.de` → Germany, and Germany → `.de`. Both directions are built and reviewed **independently** — knowing France uses `.fr` when shown the domain doesn't guarantee instant recall the other way, so each direction gets its own introduction/FSRS progress per user (a deliberate, not incidental, design choice — see §2).

## 2. Topic tree, quiz_kind, and the two-direction split

TLD is stored as a single country fact per the task brief (`fact_defs.key = 'tld'`, architecture §2.7) — there is exactly one `tld` value per country, not two. The *direction* of the question is a property of which topic the exercise is drawn from, not of the data, so this design uses **one quiz_kind, one Generator, two sibling topics** distinguished by `topics.config` (architecture §2.1's own stated purpose for that column: "per-topic knobs... distractor group ref"):

```
countries (container — shared with design-country-profiles.md §2)
└── domains (container, base_tier=0)
    ├── tld-to-country   (quiz_kind='tld_country', config={"direction":"tld_to_country"})
    └── country-to-tld   (quiz_kind='tld_country', config={"direction":"country_to_tld"})
```

`Generator.BuildExercise` reads `req.Topic.Config` to pick direction. Each direction topic gets its **own parallel item set** (same countries, different `topic_id`) rather than one item set shared across directions — items are topic-scoped by schema (architecture §2.2: `UNIQUE(topic_id, key)`), and duplicating a lightweight `{iso, tld}` pair across two topics is cheap. The payoff: `/topics` can show "TLD → country: 42/50 introduced" and "Country → TLD: 30/50 introduced" as genuinely separate progress bars, and a user can be ahead on one direction without it masking the other — which is the actual pedagogical goal, not an accident of the schema.

## 3. Item shape

One item per `(direction-topic, country)`, `items.country_id` set, `items.key = iso_a2`, minimal payload (same "cache nothing that can drift" principle as `design-country-profiles.md` §4 — the TLD value itself lives only in `country_facts`, never duplicated into `items.payload`):

```json
{}
```

Empty payload is deliberate: everything `BuildExercise` needs (the target TLD, the label, sibling TLDs for distractors) comes from the injected `FactReader` (§6) at generation time, keyed by `item.CountryID`. This is stricter than `roadside`'s approach (which caches `side` into payload for generation speed, architecture §6.2) — TLDs don't need that speed-up (one small `ListCountryFactsByDefKey` call per exercise is cheap, architecture §2.7), and skipping the cache means there is nothing here that can ever contradict the underlying fact.

## 4. Where data lives — seed file sketch

`seeds/tlds.yaml` registers the `tld` fact_def and the two direction-topics' item lists (not fact values — those are a separate data task, same split as `design-country-profiles.md` §5):

```yaml
fact_defs:
  - key: tld
    label: Country-code TLD
    value_type: text
    cardinality: single

countries: [DE, FR, KE]   # illustrative — which countries get TLD items in BOTH direction-topics
```

A single flat `countries:` list drives both direction-topics' item seeding (§5 step 4) — there is no need for the yaml itself to duplicate the list per direction.

## 5. Loader steps

1. Upsert `countries` root (shared with country-profiles, idempotent upsert-by-slug) and `domains` container child.
2. Upsert the two direction topics under `domains`, each with `Kind = "tld_country"` and its own `config` JSON (`{"direction":"tld_to_country"}` / `{"direction":"country_to_tld"}`).
3. `store.UpsertFactDef` for `tld` (idempotent).
4. For each ISO in `countries:`, resolve via `store.GetCountryByISO`, then upsert **two** items — one under `tld-to-country`, one under `country-to-tld` — both `key = iso_a2`, `payload = {}`, tier from the shared `countrytier` helper (`design-country-profiles.md` §8).
5. TLD fact *values* are seeded separately (out of scope), same deferred-data pattern as country-profiles.

## 6. Generator behavior per mode

`Generator` (package `internal/topics/tlds`) carries the same narrow `FactReader` interface as country-profiles (`ListFactsForCountry`, `ListCountryFactsByDefKey` — both already implemented on `*storage.Store`, no new storage method):

- **`direction=tld_to_country`, ModeSingle:** `Prompt = "Which country uses the domain **.{tld}**?"`; target = country name; distractors = up to 3 other countries sampled from `ListCountryFactsByDefKey("tld")`, excluding the target.
- **`direction=country_to_tld`, ModeSingle:** `Prompt = "{flag} {country} — which top-level domain?"` (flag-emoji-prefixed per architecture §5.1/decision 6, via `storage.Country.FlagEmoji`); target = `.{tld}`; distractors = up to 3 other TLDs from the same pool.
- **ModeText** (either direction, optional/lower-priority): `Accept` for `tld_to_country` = country's English name + ISO A2/A3; for `country_to_tld` = the TLD with and without the leading dot, case-insensitive (handled by `quiz.TextMatcher.Normalize`, architecture §1.6).
- `ErrNoContent` when the target country has no `tld` fact row yet (architecture §8's sentinel — same handling as country-profiles §7).
- `BuildIntro`: text-only, states the fact both ways ("🇰🇪 Kenya's country-code domain is .ke.") regardless of which direction-topic it's for — the teaching content is direction-agnostic even though quizzing isn't.

## 7. Tier mapping rule

Reuses `internal/topics/countrytier` (introduced in `design-country-profiles.md` §8) directly — no TLD-specific tiering logic. Both direction-topics' items for a given country get the same tier (the country's tier), matching country-profiles' rule for the same reason: difficulty here is "how well do I know this country," not a property of the TLD string itself.

## 8. Audit method

`internal/topics/tlds/audit_test.go`, gated on `GEODRILL_TEST_DATABASE_URL`:
1. **Direction parity:** the set of `country_id`s with an item under `tld-to-country` equals the set under `country-to-tld` — the two direction-topics must never drift apart (a country present in one but not the other is a seeding bug, not a legitimate asymmetry).
2. Every non-empty `tld` fact value is well-formed: starts with `.`, 2–3 lowercase ASCII characters after the dot (catches a malformed ingest before it reaches a live quiz).
3. No two items in the same direction-topic share a `country_id` (seed-time uniqueness).
4. Every item's target country has at most one `tld` fact row (the CHECK-enforced single-value invariant from architecture §2.7 surfaced at the application level, mirroring `roadside`'s "exactly one `drives_on` fact per country" audit, `internal/topics/roadside/integration_test.go`).

## 9. Files to create

- `internal/topics/tlds/{seed.go,generator.go,seed_test.go,generator_test.go,audit_test.go,integration_test.go}`
- `seeds/tlds.yaml` (structure + fact_def registration only)

No new migration, no new shared package beyond `countrytier` (already proposed by `design-country-profiles.md`, reused here — do not re-implement).

## 10. Verification commands

```
go test ./internal/topics/tlds/...
GEODRILL_TEST_DATABASE_URL=postgres://…/geodrill_test go test -p 1 ./internal/topics/tlds/...
```
