# Design: Flags quiz (`quiz_kind='flag_country'`)

Architecture-only design for the flags topic (orchestrator brief item C1 / architecture §6 family). Cross-references are to `vibe/geodrill-architecture.md` by section number. No data fill: no country lists, no image URLs, no per-country tiers — those are haiku/data tasks that consume this design.

## 1. Goal

Flag image (or emoji fallback) → country name, single-choice by default. Two designed variants:
- **Set-choice** for visually confusable flag pairs/groups (e.g. Chad/Romania; Indonesia/Monaco/Poland — full list of 10 groups in §5a) — the grading model from architecture §1.6, not a hand-waved "close enough" MCQ.
- **Emoji-only text fallback** when no image asset is ingested yet for an item, so the topic is usable before the media pipeline is fully populated.

Includes GB subdivision flags (England/Scotland/Wales), which are visually distinct from each other and from the Union Flag, so they are ordinary single items — no subdivision belongs to a confusable group.

## 2. Topic tree and quiz_kind

Own root, mirroring roadside's shape (architecture §6.2, `internal/topics/roadside/seed.go`):

```
flags (container, base_tier=0)
└── guess-the-flag (quiz_kind='flag_country', base_tier=0, exercise_modes={single,set,text})
```

`RootSlug="flags"`, `TopicSlug="guess-the-flag"`, `Kind="flag_country"`. No new migration: `topics`/`items`/`content_items`/`media_files` all exist from `000005_v2_core`/`000006_v2_exercises_reviews` (architecture §2.1, §2.2, §2.8) — this is purely a new registry entry + seed data.

## 3. Item shape — two kinds sharing one topic

**Single items** (the common case): one country, `items.country_id` set, `items.key = iso_a2`.

```json
{
  "flag_emoji": "🇫🇷",
  "image": "fr.png",
  "is_subdivision": false
}
```

`image` is a path relative to the media root (§5); empty string means no asset ingested yet for this item — the generator falls back to emoji-only text mode (§6) rather than failing.

**Confusable-group items**: no single country can be asked about unambiguously on its own, so the *group* is the quizzable unit and grading is set-equality (mirrors `specialchars`' subgroup items, architecture §6.1, `internal/topics/specialchars/generator.go` `buildSetMCQ`). `items.country_id = NULL` (payload carries the member list instead — items.country_id is a single optional FK, architecture §2.2), `items.key = "<sorted-iso-codes>"` e.g. `"RO,TD"`.

```json
{
  "countries": ["TD", "RO"],
  "images": ["td.png", "ro.png"],
  "label": "Chad / Romania"
}
```

**Design rule (important):** a country that belongs to a confusable group gets **no standalone single item**. Quizzing "here's Chad's real flag — is it Chad?" as an unambiguous single MCQ would be undecidable from the image alone and teach a false signal. The group item is the only way that country's flag is quizzed. This mirrors how `specialchars` items either claim one language (single) or a subgroup (set), never both for the same letter.

`BuildExercise` picks which member's image to show for a group item via `rng.Intn(len(images))` each time — varying which of the pair/triplet is displayed across repetitions, so the user learns to tell all members apart, not just memorize one image.

## 4. Where data lives — seed file

`seeds/flags.yaml` is filled in: 228 ordinary single items (one per country/subdivision not in a confusable group) plus 10 `confusable_groups` entries covering the remaining 24 countries — 252 total, matching every `iso_a2` row in `seeds/countries.yaml` exactly once. Real shape (see the file's own top-of-file schema comment for the authoritative version):

```yaml
flags:
  - country: FR
    image: fr.png
  - country: GB-SCT       # subdivision, ordinary single item
    image: gb-sct.png

confusable_groups:
  - group: chad-romania
    countries: [TD, RO]
    images: [td.png, ro.png]
    label: "Chad / Romania"
  - group: indonesia-monaco-poland
    countries: [ID, MC, PL]
    images: [id.png, mc.png, pl.png]
    label: "Indonesia / Monaco / Poland"
```

Countries themselves are NOT re-declared here — `flags.country` / `confusable_groups.countries` are ISO A2 codes resolved against the existing `countries` table (architecture §2.6, already seeded by `roadside.Seed`/its `countries.yaml`). Flags seeding only needs `GetCountryByISO` lookups, never `UpsertCountry`. `image`/`images` entries are bare filenames — relative to the media root, which is itself `data/flags/` (see §5), so there is no `flags/` path segment inside the value.

## 5. Media pipeline

**Image source (decided):** [flagcdn.com](https://flagcdn.com/) (operated by Flagpedia.net), PNG at the `w320` fixed-width size — e.g. `https://flagcdn.com/w320/fr.png`, `https://flagcdn.com/w320/gb-sct.png`. flagcdn.com additionally exposes the UK constituent-country subdivisions under exactly the codes this project already uses lowercased (`gb-eng`, `gb-sct`, `gb-wls`, plus `gb-nir` which this project doesn't seed), so no separate subdivision source was needed.

**License:** public domain. Flagpedia.net's own License section (`https://flagpedia.net/about`, "License"): *"Flag images are in the public domain (exempt from copyright). They are completely free for non-commercial and even commercial use."* No attribution is legally required (a backlink to flagpedia.net is merely "appreciated"). This is safe to redistribute via a fetch script committed to a public repo — the script only contains a URL pattern and country codes, and the images it fetches carry no copyright restriction.

**Media root (decided):** `data/flags/`, i.e. the existing gitignored `/data/` root (already covering the Tatoeba download cache, `.gitignore` line 2) gets a `flags/` subdirectory — no new `.gitignore` entry needed. This supersedes the originally-sketched separate `media/` directory: reusing `/data/` avoids an extra gitignore rule for what is conceptually the same thing (a local, regenerable, non-versioned asset cache). Full path for a given code: `data/flags/<lowercase-iso2>.png` (e.g. `data/flags/fr.png`, `data/flags/gb-sct.png`).

**Fetch script (built):** `scripts/fetch-flags.sh` — reads every `iso_a2` out of `seeds/countries.yaml` (including the 3 GB-* subdivision rows), downloads `https://flagcdn.com/w320/<lowercase-code>.png` into `data/flags/<lowercase-code>.png` for any code not already present on disk, at 1 request/second with an honest User-Agent identifying the project and a contact URL. A per-file download failure is logged to stderr and the script continues to the next code (never aborts the whole run); a summary (total/downloaded/skipped/failed) prints at the end. Run once already: all 252 codes downloaded successfully (0 failures) — `data/flags/` is populated locally (~1.1 MB) for the downstream Go worker to test the media pipeline against real files. Re-running is a no-op (idempotent skip-if-present).

**Still open for the Go worker (a new `cmd/flagassets` tool, isolated per architecture §8's conflict-avoidance rule)** — this data task only populates the local disk cache; DB ingestion is unbuilt:
1. For each seed-file image path, compute `sha256` of the on-disk file under the media root (`data/flags/`).
2. `store.InsertContent`-equivalent for v2: create a `content_items` row (`kind='photo'`, `topic_id=<guess-the-flag topic id>`, `key=<iso_a2 or group key>`, `payload=<relative path>`, architecture §2.8).
3. `store.PutMediaFile(ctx, &contentID, localPath, sha256, width, height, bytes)` (already implemented, `internal/storage/media.go`) — upserts keyed on `local_path`, idempotent.
4. `telegram_file_id` starts empty; it is populated lazily.

### 5a. Confusable groups (10, 24 countries)

Beyond the two named in the original sketch, this task's research pass added a few more well-known look-alike sets, keeping every country in at most one group (required by the §3 exclusivity rule and the §8 audit):

| Slug | Members | Why confusable |
|---|---|---|
| `chad-romania` | TD, RO | Near-identical blue/yellow/red vertical tricolour, differing only in shade |
| `indonesia-monaco-poland` | ID, MC, PL | Plain red/white horizontal bicolour, differing only in stripe order (ID/MC red-over-white vs PL white-over-red) |
| `netherlands-luxembourg` | NL, LU | Red/white/blue horizontal tricolour, differing only in blue shade and proportions |
| `ireland-cote-divoire` | IE, CI | Green/white/orange vertical tricolour, mirror-image stripe order |
| `norway-iceland` | NO, IS | Nordic-cross flags, both red/white/blue, differing only in cross proportions |
| `pan-slavic-tricolour` | SI, SK, RU | White/blue/red horizontal tricolour family; SI/SK add a small coat of arms easy to miss at a glance |
| `australia-new-zealand` | AU, NZ | Blue ensign with Union Jack canton plus stars, differing only in star count/color and Commonwealth Star |
| `pan-african-green-yellow-red` | SN, ML, CM | Green/yellow/red vertical or horizontal pan-African tricolour, differing only in emblem/stripe orientation |
| `andean-tricolour` | CO, EC, VE | Yellow/blue/red horizontal tricolour (Gran Colombia lineage), differing only in band proportions and coat of arms |
| `rio-de-la-plata-sun` | AR, UY | Light-blue/white horizontal bands with a golden Sun of May, differing only in band count/layout |

**Telegram file_id caching (a small existing-code gap to flag for the implementer):** today `Session.SendPhoto` (`internal/telegram/bot.go`) always sends via `telebot.FromDisk(path)` — it never consults or writes `media_files.telegram_file_id`. Flags is the first topic that actually needs the "cached on first send" win (task brief, architecture §2.8/§6 decision 6), so this wave should extend `SendPhoto`/`EditCaption` (or wrap them) to: look up `GetMediaByLocalPath`, send via a cached Telegram file_id when present (telebot supports sending an `&telebot.Photo{File: telebot.File{FileID: id}}`), otherwise send from disk and call `SetMediaTelegramFileID` with the resulting `msg.Photo.FileID`. This touches `internal/telegram/bot.go`, which is outside this design doc's own file scope (`vibe/` only) — call it out as a required follow-up task for whichever wave wires flags into `cmd/bot`.

## 6. Generator behavior per mode

`Generator` (package `internal/topics/flags`) is constructed with the media root path injected (`flags.New(mediaRoot string)`) — a plain config value, not a DB-backed dependency, so it doesn't need the `ContentSampler`-style interface the `internal/topics` package doc describes for DB-backed needs.

- **ModeSingle** (single items, `p.image != ""`): `Exercise.MediaPath = filepath.Join(mediaRoot, p.Image)`, options = target country name + up to 3 distractors drawn from sibling single items (`req.Siblings`, excluding any item that is itself a confusable-group member — avoids accidentally offering an indistinguishable distractor). Prompt/caption: `"Which country is this?"`.
- **Emoji-only text fallback** (single items, `p.image == ""`): no `MediaPath` set; `Exercise.Prompt = fmt.Sprintf("%s — which country is this?", p.FlagEmoji)`, same options/grading as ModeSingle. This is a *content-availability* fallback, not a user-selectable mode — the generator picks it automatically per item.
- **ModeSet** (group items): `Exercise.MediaPath` = the chosen member's image path (rng-picked per §3); `OptionSets` = the target set (`quiz.CanonSet(p.Countries...)`, label from `p.Label`) plus up to 3 distractor sets built by the same swap-one-member algorithm as `specialchars.buildDistractorSets` (`internal/topics/specialchars/generator.go` — reuse the pattern, pool = other group items' member countries plus a handful of non-group singleton "sets"). Prompt: `"Which countries could this flag belong to?"`.
- **ModeText**: available on single items only (group items are set-choice-only, mirroring `specialchars`' subgroup restriction); `Accept` = country's English name + ISO A2/A3 codes; `CorrectAnswer` = the English name.

Every country label — button, prompt, confirmation — is prefixed with its flag emoji per architecture §5.1/decision 6, using `storage.Country.FlagEmoji` (already populated for subdivisions as Unicode tag sequences, architecture §2.6/§9.4).

## 7. Tier mapping rule

Reuses the architecture §4 rubric's **Flags** column directly: tier 0 = 🇺🇸🇬🇧🇫🇷🇯🇵 + the user's own flag (not derivable generically — a per-user override, out of scope for the seed-time tier, so seed-time tier 0 is just the fixed big-4 + UN P5 baseline and a `/settings`-driven "always tier 0" override is a future affordance, not this doc's concern); tier 1 = UN P5 + big EU; tier 2 = all UN members; tier 3 = subdivisions + major dependencies; tier 4 = obscure dependencies/historical.

**Confusable-group items take the MAX (harder) tier among their members.** Rationale: a set-choice question only meaningfully tests discrimination once the user would otherwise be expected to know *both* candidates individually — gating on the easier member's tier would surface "Chad or Romania?" before the user has ever seen Romania's flag at all.

Recommend factoring the per-country tier function into a small shared helper (not a new package for this doc alone — see `vibe/design-country-profiles.md` §6, which proposes `internal/topics/countrytier` for exactly this recurring need across flags/profiles/TLDs). Flags-specific note: subdivisions are always tier 3 regardless of the shared helper's generic UN-member logic (subdivisions have no UN membership at all), so `flags`' own tier function wraps the shared helper with a `if item.is_subdivision { return 3 }` short-circuit.

## 8. Audit method

`internal/topics/flags/audit_test.go`, gated on `FLAGS_AUDIT_MEDIA_ROOT` (skip if unset — the media root is gitignored, so CI without it configured shouldn't fail) plus the standard `GEODRILL_TEST_DATABASE_URL` fuse:
1. Every single item's `payload.image` (when non-empty) resolves to a file that exists under the media root, and its `media_files.sha256` matches a fresh hash of that file (catches stale/corrupted or renamed assets).
2. No country ISO code appears in both a single item AND a confusable-group item's `countries` list (the §3 exclusivity rule).
3. Every confusable-group's `countries` entries are valid, resolvable ISO A2 codes in the `countries` table.
4. `len(payload.images) == len(payload.countries)` for every group item (one image per member, no dangling references).

## 9. Files to create

- `internal/topics/flags/{seed.go,generator.go,seed_test.go,generator_test.go,audit_test.go,integration_test.go}`
- `internal/topics/countrytier/tier.go` (shared helper, if not already created by whichever of these five docs lands first — see §7)
- `cmd/flagassets/main.go` (isolated ingest tool, §5 — DB ingestion, not yet built)
- `seeds/flags.yaml` — **done**: 228 single items + 10 confusable groups (§4, §5a)
- `scripts/fetch-flags.sh` — **done**: idempotent flagcdn.com PNG fetcher (§5)
- No `.gitignore` change needed — `data/flags/` is already covered by the existing `/data/` rule

No new migration.

## 10. Verification commands

```
go test ./internal/topics/flags/...
GEODRILL_TEST_DATABASE_URL=postgres://…/geodrill_test go test -p 1 ./internal/topics/flags/...
FLAGS_AUDIT_MEDIA_ROOT=./media GEODRILL_TEST_DATABASE_URL=postgres://…/geodrill_test go test ./internal/topics/flags/... -run Audit
```
