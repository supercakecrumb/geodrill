# Design: Cities (`quiz_kind='city_country'` family)

Architecture-only design (orchestrator brief item C4 — explicitly the most speculative of the five). Cross-references are to `vibe/geodrill-v2-architecture.md` by section number. No data fill: no city names, no population figures, no per-city tiers.

## 1. Goal

Cities studied biggest → smallest: map-position recognition ("where is this on the map"), plus recall of terrain, elevation, and river association. Distinct from `design-rivers-mountains.md` in that a city is the quizzable subject here (rivers/mountains quiz the reverse: given a landmark, name the country).

## 2. Item shape — items + payload, no new `cities` table (recommendation + justification)

**Recommendation: no first-class `cities` table.** A city is represented purely as an `items` row in a new topic, with all city-specific data cached in `items.payload` (architecture §2.2's jsonb payload is exactly the documented seam for this: "topic-specific: ... zero-DDL"). Justification, weighed against the precedent that motivated `countries` becoming first-class (architecture §2.6):

1. **No cross-topic reference need (yet).** `countries` earned a first-class table because five-plus topics (roadside, flags, profiles, TLDs, rivers/mountains) all join against it via `items.country_id` and `country_facts`. Cities are referenced by exactly *this* topic. A table exists to be joined against by more than one consumer — that condition isn't met here.
2. **No arbitrary-filter requirement.** Architecture §2.7 justifies `country_facts`' typed/normalized shape because a future public map/stats-explorer needs to run arbitrary SQL filters over country attributes. No equivalent "cities explorer" requirement is in scope — city attributes here (terrain, elevation, rivers) are exercise-generation inputs, not queryable dataset dimensions.
3. **`items.position` already gives "biggest → smallest" for free.** Reusing the existing `int` column (architecture §2.2) as population rank needs zero new schema — city N's rank is just its `position` within the topic.
4. **Cheap to promote later.** If rivers-mountains, or a future feature, ever needs to join "which cities sit on the Danube," promoting `payload.rivers` into a real relation is a small, isolated migration on a topic with (at most) a few hundred rows — nowhere near the ~250-country, many-facts-per-country scale that made `country_facts` worth building normalized up front. This mirrors architecture §2.11's own reasoning against keeping `decks`/`skills` "because promoting later is cheap and keeping two parallel identity systems isn't."

**Open question for Aurora** (flagged in the same spirit as architecture §9): if a second topic ever needs to query cities relationally, revisit this recommendation then — don't build it preemptively.

### Payload shape

`items.country_id` set (cities belong to exactly one country — the existing FK, architecture §2.2, needs no change), `items.key` = a collision-safe slug (`"<iso_a2>-<ascii-slug>"`, e.g. `"us-springfield"`, since city names collide across countries and even within one), `items.position` = population rank (1 = biggest).

```json
{
  "elevation_m": 35,
  "terrain": ["riverine", "plains"],
  "rivers": ["Seine"],
  "map_image": "cities/paris_map.png",
  "photo_image": "cities/paris_photo.png"
}
```

`map_image`/`photo_image` are relative media-root paths, empty string when not yet ingested (same content-availability fallback convention as `design-flags-quiz.md` §6 — this topic reuses that fallback, not a duplicate design).

## 3. Topic tree and quiz_kinds

Own root — a city is not a country attribute, so it doesn't belong under the `countries` root shared by profiles/TLDs.

```
cities (container, base_tier=0)
└── which-city (quiz_kind='city_map', base_tier=0, exercise_modes={single})
```

One topic, one quiz_kind, multiple exercise *flavors* dispatched by which payload fields are populated (mirrors `specialchars`' single-generator-multiple-shapes pattern, architecture §6.1) — not four separate quiz_kinds, since they all share the same item and the same "recognize this city" goal.

## 4. Where data lives — seed file sketch

`seeds/cities.yaml` (illustrative shape only):

```yaml
cities:
  - name: Paris
    country: FR
    population_rank: 23
    elevation_m: 35
    terrain: [riverine, plains]
    rivers: [Seine]
    map_image: cities/paris_map.png
    photo_image: cities/paris_photo.png
```

## 5. Loader steps

1. Upsert the `cities` root and `which-city` child topic.
2. For each entry, resolve `country` via `store.GetCountryByISO` (no new country rows — reuses the existing `countries` table).
3. Build the collision-safe key: `strings.ToLower(iso_a2) + "-" + asciiSlug(name)`.
4. Marshal the payload (§2), upsert the item with `position = population_rank`, tier from the population-rank rubric (§6).
5. Media ingestion (map/photo images) follows the same `content_items` + `media_files` pipeline as `design-flags-quiz.md` §5, via a dedicated ingest tool (§9) rather than the topic's own `Seed` — city images are *generated*, not sourced from a public-domain set, so ingestion is a rendering step, not a download step (see §9's open question on tile licensing).

## 6. Tier mapping rule — population-rank buckets, not the countries rubric

Cities are "studied biggest → smallest," a ranking dimension the architecture §4 country-familiarity rubric doesn't cover (a small country's capital can be globally famous; a big country's 40th-largest city can be obscure — country tier is the wrong proxy). This doc proposes a parallel, population-rank-bucketed rubric, in the same spirit as §4 and equally **subject to Aurora's sign-off before any city gets a tier**:

| Tier | Population rank bucket |
|---|---|
| 0 | Top 20 world cities |
| 1 | Top 100 |
| 2 | Top 300 |
| 3 | Top 1000 |
| 4 | Beyond top 1000 / regional-significance-only |

`items.tier` is set directly from `population_rank` against these bucket boundaries at seed time (a pure function of `position`, no per-item override needed in the common case, though `items.tier` remains available as an override for a city that's famous out of proportion to its raw population rank — e.g. a small but iconic capital).

## 7. Generator behavior per mode

`Generator` (package `internal/topics/cities`), constructed with an injected media-root path (`cities.New(mediaRoot string)`, same plain-config pattern as `flags.New`, §7 there):

- **Map-position mode** (`payload.map_image != ""`): `Exercise.MediaPath` = the map image; `is_media=true`; options = target city name + up to 3 distractors from sibling items (same population-rank bucket, so difficulty stays consistent — pulling a top-20 city as a distractor for a top-1000 item would give away the answer by familiarity alone). Prompt/caption: `"Which city is marked on this map?"`.
- **Photo-recognition mode** (`payload.photo_image != ""`, distinct exercise from map-position when both assets exist): same options/grading shape, caption `"Which city is this?"`.
- **Content-unavailable fallback** (both image fields empty): text-only prompt using the city name plus country flag emoji as the *given*, quizzing terrain/elevation/river instead of "name this city" — i.e., when there's no visual to test recognition against, the mode shifts to recall-of-facts rather than silently degrading to a trivial "we told you the name, what's the name" question. Concretely: `"🇫🇷 Paris — which of these best describes its terrain?"` (ModeSingle over `payload.terrain`, options from a small fixed vocabulary sampled across siblings' `terrain` values) or `"...which river runs through it?"` (ModeSingle over `payload.rivers`, or ModeSet if a city has more than one).
- **Terrain/river modes are always available** (independent of media presence) as a second, separate item-derived exercise family — even a city with both images ingested can still be quizzed on its river association; these read `payload.terrain`/`payload.rivers` directly (no DB fact lookup needed, unlike country-profiles — city payload IS the fact store here, since nothing else needs to query it relationally, §2).
- **Elevation mode**: bucketed MCQ (`<100m` / `100–500m` / `500–1500m` / `>1500m`) rather than free-text numeric matching — avoids needing exact-value grading for a fact nobody would recall to the meter.

## 8. Audit method

`internal/topics/cities/audit_test.go`, gated on `GEODRILL_TEST_DATABASE_URL` + `CITIES_AUDIT_MEDIA_ROOT` (skip if unset, same reasoning as `design-flags-quiz.md` §8 — media root is gitignored):
1. No two items share a `position` value that would make "biggest → smallest" ambiguous within the same tier bucket (exact global uniqueness isn't required — ties are fine — but flag any pair sharing a rank AND a tier boundary-adjacent position, which would make bucket assignment nondeterministic).
2. Every `country` reference resolves to an existing `countries` row.
3. Every non-empty `map_image`/`photo_image` path exists on disk under the media root and its `media_files.sha256` matches (identical check to `design-flags-quiz.md` §8.1).
4. Every `terrain` value belongs to a small fixed vocabulary (checked for internal consistency across siblings — e.g. no `"Riverine"` vs `"riverine"` capitalization drift — not against an externally fixed enum, since the vocabulary itself is a data-task decision).

## 9. Files to create

- `internal/topics/cities/{seed.go,generator.go,seed_test.go,generator_test.go,audit_test.go,integration_test.go}`
- `cmd/citymaps/main.go` — isolated map-tile/photo generation tool (architecture §8's "isolated `cmd/`" pattern, mirroring `cmd/plonkit`'s isolation note). **Open question for Aurora**: which map-tile source/renderer to use, and its licensing/attribution terms for redistribution via Telegram — most tile providers restrict caching or require on-image attribution; this needs a decision before `cmd/citymaps` is implemented, not assumed here.
- `seeds/cities.yaml` (structure only)

No new migration — `items`/`topics`/`content_items`/`media_files` are all already generic enough (§2's core payoff: zero schema changes for this entire topic).

## 10. Verification commands

```
go test ./internal/topics/cities/...
GEODRILL_TEST_DATABASE_URL=postgres://…/geodrill_test go test -p 1 ./internal/topics/cities/...
CITIES_AUDIT_MEDIA_ROOT=./media GEODRILL_TEST_DATABASE_URL=postgres://…/geodrill_test go test ./internal/topics/cities/... -run Audit
```
