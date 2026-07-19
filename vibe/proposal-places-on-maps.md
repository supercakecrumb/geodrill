# Proposal: places-on-maps — map-based questions for geodrill

Research + proposal only (board card "Places-on-maps foundation — Phase 4", 2026-07-19). No code, no schema, no topic in this doc — it grounds a decision Aurora makes separately. Builds on `vibe/design-cities.md` (map-position mode already speculatively designed there), `vibe/design-rivers-mountains.md` (optional map media for landmarks), `vibe/design-flags-quiz.md` (the proven photo/media pipeline), and `vibe/adding-topics.md` (topic conventions, the engine).

## 1. Coordinates data model

**What to store.** Two shapes cover every map mechanic considered here:

- **Country**: centroid (`lat`, `lng`) + bounding box (`min_lat`, `min_lng`, `max_lat`, `max_lng`). Centroid places a marker or centers a map; bbox drives zoom-to-fit and proximity-scoring normalization (a "close" guess means something different for Russia than for Monaco).
- **City**: a single `lat`/`lng` point. No bbox needed — cities are quizzed as points, not regions.

**Where it fits the existing schema.** Per `adding-topics.md` and the five design docs, geodrill's convention is jsonb payload first, first-class tables only when more than one consumer needs to join against the data relationally (this is exactly `design-cities.md` §2's justification for not giving cities their own table). Coordinates fit the same pattern:

- **Cities** (`vibe/design-cities.md`, item = `items` row, topic `cities`/`which-city`): add `lat`/`lng` to the existing `payload` json shape (`{"lat": 48.8566, "lng": 2.3522, ...}` alongside `elevation_m`, `terrain`, `map_image`, etc.). No new table, no migration — this is an additive payload field on a topic already designed to carry exactly this kind of per-item fact.
- **Countries**: `countries` is already first-class (architecture §2.6, joined by 5+ topics). Coordinates here are a genuine schema decision, not a payload add-on, because every topic that already joins `countries` (roadside, flags, profiles, TLDs, rivers/mountains) could benefit from centroid/bbox for free once it's a real column — this is the one case in this whole proposal where promoting to a column beats payload-only, since the "more than one consumer" bar is already cleared by the existing `countries` table's own justification. Two new nullable columns on `countries` (or a `country_geo` 1:1 side table if Aurora prefers not to touch the core table): `centroid_lat`, `centroid_lng`, `bbox_min_lat`, `bbox_min_lng`, `bbox_max_lat`, `bbox_max_lng`.
- **Rivers/mountains** (`vibe/design-rivers-mountains.md`): out of scope for a *point* coordinate (a river isn't a point), but the optional map-media enhancement that doc already sketches (§7, "generated map image highlighting the river/mountain") would need a path/polyline shape, not a single lat/lng — flagged here as a harder, separate data problem, not solved by this proposal's country/city coordinate model.

**Seed shape.** Two options, and this proposal recommends the second:

1. A dedicated `seeds/coordinates.yaml` keyed by ISO code / city key, loaded as a second pass that patches `items.payload`/`countries` columns after the owning topic's own seed has run.
2. Coordinates folded directly into the existing `seeds/countries.yaml` and `seeds/cities.yaml` as new fields (`centroid_lat`/`centroid_lng`/bbox on countries; `lat`/`lng` on cities) — no new file.

**Recommended: option 2.** A country's centroid is a property of the country, not a separate concern from its name/ISO code/flag emoji already in `seeds/countries.yaml`; splitting it into a parallel file just means every consumer has to join two seed files by ISO code for no benefit. Cities already have a payload-shaped record in `seeds/cities.yaml` (`design-cities.md` §4) — `lat`/`lng` are two more fields on the same entry, following the same pattern as `elevation_m`. A standalone `coordinates.yaml` only earns its keep if coordinates get their own independent lifecycle (re-fetched/re-audited on a different cadence than the rest of the country/city data) — not the case here.

**Data sources for coordinates:**

| Source | Coverage | License | Fit |
|---|---|---|---|
| **Natural Earth** (`naturalearthdata.com`, populated-places + admin-0 layers) | Country centroids (admin-0), major city points | Public domain — no attribution, no commercial restriction, explicit permission to modify/redistribute | Best fit for country centroids; this is also restcountries.com's own upstream source for `latlng` |
| **GeoNames** (`geonames.org` dumps, `cities500`/`cities1000`/`cities5000` extracts) | ~450 cities already seeded (`seeds/cities.yaml`) map cleanly onto GeoNames' `cities5000`/`cities15000` extracts by name+country | CC BY 4.0 — attribution to GeoNames required in the seed file's header comment or a `SOURCES.md`, not per-record | Best fit for city lat/lng — far deeper coverage than Natural Earth's populated-places layer, and it's the same source restcountries.com uses for capital coordinates |
| **restcountries.com API (v5)** | Country centroid (`latlng` field) + bbox is *not* directly exposed (would need deriving from Natural Earth's own admin-0 polygon bounds separately) | Aggregates public-domain/CC0 sources (Natural Earth, Wikidata) per their own docs, clean for commercial reuse | Convenient single fetch for country centroids only; still Natural-Earth-sourced under the hood, so no licensing advantage over going direct, but less parsing (JSON vs shapefile) |

**Recommendation:** country centroids via a one-time `restcountries.com` fetch (JSON is trivial to compile into `seeds/countries.yaml`, same "haiku-compiled" workflow as the existing capitals/cities seeds); bbox computed from Natural Earth's admin-0 shapefile bounds (a one-time offline extraction, not a runtime dependency) since restcountries doesn't expose it; city lat/lng from GeoNames' `cities5000.zip`/`cities15000.zip` extract, matched against the already-seeded 453 cities by name+ISO, with a GeoNames attribution line added wherever the project already documents data provenance (flags' `SOURCES`-style note in `design-flags-quiz.md` §5 is the precedent to follow). None of this needs a live API call at runtime — it's a one-time data-compilation task like every other seed file in this project.

## 2. Map rendering options — honest comparison

Three fundamentally different approaches. All three can send the end result through the **existing photo pipeline** (`content_items`/`media_files`, `telegram_file_id` caching, `Session.SendPhoto` — proven by flags, `design-flags-quiz.md` §5/§9) *except* the Mini App path, which is a different UI paradigm entirely (a webview, not a chat photo message).

### (a) Generated static map images, rendered server-side

Go has one well-known library for this: **`flopp/go-staticmaps`** (MIT license, built on `fogleman/gg` for 2D drawing) — fetches raster tiles from a configured provider, draws markers/paths on top, outputs a PNG. This is a rendering *library*, not a tile *source* — it still needs tiles from somewhere, which is where the real decision and the real risk live:

- **OSM's own tile server (`tile.openstreetmap.org`)** — the tile-licensing trap this proposal was asked to flag explicitly. As of the current OSMF tile usage policy: bulk/offline/prefetch patterns are banned, "headless bots that pan/zoom to force rendering" are explicitly called out (which is *exactly* what a server rendering static maps on demand does), and access "may be withdrawn at any point, without notice" — with commercial or donation-soliciting services singled out as especially exposed. **This is not a viable production tile source for geodrill** even though `go-staticmaps` supports it by default; using it risks an IP ban with no warning.
- **Self-hosted tile rendering** (own OSM-data-derived tile server, e.g. via `openstreetmap-carto` + a rendering stack, or a pre-rendered tile cache for the ~450 cities/250 countries actually needed) — fully compliant, since OSM's own data is public domain / ODbL-attributed and free to self-host from. Real infra cost: a tile-rendering stack (mapnik or similar) is heavier than anything else in geodrill's current single-Go-binary-plus-Postgres footprint, and is arguably overkill when the actual need is "a few hundred distinct point locations," not general-purpose interactive tiles.
- **Third-party tile source configured into `go-staticmaps`** (the library ships adapters for Thunderforest, OpenTopoMap, Stamen, Carto, most requiring an API key) — this collapses into option (b) below in practical terms (you're now managing a third-party API key and its quota/ToS), just with an extra rendering step in front of it.
- **A vector-tile source rendered offline once per item** (e.g. MapLibre's style + a headless renderer, or `OpenFreeMap`'s explicitly-unlimited-free vector tiles) — the licensing-cleanest *and* infra-lightest option in this family: OpenFreeMap tiles are free with no attribution/key requirement, and because geodrill's maps are for a **fixed, small, known set of ~450 city points + ~250 country centroids**, the "render on demand" problem shrinks to "render once, cache forever" — this is a batch job, not a live service.

**License summary for (a):** viable only if the tile *source* is either self-hosted, OpenFreeMap-style unlimited-and-free, or a licensed third-party key — never OSM's own tile.openstreetmap.org for anything beyond a handful of manual test renders.

### (b) Third-party static-map APIs

| Provider | Free tier | Paid pricing | API key handling | Caching/ToS note |
|---|---|---|---|---|
| **Mapbox Static Images** | 50,000 images/month | ~$1.00/1,000 beyond free tier, volume discounts | Secret key server-side | Standard commercial ToS; no unusual caching restriction found |
| **Geoapify Static Maps** | Credit-based free tier (reported ~3,000 credits/day on some plans); commercial use allowed on free tier | Pay-as-you-go beyond free credits | API key server-side | Requires visible Geoapify attribution on free tier |
| **MapTiler Static Maps** | **None** — static maps API is excluded from the free plan entirely | Flex plan from $25/month minimum just to unlock the endpoint | API key server-side | N/A — must be paid from day one |
| **Stadia Maps Static Maps** | 2,500 credits/month free (~20 credits/static-map request → ~125 free maps/month); free tier is non-commercial/eval only | Paid plan required for commercial use and for the right to *cache* generated images long-term | API key server-side | Free tier explicitly bars commercial use; caching rights (needed for geodrill's `media_files` cache-forever pattern) require a paid subscription |

**Fit with geodrill's media pipeline:** any of these slot cleanly into the existing pattern — fetch once, store under the media root, `content_items`/`media_files` row, cache the `telegram_file_id` after first Telegram send (same as flags). Because the target set is small and fixed (~450 cities, ~250 countries), a one-time fetch-and-cache script (mirroring `scripts/fetch-flags.sh`) means ongoing API usage is near-zero after the initial run — monthly free tiers comfortably cover a single backfill even at Stadia's lower limit. The real constraint isn't request volume; it's **commercial-use and caching-rights terms**, since geodrill is a public repo/bot (Stadia's free tier explicitly excludes this; Geoapify's free tier explicitly allows it with attribution).

**Recommendation within (b), if this path is chosen:** Geoapify — free tier explicitly permits commercial use, attribution requirement is cheap to satisfy (a credit line in `/help` or a caption fallback), and per-request cost if ever exceeded is modest.

### (c) Telegram Mini App interactive map (Leaflet/MapLibre)

The richest UX — tap or drag a pin, live pan/zoom, actual "click the location" gameplay — but it is **Phase 7 work, not additive to it**: it requires the Mini App's HTTPS infrastructure (`internal/api`, initData HMAC auth, TLS via caddy — all listed as not-yet-built in the kanban's Phase 7 card) before a single map pixel can render. For 2026, MapLibre GL JS (WebGL, vector tiles, no API key with a free vector source like OpenFreeMap) is the modern default over Leaflet; Leaflet remains viable for a simpler pin-only view and is lighter inside a Telegram WebView on low-end devices. `maplibre-gl-leaflet` exists as a bridge if both APIs are wanted. Either way, tile sourcing for the Mini App has the same free options as (a)'s vector-tile branch (OpenFreeMap, MapTiler free tier for non-static use).

### Comparison table

| | (a) Generated static images | (b) Third-party static-map API | (c) Mini App interactive map |
|---|---|---|---|
| **Infra needed** | Rendering lib (`go-staticmaps`) + a compliant tile source | None beyond an API key | Full Mini App stack (HTTPS, initData auth, frontend) |
| **Cost at geodrill's scale (~700 fixed points)** | ~$0 (self-render once, cache forever) | ~$0 (one-time backfill fits any free tier) | ~$0 marginal, but Phase 7 infra is a real one-time cost |
| **Offline-ability** | Yes, once rendered | Yes, once fetched | N/A (live interactive) |
| **Licensing risk** | High if OSM tile.openstreetmap.org used directly; low with OpenFreeMap/self-host | Low–medium (read each provider's commercial/caching terms) | Low (same tile options as (a)) |
| **Fits existing photo pipeline as-is** | Yes | Yes | No — different UI paradigm entirely |
| **UX richness** | Static image, MCQ-style questions only | Static image, MCQ-style questions only | Full interactive: drag pin, live distance feedback |
| **Blocked on Phase 7?** | No | No | Yes |
| **Dev effort (this proposal's estimate)** | Small–medium | Small | Large (shared with Phase 7's own cost, not incremental to it) |

**Verdict:** (a), specifically the self-rendered/OpenFreeMap-tile-cache branch, is the only option that is simultaneously cheap, licensing-clean, and ships with zero new infrastructure beyond what flags already proved out. (b) is a legitimate close second if Aurora would rather pay a small, bounded, one-time cost than stand up any rendering code at all — Geoapify specifically. (c) is strictly better UX but is not an incremental cost on top of (a)/(b); it's riding on Phase 7's own infrastructure bill.

## 3. Question mechanics

| Mechanic | Description | Rendering option required | Reuses |
|---|---|---|---|
| **(a) Map-with-marker → name the place** | Show a generated/fetched map image with a pin on a city or country; MCQ or autocomplete answer | (a) or (b) — any static image | Autocomplete answer mode (already shipped, `spike-autocomplete-inline.md`); this is `design-cities.md`'s already-speculated map-position mode, now with a real coordinate to place the marker at instead of a placeholder |
| **(b) "Click the location"** | Player taps/drags a pin on a live map; distance-to-target is the score | (c) only — needs a real map surface to click on | Phase 7 infra; not buildable before it |
| **(c) Region/continent multiple-choice** | No map image at all — "Which continent is Chad in?" as a plain MCQ, using the country's `region`/continent fact | None — text-only fallback | `country_facts`/`region` (already seeded via `seeds/country_profiles.yaml`, per `design-country-profiles.md`) — ships today, zero new infra |
| **(d) Proximity scoring** | GeoGuessr-style: player's guess (a pin, or a picked country/city) is scored by real distance to the true coordinate, not binary right/wrong | Needs real lat/lng on both the target and the guess — pairs naturally with (b) for a pin-drag guess, but can also work with (a)+autocomplete if the *guessed* city/country's centroid is looked up and compared to the target's centroid (distance-banded score: "🔥 within 50km" / "close: within 500km" / etc.) | The country/city coordinate data from §1 — this is the one mechanic that actually needs coordinates to exist beyond "a pixel position on one rendered image"; (a)/(c) don't strictly need real coordinates at all, since a pre-rendered marker position is enough for MCQ grading |

Mechanic (a) is the only one that meaningfully needs *both* a rendered map *and* real coordinates together (marker placement); (c) needs neither map rendering nor precise coordinates (region is a categorical fact); (d) needs coordinates but not necessarily a rendered image if paired with autocomplete-guess-then-compute-distance instead of a draggable pin.

## 4. Recommended path + phasing

**Phase A (minimal first cut) — static generated map images + existing photo pipeline + autocomplete answers.**

- Compile country centroid/bbox (§1: restcountries.com fetch + Natural Earth bbox extraction, one-time) into `seeds/countries.yaml`; compile city lat/lng (GeoNames match) into `seeds/cities.yaml`.
- Render once, offline, using `go-staticmaps` against an OpenFreeMap-style unlimited free vector tile source (or a self-hosted tiny tile cache for just the ~700 needed points) — a batch script mirroring `scripts/fetch-flags.sh`'s idempotent-skip-if-present pattern, writing into a gitignored media root.
- Ingest via the same `content_items`/`media_files` pipeline flags already proved (a small `cmd/citymaps`-style tool, already flagged as needed in `design-cities.md` §9).
- Ship mechanic (a) (map-with-marker) for cities first (`design-cities.md`'s map-position mode, now unblocked) and add a country-centroid marker mode for capitals/country topics as a bonus.
- Ship mechanic (c) (region MCQ) essentially for free — the data already exists, it just needs a generator, no map work at all — as a genuinely no-image fallback usable even where a city/country has no map image ingested yet (same "content-availability fallback" convention as flags' emoji-only mode and cities' terrain-fallback mode already establish).
- **No Mini App dependency.** Everything here rides the existing bot/photo/autocomplete surfaces.
- **Effort estimate:** small–medium — most of the work is the one-time data compilation (§1) and the one-time rendering batch job, both bounded by the ~700-point fixed target set; the generator code itself is a thin variant of `design-cities.md`'s already-designed map-position mode.

**Phase B (proximity scoring) — layer onto Phase A, no new infra.**

- Once real coordinates exist (Phase A), add distance-banded scoring to the autocomplete-guess flow: player picks a city/country by name, geodrill computes true distance between guessed and target centroids, scores/labels it GeoGuessr-style ("🎯 exact" / "🔥 <50km" / "close: <500km" / "way off").
- **Effort estimate:** small — pure application logic (haversine distance) on top of data that already exists after Phase A; no rendering, no new mechanic, just richer feedback on the existing map-with-marker question.

**Phase C (Mini App interactive map) — rides Phase 7, not incremental to it.**

- Once Phase 7's Mini App HTTPS/auth infra exists for other reasons (the nerd-stats dashboard, topic management), add a MapLibre-based "click the location" screen reusing the same coordinate data compiled in Phase A.
- **Effort estimate:** large, but shared with Phase 7's own bill — this proposal does not recommend building Mini App infra *for* maps alone.

**Main risk, stated plainly:** the tile-licensing gotcha. It is tempting to reach for `go-staticmaps`'s default OSM tile source because it needs zero configuration — that is precisely the path OSMF's tile usage policy prohibits for a server-side, non-interactive, would-be-production use pattern like this one. The safe default is OpenFreeMap (free, unlimited, no key) or a tiny self-hosted render for the fixed point set; a paid third-party API (Geoapify) is the fallback if self-rendering proves more fiddly than expected.

## 5. Timed city-listing game + coins (sketch)

Board Backlog "Future-future" card: player types as many cities of a given country as possible against a clock; autocomplete-assisted; dedup; per-city scoring; coins toward a future economy.

**How it sits on top:**

- **Game zone fit** — this is a game, not FSRS material, exactly like Language Roulette (`design-game-zone.md`): ephemeral session, only aggregate stats persist, slots into the existing `/game` menu alongside Language Roulette. It would reuse the established `game_stats` table shape (`user_id`, `game` key, best-score-style aggregate columns, `last_played_at`) — a new `game` value (e.g. `city_listing`) rather than a new table, following the precedent that the table was designed to hold more than one game's stats.
- **Data reuse** — the cities dataset (`seeds/cities.yaml`, already 453 cities / 165 countries) is exactly the corpus needed: "list cities of 🇧🇷 Brazil" just filters `items` by `country_id` within the cities topic. No map rendering or coordinates needed for this game at all — it's a pure recall/typing exercise, not a map mechanic, which is why it's a sketch here rather than folded into §3/§4: it doesn't actually depend on this proposal's coordinate work shipping first, only on the cities dataset that already exists.
- **Mechanics sketch** — round starts, country picked (random or player-chosen, weighted by how many cities are seeded for it so a country with 2 seeded cities isn't a frustrating round); player types into the existing autocomplete/inline-query surface; each accepted, non-duplicate city name within the time limit scores points (simplest: flat point per city, or population-rank-weighted so naming an obscure city scores more than the obvious capital); clock runs out, final tally shown with best-run comparison (mirrors Language Roulette's "final streak, personal best" pattern).
- **Coins** — this sketch treats coins as *out of scope for design here*: the card itself says "what coins buy: TBD," and this proposal isn't the place to invent an economy. The only forward-compatible design note: award coins as a simple per-round integer alongside the existing `game_stats` aggregate (a `total_coins` column, or a running balance on the user row) so the *earning* side is ready whenever the *spending* side gets designed — don't build a currency system prematurely.
- **What it needs vs. the maps work** — nothing from §1–§4. It needs the cities dataset (done) and the game-zone pattern (done, proven by Language Roulette). It is genuinely independent of whichever map-rendering path Aurora picks — flagging this explicitly so it isn't seen as blocked on this proposal's outcome.

## 6. Open questions for Aurora

1. **Tile source / rendering path — self-render (OpenFreeMap or self-hosted) vs. pay for a third-party static-map API (Geoapify) vs. wait for Mini App and skip static images entirely?** This proposal recommends self-rendering as the cheapest, licensing-cleanest path, but it is real (if small) new Go code (`go-staticmaps` + a batch ingest tool) vs. Geoapify being closer to zero new code for a small ongoing cost risk.
2. **Is proximity/distance-banded scoring (§3d, §4 Phase B) wanted at all, or is MCQ/autocomplete-exact-match enough?** It's cheap to add once coordinates exist, but it's a real design decision about how "correct" is scored — binary vs. graduated — that changes the feel of the quiz, not just its implementation.
3. **Country bounding boxes: worth compiling now, or defer until proximity scoring (Phase B) is actually greenlit?** Centroids alone cover mechanic (a) and city-level (d); bbox only earns its keep for normalizing "how close is close" per-country in Phase B — if Phase B isn't approved yet, bbox compilation can be deferred without blocking Phase A.
4. **Does the timed city-listing game (§5) get built now (it's independent and cheap) or stay parked as "future-future" per the board's own framing?** It doesn't block or depend on anything in §1–§4, so it's a genuinely separate go/no-go, not sequenced by this proposal.
