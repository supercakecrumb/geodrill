# Design: language similarity graph

Status: proposal, no code yet.

## Why

Today "close distractors" for the guess-the-language game means one thing: `internal/topics/engine.DistractorPolicy.SameGroup`, gated by whichever of the nine flat buckets in `seeds/decks.yaml` a language happens to sit in (romance, cjk, slavic-latin, slavic-cyrillic, nordic, se-asia, malay-indonesian, indian-scripts — 40 distinct ISO-639-3 codes across them). That's a binary relation: two languages are either "in the same box" (any sibling is a fair distractor) or invisible to each other. It throws away all the structure that actually matters to a learner staring at a sentence: Croatian and Slovenian are much closer to each other than either is to Polish, even though `slavic-latin` treats all five as equally interchangeable; Hindi and Tamil are both dumped in `indian-scripts` despite not sharing a script, a family, or a region.

This doc proposes replacing the flat groups with a **weighted graph**: every language is a node, every pair has a similarity score in `[0, 1]`, and that score drives both smarter distractor sampling now and an interactive graph visualization on the future nerd-stats site/Mini App later. It is a design only — no code changes are made as part of this doc.

## 1. Edge-weight definition

Four named signals feed the score. Three are graded multipliers on a `0..1` scale; the fourth (script) is a **gate**, not an additive term — reasoning below.

- **Family closeness** (`family`) — position in the language family tree (Glottolog/Ethnologue-style): `1.0` same branch (e.g. both East Slavic: Russian/Ukrainian), `0.7` same subfamily different branch (e.g. West Slavic vs East Slavic), `0.3` same top-level family different subfamily (e.g. Slavic vs Romance, both Indo-European), `0.0` unrelated families (e.g. Finnish/Uralic vs Swedish/Indo-European — note this correctly flags today's `nordic` deck as internally overreaching, since Finnish isn't Germanic at all).
- **Geographic proximity** (`geo`) — coarse named-region match, not lat/long: `1.0` same micro-region (Balkans, Baltics, Benelux...), `0.5` same macro-region (Europe, SE Asia...), `0.0` different continents. Deliberately coarse — see §2.
- **Orthographic/diacritic overlap** (`ortho`) — Jaccard similarity of the set of non-ASCII letters/diacritics each language's standard orthography uses (Polish `ą ć ę ł ń ó ś ź ż` vs Czech `č ď é í ň ř š ť ú ů ý ž` vs Croatian `č ć đ š ž`). This is the signal that matters most for THIS app: the game shows a rendered sentence, and a learner's actual confusion is "these look alike on the page," which correlates with shared letterforms far more directly than with family membership. Indonesian vs Malay — practically a dialect pair, near-identical plain Latin inventory — should score near `1.0` here even before family/geo are considered.
- **Script match** (`script_gate`) — `1.0` identical ISO 15924 script (Cyrl–Cyrl, Latn–Latn), `0.3` related-but-distinct scripts that still share visible glyph repertoire (e.g. Chinese Hanzi/Japanese Kanji — Han-derived, `Hani` vs `Jpan`; or Devanagari/Bengali, both Brahmic abugidas with a family resemblance a learner can half-recognize), `0.05` unrelated scripts (Cyrillic vs Thai vs Hangul vs Latin).

**Formula:**

```
raw   = 0.45 * family(a,b) + 0.15 * geo(a,b) + 0.40 * ortho(a,b)
sim   = raw * script_gate(a,b)
```

Script is multiplicative, not additive, because a continuous blend would waste resolution on cross-script noise that's already effectively excluded from any sane distractor pool (nobody confuses a Thai sentence for a Cyrillic one) — better to spend the `0..1` budget on *within-script* gradations, which is exactly where the current boolean `SameGroup` is too coarse. The `0.05`/`0.3` floors (rather than a hard `0`) keep every pair a real, visualizable edge for §5 rather than a graph with disconnected islands — a learner occasionally *does* get tripped up across scripts (Vietnamese's heavy Latin diacritics next to Polish's, say), and the graph should be able to show that as a thin edge even if the distractor sampler all but ignores it.

Worked examples against the app's actual deck contents: Croatian↔Slovenian (`family 0.7` Western South Slavic, `ortho` high shared-diacritic overlap, `geo 1.0` adjoining, same script) lands near `0.9` — correctly much tighter than Croatian↔Polish (`family 0.7`, `ortho` low, `geo 0.5`) at roughly `0.5`. Chinese↔Japanese (`family 0.0` — Sino-Tibetan vs Japonic isolate — `script_gate 0.3` for shared Han glyphs) lands low-but-nonzero, correctly weaker than the game's `cjk` bucket implies; Japanese↔Korean (`family 0.0`, `script_gate 0.05` — Hangul shares nothing visually with Kana) lands near-zero, flagging that `cjk` is really "three unrelated writing traditions bundled for convenience," not a linguistic cluster. Within `indian-scripts`, Hindi↔Marathi (same script, same Indo-Aryan branch, same region) scores near `1.0` while Hindi↔Tamil (Indo-Aryan vs Dravidian, Devanagari vs Tamil script) scores near `0.1` — the graph immediately does what the flat 9-language bucket can't.

## 2. Data source

Three options considered:

- **Hand-curated edges** — a `seeds/language_graph.yaml` of `(a, b, weight)` triples. Rejected as the primary source: pairwise authoring is `O(n²)` (40 languages → 780 pairs) and every newly ingested language needs its edges hand-written against every existing one.
- **Fully computed from the ingested Tatoeba corpus** — character n-gram Jaccard between each language's sentence corpus already in the DB. Attractive (fully automatic, adapts to new languages) but noisy alone: n-gram overlap also picks up unrelated lexical/loanword similarity, and quality depends on having enough ingested sentences per language, which isn't verified here.
- **Hybrid (recommended)** — hand-seed per-language **attributes**, not per-pair edges, then compute every pairwise weight programmatically from the formula above. This is `O(n)` authoring effort (one entry per language: family path, script, region, diacritic-letter set) instead of `O(n²)`, and it mirrors the app's existing "seed yaml → topic/items" pattern (`seeds/decks.yaml` → `guesslang.Seed`) just one layer removed — a `seeds/languages.yaml` of attributes feeds a pure `Similarity(a, b)` function instead of feeding topic rows directly.

Effort/honesty: family, script, and letter-inventory are reliable, low-effort, and mostly static (Glottolog-grade facts, ~1-2 hours to write ~41 entries given the clusters already sketched in §1). Geo is deliberately kept at coarse named-region granularity rather than precise coordinates — that precision isn't needed at 41 languages and a wrong micro-region label is cheap to fix later, whereas hand-tuning lat/long for marginal gain isn't worth it now. The corpus-computed n-gram refinement (replacing/blending with the static letter-inventory term) is the honestly-weaker link — it's deferred to Phase 3 (§6) until corpus coverage per language is confirmed sufficient, rather than shipped as v1's basis.

The 41 (40 unique + `ind` duplicated across two decks) languages currently ingested cluster into: Cyrillic (Russian, Ukrainian, Bulgarian, Serbian, Macedonian), Latin-with-heavy-diacritics (Polish, Czech, Slovak, Croatian, Slovenian, the Romance set, Nordic set), CJK (three unrelated scripts bundled by convenience), Indic Brahmic scripts split across two unrelated families (Indo-Aryan: Hindi/Marathi/Bengali/Gujarati/Punjabi vs Dravidian: Tamil/Telugu/Kannada/Malayalam), and Southeast Asian scripts (Thai, Lao — related Brahmic-derived Tai scripts; Khmer — Brahmic Austroasiatic; Burmese — Brahmic Sino-Tibetan; Vietnamese/Indonesian/Malay — plain Latin, Austroasiatic and Austronesian respectively).

## 3. Storage & shape

Phase 0/1 needs **no new table at all**: 41 languages is 820 unordered pairs, cheap to compute in memory at process startup from `seeds/languages.yaml` attributes and hold as a `map[[2]string]float64` (or a `Neighbors(code string) []Weighted` index) for the distractor sampler to query directly. No migration, no persistence bug surface, degrades trivially (see §4).

Once the nerd-stats site needs to serve the graph over HTTP (Phase 2+), materialize it:

- `languages(code PK, name, family_path text[], script, region)` — the attribute table, upserted from `seeds/languages.yaml` the same way `guesslang.Seed` upserts topics from `seeds/decks.yaml`.
- `language_edges(a, b, weight, components jsonb)` — one row per unordered pair, `components` holding the four named sub-scores (`{"family":0.7,"geo":1.0,"ortho":0.85,"script_gate":1.0}`) so the frontend can render a "why are these close" tooltip (§5) without re-deriving it.

Store **dense**, not sparse: 820 rows is nothing, and storing every pair keeps the write side (a straight re-seed from attributes) simple — push the "only show meaningful edges" decision to the read/render side instead, where a `min_weight` query param or an in-frontend threshold slider is far cheaper to tune than a migration that changes which rows exist.

## 4. Distractor sampling

Today:

```go
type DistractorPolicy struct {
    Max       int
    SameGroup bool
}
```

Successor — additive, not a breaking change:

```go
type DistractorPolicy struct {
    Max       int
    SameGroup bool
    Neighbors NeighborSource // nil = today's flat SameGroup behavior, unchanged
}

// NeighborSource resolves similarity weights for weighted nearest-neighbor
// sampling, restricted to the sibling pool the engine already computed
// (topic-scoped, as today — Neighbors never expands the candidate set,
// only re-weights it).
type NeighborSource interface {
    Weights(answer string, siblings []string) map[string]float64
}
```

Sampling: given the answer's weights over its sibling pool, oversample the top `Max * 3` candidates by weight, then weighted-random-sample `Max` of them without replacement using the engine's existing `*rand.Rand` — biased hard toward close languages, but not the literal same top-K every single time a language is asked about, matching the app's existing "shuffle so it's not identical every time" convention for `Distractors`.

Graceful degradation is the important property: `Neighbors` is opt-in. If the graph isn't built yet, or an item's language code has no attribute-table entry (e.g. a language just added to a deck before `seeds/languages.yaml` is updated for it), `Weights` returns nothing useful for that code and the policy falls back to today's `SameGroup` (or any-sibling) sampling — the graph can ship, and later fill in, without ever being able to break an item into "no valid distractors."

## 5. Nerd-stats visualization

Force-directed graph (`d3-force` / `react-force-graph` on the future website or Mini App webview): nodes = languages (flag + code + name label), edges = pairs above a render threshold, edge thickness/opacity ∝ `weight`. API shape:

```
GET /api/languages/graph?min_weight=0.2
{
  "nodes": [{"code":"rus","name":"Russian","script":"Cyrl","family":"East Slavic"}, ...],
  "edges": [{"a":"rus","b":"ukr","weight":0.82,"components":{...}}, ...]
}
```

Per-user overlay: separately aggregate a user's own wrong-answer pairs from their review/attempt log (which pair of languages they've actually mixed up, and how often) and render those as a highlighted layer on top of the base graph — "here's what the graph says *should* be close" vs "here's what YOU actually confuse," which can diverge in genuinely interesting ways (e.g. a user who confuses Vietnamese/Indonesian — not linguistically close at all, but both plain unaccented-feeling Latin to an untrained eye). Note: this doc didn't verify whether `internal/game`'s attempt log already tracks per-attempt wrong-answer identity at the granularity this needs — flagged as an open question in §7.

Three concrete future features this unlocks:

1. **Language neighborhood explorer** — click a node, see its top-5 neighbors with the `components` breakdown rendered as a human sentence ("shares Cyrillic script and East Slavic branch with Russian"), jump straight into a quiz scoped to just that neighborhood.
2. **Self-improving weights** — aggregate *all* users' wrong-answer pairs against the theoretical graph to find systematic mismatches (pairs the graph underrates that real learners confuse constantly, or vice versa) — a feedback loop for tuning the §1 coefficients from real data instead of my hand-picked `0.45/0.15/0.40`.
3. **Weighted path-finder** — shortest weighted path between two arbitrary languages ("how is Icelandic connected to Bulgarian?") as a fun trivia/exploration feature, and reusable later as an explicit difficulty knob (near-neighbor distractor vs far-neighbor distractor as a selectable game mode).

## 6. Phasing

- **Phase 0 — attribute seed.** Write `seeds/languages.yaml`: ~41 entries of `{code, family_path, script, region, letters}`. No code. Effort: ~1-2 hours (facts, not implementation).
- **Phase 1 — in-memory graph + distractor wiring.** Pure `Similarity(a, b)` function per §1's formula, computed into an in-memory neighbor index at boot, `DistractorPolicy.Neighbors` wired into `guesslang`'s descriptor. No DB migration, no frontend. This is the **minimal first cut** — it's the one that pays for itself immediately by making distractors smarter, and it's small enough to ship standalone ahead of the nerd-stats site. Effort: ~0.5-1 day.
- **Phase 2 — persistence + API.** `languages` / `language_edges` tables, seeded the same way topics are; a read endpoint for the frontend. Effort: ~1 day.
- **Phase 3 — corpus refinement.** Batch job over ingested Tatoeba sentences computing real character n-gram overlap, blended into or replacing the static `ortho` term, stored as an added `components` key. Effort: ~1-2 days, gated on confirming per-language corpus size is adequate.
- **Phase 4 — nerd-stats frontend.** Force-directed render + per-user overlay (§5). Sized by whatever the nerd-stats site's own timeline already is — not estimated here.

Recommended path: do Phase 0 + 1 now (small, immediate distractor-quality win, zero risk given the fallback in §4); defer Phase 2-4 until the nerd-stats site work actually starts.

## 7. Open questions for Aurora

1. Geo granularity — coarse named regions (my default) vs real lat/long centroids? I think coarse is right at 41 languages; flag in case you disagree.
2. The `0.45 family / 0.15 geo / 0.40 ortho` weights and the `1.0/0.3/0.05` script-gate values are my best-guess defaults, sanity-checked against the worked examples in §1 — but they're ultimately a gut call. Worth checking against a handful of pairs you already know are (or aren't) actually confusing in real `@geodriller_bot` usage before Phase 1 ships, if you have that intuition handy.
3. Per-user overlay (§5) needs a per-attempt "what did the user actually pick" log at the (answer, wrong-pick) granularity — I did not check whether `internal/game`'s existing attempt tracking already captures this, since I was scoped to read only `guesslang/seed.go`, `seeds/decks.yaml`, and `engine/descriptor.go` for this doc. Worth a quick look before committing to Phase 4's exact shape.
4. Dense-store/sparse-render for `language_edges` (§3) — confirm no objection to always writing all ~820 pairs and pushing the "meaningful edges only" cut to the read side.
