# Design: Rivers & mountains (`quiz_kind='river_country'` / `'mountain_landmark'`)

Architecture-only design (orchestrator brief item C5). Cross-references are to `vibe/geodrill-architecture.md` by section number, and to the other four docs where a pattern is reused rather than reinvented. No data fill: no river/mountain lists, no per-item tiers, no range names.

## 1. Goal

Two related landmark-recognition quizzes: which country (or countries) a river flows through, and which country/range a mountain belongs to. Both are "landmark → country" in direction, the reverse of `design-cities.md` (which quizzes "given a location, name the city"). A river or mountain is its own entity — not a country attribute — so, like cities (`design-cities.md` §2) and unlike profiles/TLDs, there is no `fact_defs`/`country_facts` involvement: these items ARE the quizzable subject, countries are what they link to.

## 2. Two packages, not one

Rivers and mountains are independent Go packages (`internal/topics/rivers`, `internal/topics/mountains`), each with its own `quiz_kind`, mirroring how `specialchars`/`roadside`/`words` are each their own package per mechanic (architecture §6) rather than one grab-bag "geography" package. They share a topic tree root and, informally, an implementation pattern (§4), but are independently buildable/testable/reviewable — a wave-3-style task split, not a hard requirement of this doc, but the natural seam if this work is later parallelized.

```
geography (container, base_tier=0)
├── rivers    (quiz_kind='river_country')
└── mountains (quiz_kind='mountain_landmark')
```

## 3. Item shape — the landmark is the item, `country_id` stays NULL

A river or mountain routinely spans more than one country (the Rhine flows through six), so it cannot be pinned to a single `items.country_id` the way a roadside/flags/TLD item can — architecture §2.2 already documents `country_id` as *optional*, precisely for cases like this. `items.key` = a landmark slug (`"rhine"`, `"mont-blanc"`), `items.country_id = NULL`, and the country membership lives in payload as a plain ISO list:

**Rivers** — always a country *set* (even a river wholly inside one country still uses a one-element set, so the generator's single-vs-set dispatch, §5, is driven purely by `len(countries)` rather than a second item shape):

```json
{ "countries": ["DE", "FR", "CH", "NL"] }
```

**Mountains** — country set (a peak can sit on a border) plus an optional range name:

```json
{ "countries": ["FR", "IT"], "range": "Alps" }
```

This is the same "single-vs-subgroup shape, dispatched by payload cardinality" pattern `specialchars` already uses for single-language vs subgroup letters (architecture §6.1, `internal/topics/specialchars/generator.go` lines 95–110) — reused here for the third time across these five docs (after flags' confusable groups and country-profiles' `languages_spoken`), which is worth noting as a recurring shape across the whole v2 topic family, not a coincidence per-doc.

## 4. Where data lives — seed file sketch

`seeds/rivers.yaml` / `seeds/mountains.yaml` (illustrative shape only):

```yaml
# seeds/rivers.yaml
rivers:
  - key: rhine
    name: Rhine
    countries: [DE, FR, CH, NL]
    tier: 0

# seeds/mountains.yaml
mountains:
  - key: mont-blanc
    name: Mont Blanc
    countries: [FR, IT]
    range: Alps
    tier: 0
```

**Tier is an explicit per-entry field in the yaml**, not derived from a formula (§6) — following `words`' convention (`internal/topics/words/seed.go` `wordEntry.Tier`) rather than roadside's derived-tier-function convention (`internal/topics/roadside/registry.go` `countryTier`). Reason: a river/mountain's fame doesn't track any country-level signal 1:1 (the Nile is tier-0-famous; a country it passes through, like South Sudan, might itself be a low-familiarity tier-3 country under the countries rubric) — there's no clean derivation, so it's a direct editorial call per landmark, same as word difficulty.

## 5. Loader steps

1. Upsert the `geography` root and `rivers`/`mountains` child topics.
2. For each entry, validate every code in `countries` resolves via `store.GetCountryByISO` (fails loudly on a typo'd ISO code — no silent partial import).
3. Marshal the payload (§3), upsert the item with `country_id = NULL`, `tier` = the yaml's explicit `tier` field, `key` = the yaml's `key` slug.
4. No fact_defs, no country_facts writes — this loader only touches `topics`/`items`.

## 6. Tier mapping rule

Explicit per-entry (§4) — no shared tier helper, unlike country-profiles/TLDs/flags (`design-country-profiles.md` §8). This is a deliberate divergence, not an oversight: those three topics tier by "how well-known is this *country*," a signal the shared `countrytier` helper already encodes; rivers/mountains tier by "how famous is this *landmark*," an orthogonal editorial judgment with no formula to derive it from. Sign-off on individual tier values follows the same "Aurora approves before any item gets a tier" rule as architecture §4 and §9.1.

## 7. Generator behavior per mode

Each package's `Generator` needs no injected dependencies — like `specialchars`/`roadside`, everything comes from the item's own payload plus its siblings' payloads (architecture §6.1/§6.2), no DB-backed lookups required.

**Rivers** (`internal/topics/rivers`), dispatch by `len(payload.countries)`:
- `len == 1`: ModeSingle, `Prompt = "Which country does the {name} river flow through?"`, target = the one country, distractors = random countries from sibling single-country rivers (avoids leaking the answer by "it must be a multi-country one" pattern-matching).
- `len > 1`: ModeSet only (mirrors `specialchars.buildSetMCQ`/`buildDistractorSets`, `internal/topics/specialchars/generator.go`, reused verbatim as the swap-one-member distractor algorithm), `Prompt = "Which countries does the {name} river flow through?"`, target = `quiz.CanonSet(payload.countries...)`, distractor sets built by swapping one member for a country drawn from other multi-country rivers' membership pools.

**Mountains** (`internal/topics/mountains`) — two independent exercise families per item, both always available (not a len-based dispatch like rivers, since range and country are separate facts about the same landmark):
- **Country association**: same single-vs-set dispatch as rivers, by `len(payload.countries)`. `Prompt = "Which country is {name} in?"` (single) or `"Which countries does {name} border?"` (set).
- **Range association** (only when `payload.range != ""`): ModeSingle, `Prompt = "Which range is {name} part of?"`, target = `payload.range`, distractors = up to 3 distinct `range` values sampled from sibling items (same small-N convention as every other topic's MCQ).
- A mountain missing `range` simply doesn't offer that exercise family — `BuildExercise` returns `topics.ErrNoContent` (architecture §8's sentinel) if asked for it, and the caller (per the sentinel's documented contract) skips to another candidate rather than failing.

**Media (optional, lower priority)**: both packages MAY show a generated map image highlighting the river/mountain, via the exact same `content_items`/`media_files`/gitignored-media-root pipeline as flags (`design-flags-quiz.md` §5) and cities (`design-cities.md` §5/§9) — not a fourth bespoke pipeline. Text-only (landmark name given directly in the prompt) is a fully functional fallback and should ship first; media is an enhancement, not a blocker, since — unlike flags or cities — recognizing a river/mountain from a name alone is already the core skill being tested (GeoGuessr rarely shows you a river close enough to visually identify without context clues that are really testing something else).

## 8. Audit method

One `audit_test.go` per package (`internal/topics/rivers/audit_test.go`, `internal/topics/mountains/audit_test.go`), gated on `GEODRILL_TEST_DATABASE_URL`:
1. Every ISO code in every item's `countries` list resolves to an existing `countries` row (catches a typo'd or retired code).
2. No item has an empty `countries` list (a landmark must belong to at least one country — "international waters only" rivers, if any exist in the eventual dataset, are a data-task judgment call flagged via the yaml, not silently accepted here).
3. **Mountains only**: `range` values are internally consistent across siblings (no `"Alps"` vs `"The Alps"` drift) — checked for consistency, not against an externally fixed enum, same reasoning as `design-cities.md` §8.4's terrain-vocabulary check.
4. No duplicate `key` within a topic (seed-time uniqueness, `UNIQUE(topic_id, key)` already enforces this at the DB level per architecture §2.2 — the audit test asserts the *intended* uniqueness surfaces a friendly test failure rather than a raw constraint-violation error during seeding).

## 9. Files to create

- `internal/topics/rivers/{seed.go,generator.go,seed_test.go,generator_test.go,audit_test.go,integration_test.go}`
- `internal/topics/mountains/{seed.go,generator.go,seed_test.go,generator_test.go,audit_test.go,integration_test.go}`
- `seeds/rivers.yaml`, `seeds/mountains.yaml` (structure only)

No new migration, no new shared package — reuses `quiz.CanonSet`/the specialchars-style swap-distractor pattern (architecture §1.6) directly, and the flags/cities media pipeline if the optional media enhancement (§7) is taken up.

## 10. Verification commands

```
go test ./internal/topics/rivers/... ./internal/topics/mountains/...
GEODRILL_TEST_DATABASE_URL=postgres://…/geodrill_test go test -p 1 ./internal/topics/rivers/... ./internal/topics/mountains/...
```
