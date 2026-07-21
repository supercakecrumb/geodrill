# Design: Cities on the map (`city_on_map`) — decided plan

Decision + implementation plan, approved by Aurora 2026-07-21. This doc **resolves the open
questions in `vibe/proposal-places-on-maps.md`** (rendering path picked: self-rendered static
images, no tiles, no Mini App dependency) and **supersedes the quiz mechanics in
`vibe/design-cities.md`** (its MCQ modes are rejected — no option-list quizzes for cities;
its payload/tier/no-new-table reasoning still stands). Both stay as historical records.

> **As-built note (2026-07-21):** §4/§5's *offline batch render + upload* was superseded during
> the build by **on-demand rendering** — the bot renders each city map in-process on first send
> and caches the Telegram `file_id` (rendered at most once), so no batch/upload step is required
> and Garage is *optional* persistence. `cmd/citymaps`/`cmd/citymapsync` remain as an optional
> local-preview / pre-warm; the Natural Earth GeoJSON is baked into the Docker image. The
> seeder sets `map_image` for every city *with coordinates* (not gated on media_files). The
> README "Cities map-based topic" section is the current operator runbook.

## 1. Decisions (Aurora, 2026-07-21)

1. **Replace, don't add.** The current city→country quiz (`city_to_country`) is removed and
   replaced in place by ONE map-based question per city. No parallel topic, no V2 path.
2. **Review question** = a static map image with a dot at the city's location; the user
   **types the city name** (ModeText + inline autocomplete). MCQ/option lists stay banned
   for cities.
3. **Intro card ("city discovered")** = the same map image + a rich caption: country (flag),
   admin-1 region, population, elevation, and — for tiers 0–2 — a scraped landmark/fact blurb.
4. **Map rendering: self-rendered, zero tiles.** A pure-Go batch renderer over Natural Earth
   public-domain vector data. No tile servers (OSMF policy trap), no API keys, no attribution
   obligations, and **no text labels on the image** (labels would leak the answer).
5. **Facts: scraped, not hand-filled.** An automated pipeline (Wikidata match → Wikipedia
   page-summary extract) for tier 0–2 cities (~1,000), committed as a derived seed file.
6. **Existing city progress: reset.** Map-dot recognition is a different skill than
   recalling a city's country; everyone re-discovers cities biggest-first through the new
   intro cards. Review history stays as archive.
7. **Unchanged:** population-banded tiers 0–6, `items.position` = global population rank
   (introduction order stays biggest-first), GG-only gating via `items.gg_relevant`.

## 2. UX spec

**Review question** (photo message):
> 📍 Which city is at the red marker? Type its name.

with the "⌨️ Type your answer" inline-autocomplete button (city-name suggestions). Grading is
the existing free-text path (`quiz.TextMatcher`, MaxEdits 2) over accepted spellings =
curated exonym + GeoNames native name + asciiname (e.g. Munich / München / Munchen).

**Intro card** (photo + caption; lines omitted when data is missing; fact block and
attribution only when a blurb exists):

```
📍 Munich — 🇩🇪 Germany
🗺 Bavaria
👥 1,512,491 people
⛰ 520 m elevation

Munich is the capital and most populous city of Bavaria…

📖 en.wikipedia.org/wiki/Munich · CC BY-SA 4.0
```

Caption is plain text (the intro sender HTML-escapes); must stay under Telegram's 1024-char
caption cap (audited, §9).

**Map image style** (1280×800 PNG, no text anywhere): ocean `#B8D6E6`; neighbor countries
muted (fill `#E9E5DC`, stroke `#CFC9BD` 1 px); the city's country highlighted (fill
`#F2D8A0`, stroke `#A87F32` 2 px); city marker drawn last in screen space — halo r=16 px
`#D64533` @35% alpha → white ring r=8 px → core dot r=6 px `#D64533`.

**Missing-map fallback** (image not on disk / not rendered yet): the exercise degrades to a
text question that stays answerable — `"🏙 Name the city in 🇩🇪 Germany (Bavaria) with about
1,500,000 people."` (population rounded to 2 significant digits; region parenthetical
omitted when empty). Intro degrades to text-only (the caption is already the content).

## 3. Data compile — `scripts/fetch-cities.sh` + `cmd/citygen`

- `fetch-cities.sh`: add a second idempotent download (same skip-if-present/`--force`
  shape): `https://download.geonames.org/export/dump/admin1CodesASCII.txt` →
  `data/geonames/admin1CodesASCII.txt` (TSV `code \t name \t asciiname \t geonameid`,
  code like `DE.02`). Same GeoNames CC-BY 4.0 provenance.
- `cmd/citygen`: `geoRow` gains lat (col 4), lng (col 5), admin1 code (col 10), elevation
  (col 16, often empty), dem (col 17). **Effective elevation**: col 16 when parseable, else
  `dem` when parseable and not the `-9999` sentinel, else absent (document "dem is a model,
  not a survey" in the header). New `loadAdmin1Names` → `"DE.02" → "Bavaria"`.
- `seeds/cities.yaml` schema gains per city (omitted when unknown):
  `lat`, `lng` (5 decimals), `region` (admin-1 name), `elevation` (m), `geoname_id`,
  `alt_names` (GeoNames name + asciiname when ≠ Name, deduped).
- The **exonym-preserving merge is untouched**, and the unmatched-curated preservation rule
  extends to the new fields: an unmatched curated city keeps whatever lat/lng/region it
  already carries in the committed file — so a one-time hand patch of coordinates for the
  few unmatched rows survives every later regen (exactly the existing population semantics).
- Header comment rewritten: new fields, second CC-BY source, elevation fallback note, quiz
  description updated to the map question.
- New `cmd/citygen/main_test.go`: tier bands, elevation/dem fallback, admin1 resolution,
  curated-field preservation (extract the pure funcs to make this testable).

Regen + commit the new `seeds/cities.yaml`. The pre-cutover cities package ignores unknown
YAML fields, so this commit is green standalone.

## 4. Map renderer — `scripts/fetch-naturalearth.sh` + `internal/citymap` + `cmd/citymaps`

- **Data**: `ne_10m_admin_0_countries.geojson` (~24 MB) from the Natural Earth vector GitHub
  mirror (`nvkelso/natural-earth-vector`, `geojson/` folder) → `data/naturalearth/`
  (gitignored). Natural Earth is public domain. 1:10m is required — microstates degrade or
  vanish at 50m/110m. Fetch script mirrors `fetch-cities.sh`.
- **Deps** (via `go get`, never `go mod tidy` blindly): `github.com/paulmach/orb`
  (GeoJSON + geometry), `github.com/fogleman/gg` (2D raster).
- **`internal/citymap`** (logic lives in a package so `go test ./...` covers it with tiny
  `testdata/` fixtures, never the 24 MB download; `cmd/citymaps` stays a thin CLI):
  - `ImageFileName(key)` = `strings.ReplaceAll(key, ":", "-") + ".png"` (`de:munich` →
    `de-munich.png`) — the single home of the filename convention.
  - Country lookup by `ISO_A2_EH` property first (NE's `ISO_A2` is literally `-99` for
    France/Norway — known NE quirk), plus a small hand-override map (FR, NO, XK, extended as
    the audit demands). `AuditISOCoverage` fails fast listing every seed country with no
    polygon.
  - **Framing** (pure, unit-tested): (1) normalize all longitudes into city-centered space
    (`lon' ∈ [cityLng−180, cityLng+180)`) — this single trick handles the antimeridian for
    RU/FJ/NZ/US-Aleutians; (2) pick the country **component** (single polygon) containing
    the city point, else nearest bound-center — Paris frames on metropolitan France, not a
    France+Guiana world bbox, Honolulu on the Hawaii component; (3) frame = component bound
    ∪ city point + 8% padding per side; (4) minimum span 3.0° longitude-equivalent (~330 km)
    so microstates get muted-neighbor regional context; (5) fit to 1280×800 by expanding the
    short axis, never cropping.
  - **Projection**: local equirectangular (`x = (lon'−lon0)·cos(latMid)`, `y = lat0−lat`) —
    linear, deterministic, no high-latitude Mercator blowup. Polygon holes via even-odd fill.
  - **Determinism**: pure function of inputs; tests assert double-render byte-equality plus
    pixel probes (dot core color at the projected city point, highlight fill inside the
    country). **No golden PNGs committed** — Go's PNG encoder may drift across releases.
- **`cmd/citymaps`**: flags `-ne`, `-cities`, `-countries`, `-out data/citymaps`, `-force`,
  `-only <key>`. Startup audits: ISO coverage + missing/zero coordinates (error listing
  offenders). Per city: skip-if-present unless `-force`, render, write. Summary report.
  ~4.5k renders is a minutes-scale one-time batch.

## 5. Image storage — Garage S3 (not local disk)

**Decision (Aurora, 2026-07-21): city map images live in Garage S3, not a local disk
cache.** Existing local-disk media (flags) stays as-is; migrating it to Garage is a
follow-up ticket ([[Move flags images to Garage S3]]).

**Why Garage barely touches the hot path.** The bot's `SendPhoto`
(`internal/telegram/bot.go`) caches Telegram's `file_id` in `media_files` after the *first*
send of each image; every later send reuses the `file_id` and transfers no bytes. So Garage
is read **only once per image** — the first time that image is sent — to seed the cache.
After that, images are served from Telegram's CDN. Garage is effectively a cold origin.

**Namespace** (per [[object-storage]] / [[garage]]): one bucket per consumer, domain-scoped
name **`apps-geodrill`**; object key **`citymaps/<file>`** (the logical tree is
`apps/geodrill/citymaps/…`). The `media_files.local_path` column stores a scheme-qualified
reference **`garage://apps-geodrill/citymaps/<file>`** — no DDL, the column is just a stable
media identity (its doc already calls it "on-disk identity"; we widen that to "media
identity"). Flags keep bare relative paths (`data/flags/xx.png`), so the two backends
coexist by scheme, not by a forked code path.

**S3 client**: `github.com/minio/minio-go/v7` (lightweight, path-style, proven against
Garage). Added via `go get` (never a blind `go mod tidy` — engram/go.work rule).

**Config** (all optional; `internal/config`): `GARAGE_S3_ENDPOINT` (`http://garage:3900`
in prod over the internal `dokploy-network`), `GARAGE_S3_REGION` (default `garage`),
`GARAGE_ACCESS_KEY_ID`, `GARAGE_SECRET_ACCESS_KEY`, `GARAGE_BUCKET` (default
`apps-geodrill`). Server-side only, stored in OpenBao `kv/apps/geodrill/s3_*`, injected at
deploy. Unset → no S3 client is built; `cmd/citymapsync` errors if run without it, and the
bot logs + skips (city maps simply won't have been seeded in that case — see §7).

**`internal/storage/objstore` (new)**: a thin minio-go wrapper — `PutObject(ctx, bucket,
key, r, size, contentType)`, `GetObject(ctx, bucket, key) (io.ReadCloser, error)`,
`StatObject`. Built from config; nil when Garage isn't configured. `ParseGarageRef(s)
(bucket, key string, ok bool)` parses `garage://bucket/key`.

**`SendPhoto` change** (`internal/telegram/bot.go`): add an optional `objFetcher` (nil-safe,
wired like `media`). File resolution: if the path parses as a `garage://` ref → on a cache
miss, `GetObject` → `telebot.FromReader`; otherwise `telebot.FromDisk(path)` (flags,
unchanged). With a cached `file_id`, neither branch runs — the `file_id` is used directly.
No Garage config + a `garage://` path → log + fail that one send (never happens in practice,
since §7 only seeds `map_image` for images already registered).

**`cmd/citymapsync` (new — the uploader/registrar)**: walks local `data/citymaps/*.png`
(the renderer's output, §4), and for each: compute sha256 + PNG dims, `PutObject` to
`apps-geodrill` key `citymaps/<file>` (skip the upload when `StatObject` shows the same size
already present, unless `-force`), then `PutMediaFile(nil, "garage://apps-geodrill/citymaps/<file>",
sha256, w, h, bytes)`. Idempotent (media upsert keyed on the garage ref). Needs DB + Garage
config. `cmd/flagassets` is left **untouched**.

## 6. Facts scraper — `cmd/cityfacts` → `seeds/city_facts.yaml`

**Sources.** Identity via **Wikidata** (CC0): batched SPARQL on the GeoNames-ID property
P1566 (`VALUES ?gid {…}` ~150 ids/request → ~8 requests for all of tiers 0–2) yields QID +
enwiki title. Fallback for rows without `geoname_id` / without a P1566 hit: Wikipedia
GeoSearch at the city's lat/lng (10 km radius), accept a casefold title match against
name/alt_names, else nearest containing the name, else record unmatched and skip. Blurb via
the **Wikipedia REST page-summary** `extract` — **CC BY-SA 4.0**, so: license + attribution
in the seed header, per-entry `source_url`, and the visible credit line on the intro card
(§2). Skip `"type": "disambiguation"` (retry via GeoSearch). Trim to ≤2 sentences and ≤350
chars on a sentence boundary (`trimBlurb`, unit-tested).

**Operation.** Flags `-cities`, `-out`, `-tier-max 2`, `-limit`, `-rps 2`, `-force`.
Single-threaded ticker at `-rps` (~10 min for 1k cities), descriptive User-Agent
(`geodrill-cityfacts/1.0 (+https://github.com/supercakecrumb/geodrill)`), 3 retries with
backoff on 429/5xx. **Idempotent/resumable**: loads existing `-out`, skips present keys
unless `-force`, rewrites the file (sorted by key, stable diffs) every 25 successes and at
exit. Ends with an unmatched-cities report.

**Output** (committed):

```yaml
facts:
  - key: de:munich
    blurb: "Munich is the capital and most populous city of Bavaria…"
    source_title: Munich
    source_url: https://en.wikipedia.org/wiki/Munich
    wikidata: Q1726
    retrieved: "2026-07-21"
```

## 7. Topic replacement in place — `internal/topics/cities`

**Naming**: `Kind = "city_on_map"`; leaf slug `city-on-map`, name "City on the Map"; root
`cities` unchanged. Legacy slug `city-to-country` survives only inside the one-time
migration below.

**Payload** (`items.payload`, exact shape):

```go
type itemPayload struct {
    Key         string  `json:"key"`          // items.key echoed — engine hooks are key-only
    CityName    string  `json:"city_name"`
    Flag        string  `json:"flag"`
    CountryName string  `json:"country_name"`
    ISOA2       string  `json:"iso_a2"`
    ISOA3       string  `json:"iso_a3"`
    Lat         float64 `json:"lat"`
    Lng         float64 `json:"lng"`
    Region      string  `json:"region,omitempty"`
    Population  int64   `json:"population"`
    ElevationM  *int    `json:"elevation_m,omitempty"`
    MapImage    string  `json:"map_image"`      // bare filename under data/citymaps
    Fact        string  `json:"fact,omitempty"` // tiers 0–2 only
    FactURL     string  `json:"fact_url,omitempty"`
}
```

**generator.go** (rewritten): `parseCard` flips the direction — `Keys = [p.Key]` (the answer
is the CITY), `Subject` = the full question text (PromptText/PromptSingle = `"%s"`, flags
precedent), `Intro = introCaption(p)` (§2 template; local `formatInt` for comma grouping).
Lookup tables now read **seeds/cities.yaml**: `cityLabels[key] = Name`,
`cityAccept[key] = dedupe(Name, AltNames…)`. Delete `countryAliases` + the country tables —
superseded. Distractor policy stays the never-exercised single-choice fallback the engine's
Validate demands (sibling city names). **Media wrapper (no IO)**: because the image lives in
Garage, the generator does **no** filesystem/network probe. The seeder decides presence
(below); the generator simply builds `MediaPath = "garage://" + bucket + "/citymaps/" +
p.MapImage` **when `p.MapImage != ""`**, else applies the §2 fallback (text-only exercise /
text-only intro). `DefaultMediaRoot = "garage://apps-geodrill/citymaps"`,
`New()`/`NewWithMediaRoot(ref)` so tests can point at any ref (or `""` to force the
fallback). `ExerciseModes` stays `["autocomplete"]` (ModeText + the type-your-answer
button); grading untouched.

**seed.go** (rewritten): `citySeed` gains the §3 fields + a loader for
`seeds/city_facts.yaml` (missing file fine — facts are enhancement; malformed errors).
**Presence-at-seed-time**: before building items, load the set of registered city-map
garage refs once — a new `store.ListMediaLocalPathsByPrefix("garage://apps-geodrill/citymaps/")`
query returns every synced image's ref. For each city, set `payload.map_image =
citymap.ImageFileName(key)` **only when its ref is in that set**, else `""` (→ generator
fallback). This moves the presence check to one batch at seed time and keeps the generator
pure; re-running the seed after syncing more images fills in more `map_image` values. Item
build also adds the other new payload fields and folds `Fact`/`FactURL` for tier ≤ 2.
`sortByPopulationDesc` + position-by-rank + `TierFromCountry:false` unchanged. (Operational
order therefore matters: render → `citymapsync` → seed; documented in §10.)

**One-time in-place migration + reset** — `migrateLegacyTopic(ctx, store)`, called by
`SeedFromFile` before `engine.Seed`. Needed because `engine.Seed` upserts topics keyed on
`(parent_id, slug)` — seeding the new slug directly would create a second leaf and orphan
the 4,487 items. Idempotent by construction (keyed on the legacy path existing):

1. `GetTopicByPath("cities/city-to-country")`; not found → no-op (fresh DB / already done).
2. In one transaction:
   - `DeleteOpenExercisesByTopic` (`answered_at IS NULL` — reviews only reference answered
     exercises, so no FK issue; answered exercises + `reviews` stay as archive);
   - `DeleteOpenIntroductionsByTopic` (stale unanswered intro cards);
   - `DeleteUserItemsByTopic` — **the decided reset** (makes every city re-introducible,
     biggest-first, per `ListCandidateIntroItems`'s `user_items`-derived "new" check);
   - `RenameTopic(legacy.ID, "city-on-map", "City on the Map")`.
3. `engine.Seed` then converges quiz_kind/exercise_modes/name on the renamed row and
   rewrites all 4,487 payloads in place — item IDs (and the reviews archive's references)
   are preserved.

**Tier-progress cache is deliberately NOT recomputed in the migration.** `user_tier_progress`
is a cache; after the reset it's stale-*high* (still counts the deleted city cards as
good-shape), which only keeps tiers *unlocked* — the harmless, permissive direction — and it
self-heals on each user's next answer via the existing `RecomputeTierProgressForTier`.
Recomputing every user inside a seed would be heavy and would aggressively *re-lock* tiers a
user earned partly through cities, hiding other topics' items. So we let it self-heal.

New sqlc queries (+ regenerate; **no DDL, no new migration files**): `RenameTopic`,
`DeleteOpenExercisesByTopic`, `DeleteOpenIntroductionsByTopic`, `DeleteUserItemsByTopic`,
and `ListMediaLocalPathsByPrefix` (for the §7 presence check; already added in Phase 3).
`gg_relevant` needs nothing — city items are country-linked, the existing country pass
covers them. Call-site touch-ups: seed/register log strings in `cmd/ingest/seed_topics.go` +
the comment block in `cmd/bot/main.go`.

## 8. Autocomplete — `suggest.DomainCity`

- `internal/suggest`: new `DomainCity`; replace the constructor **in place** with
  `NewFromSources(countries, capitals, cities)`. City entries: `Key = "city:" + key`,
  `Label = Name`, `Emoji` = country flag (doesn't leak — the highlighted country is visible
  on the map; disambiguates Paris FR vs Paris US), `Coverage` = the country's `gg_coverage`
  so `MatchDomain(ggOnly)` hides non-coverage cities.
- `DomainForAnswer` resolution order **country → capital → city**, documented: city-states
  ("Singapore") stay DomainCountry, capital-named cities resolve DomainCapital — harmless
  (identical label exists there; grading is always the open exercise's own Accept list); the
  ~4.2k city-only names now route to DomainCity instead of the old default-to-country
  misroute.
- Source: export `cities.SuggestCities()` (reads seeds/cities.yaml; keeps payload private).
  Wire in `cmd/bot/main.go` after the capitals block (map country → flag/coverage via the
  loaded countries slice; skip unknown ISOs defensively). Index grows to ~5k entries —
  linear scan per keystroke stays sub-millisecond. `internal/telegram` needs no changes.

## 9. Tests / audits

- `cmd/citygen`: tier bands, elevation/dem fallback, admin1 resolution, curated-field
  preservation.
- `internal/citymap` (tiny hand-written GeoJSON fixtures, never the real NE file): component
  selection (metropolitan-France case), antimeridian framing, microstate min-span, aspect
  fit, ISO `-99` quirk, double-render byte-equality + pixel probes.
- `cmd/cityfacts` helpers: `trimBlurb`, title matching, disambiguation skip.
- `cities/generator_test.go` (rewritten): payload validation matrix; map prompt + missing-map
  fallback prompt (`NewWithMediaRoot(t.TempDir())`); caption variants (full / no-region /
  no-elevation / no-fact), population formatting, attribution gating; Accept = name +
  alt_names; MediaPath on both exercise and intro.
- `cities/seed_test.go`: payload construction from the real seed (Shanghai/Munich spot
  checks incl. `map_image` naming); fact folding strictly tier ≤ 2; missing facts file OK.
- `cities/audit_test.go`: **(a) unconditional** — lat/lng in range and not (0,0), unique
  keys, country resolves, tier matches population band, every facts key exists in
  cities.yaml with blurb ≤ 400 chars + source_url, every rendered caption < 1024 chars;
  **(b) env-gated** (test-named-DB fuse, flags precedent) — seed against a test DB pre-loaded
  with `garage://` media rows, assert every synced city's `map_image` gets set and its
  `media_files` ref round-trips. (No live-Garage dependency in tests — the object store is
  faked; only the DB registration is exercised.)
- `cities/integration_test.go`: fresh seed → `cities/city-on-map`, 4,487 items by rank; the
  **legacy-migration scenario** — old-shape topic + user_items + one open exercise + one
  answered exercise + review + tier progress → run Seed → same topic UUID renamed,
  user_items gone, open exercise gone, answered exercise + review intact, tier progress
  refreshed; second Seed run → no-op.
- `suggest`: DomainCity + ggOnly filtering; DomainForAnswer ordering (Singapore→country,
  Canberra→capital, Munich→city, unknown→country).
- Every commit: `./scripts/pre-commit.sh` (all new tests must skip cleanly without `data/`
  or a test DB).

## 10. Commit sequence (atomic, each green) + cutover runbook

1. **Data**: fetch-cities.sh admin1 + citygen fields/preservation/header/tests + regenerated
   `seeds/cities.yaml`.
2. **Renderer**: orb+gg deps, `fetch-naturalearth.sh`, `internal/citymap` + tests,
   `cmd/citymaps`.
3. **Garage plumbing**: minio-go dep, `internal/config` Garage vars, `internal/storage/objstore`,
   `SendPhoto` garage:// branch, `cmd/citymapsync`, `ListMediaLocalPathsByPrefix` query +
   regenerated code, `.env.example` update, tests. (`cmd/flagassets` untouched.)
4. **Facts**: `cmd/cityfacts` + tests + the scraped, committed `seeds/city_facts.yaml`.
5. **Suggest**: DomainCity + `NewFromSources` + `cities.SuggestCities()` + bot wiring +
   tests (inert until the cutover — no exercise produces a city answer yet).
6. **Cutover** (the one big in-place commit): cities generator/seed rewrite, migration
   queries + regenerated code, call-site strings, all cities tests, **changie fragment**.
7. **Docs**: README pipeline section, PROGRESS.md note.

**Operational cutover** (on the box — needs the internal `dokploy-network` for Garage and a
provisioned `apps-geodrill` bucket + `kv/apps/geodrill/s3_*` key; that provisioning is a
`maintain-infra`/`deploy` step, not part of these commits): `pg_dump` the dev DB first
(CLAUDE.md rule) → `./scripts/fetch-naturalearth.sh` → `go run ./cmd/citymaps` (renders local
PNGs) → `go run ./cmd/citymapsync` (uploads to Garage + registers `media_files`) → run
ingest seed (rename + reset + payload rewrite with `map_image` set for synced cities + facts
fold + gg pass) → restart the bot (built binary, never `go run`) → `./scripts/notify.sh`.
Verify by hand: one intro card, one map question, autocomplete suggesting cities, and the
missing-map text fallback for a not-yet-synced city.

## 11. Risks / open items

- **NE territory coverage vs seeds/countries.yaml**: `admin_0_countries` folds some
  dependencies into parents (RE/HK/PR/GI may lack own features). The `cmd/citymaps` ISO
  audit surfaces the real list on first run; fallback = switch the fetch to
  `ne_10m_admin_0_map_units` (same schema, splits those units). Decide with audit output in
  hand.
- **Unmatched curated cities without coordinates** (citygen reports them; historically a
  handful): one-time hand patch of lat/lng in the seed, preserved by later regens; the
  unconditional audit keeps the gap visible until fixed.
- **Fact quality**: page-summary extracts are encyclopedic openers, not always
  landmark-flavored; unmatched/disambiguation cases leave some tier 0–2 cities factless
  (caption degrades gracefully). Punchier landmark blurbs (sections API) = later slice.
- **Accept breadth**: name + asciiname + curated exonym only; native-script alternates
  deliberately excluded — revisit on typed-answer complaints.
- **Capital-shadowing in DomainForAnswer** (capital-named cities suggest from the capital
  domain): harmless by construction; documented in the method doc so it isn't rediscovered
  as a bug.
- **PNG-encoder drift across Go versions**: renders reproducible per-binary, not eternally —
  sha256 audits compare disk↔DB from the same render batch; no golden images in git.
