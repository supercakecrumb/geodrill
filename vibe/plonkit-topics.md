# Design: plonkit.net tier-5 meta topics (prototype findings)

Written from the actual output of `cmd/plonkit` (task W5.5), run against a live 5-country
sample (NL, DE, FR, GB, JP) on 2026-07-18. Every number and structural claim below comes
from that run or from direct inspection of plonkit.net's own code/markup — nothing is
invented. Cross-references are to `vibe/geodrill-architecture.md` by section number.

## 0. Read this first — robots.txt and the scrape's authorization

The task brief states Aurora has the plonkit.net developer's explicit permission to scrape
and reuse this content, and instructed the scraper to "honor robots.txt disallow paths as a
courtesy." Doing that courtesy check surfaced something worth Aurora's attention before any
larger run: plonkit.net's robots.txt (fetched 2026-07-18) is not a simple per-path disallow
list. It has **two separate `User-agent: *` groups**:

```
User-agent: *
Content-Signal: search=yes,ai-train=no,use=reference
Allow: /

User-agent: Amazonbot / Applebot-Extended / Bytespider / CCBot / ClaudeBot /
            CloudflareBrowserRenderingCrawler / Google-Extended / GPTBot / meta-externalagent
Disallow: /
                                                            [... END Cloudflare Managed Content]
User-agent: Googlebot / Bingbot / DuckDuckBot
Allow: /

User-agent: *
Disallow: /
```

Two things stand out. First, one of the named blocks is literally `User-agent: ClaudeBot` /
`Disallow: /` — Anthropic's own crawler is explicitly blocked. Second, the site's own
(non-Cloudflare-managed) block ends with a default-deny `User-agent: *` / `Disallow: /`,
allow-listing only three named search engines above it.

`cmd/plonkit` does not identify as `ClaudeBot`, Googlebot, or any other named bot — its
honest, distinct User-Agent is `geodrill-plonkit-scraper/0.1 (+github.com/supercakecrumb/
geodrill; authorized by site owner)`, so it never matches those specific groups. Per RFC 9309
§2.2, only the two `*` groups apply to it; `robots.go` merges them (see its doc comment) and,
on the standard tie-break ("the least restrictive rule must be used"), resolves to *allowed*.
That is the technically correct reading, not a convenient shortcut — but it's also genuinely
ambiguous, since a naive "last rule wins" reading of the same file would say the opposite.
This tool doesn't spoof a browser or a search bot to get around the block either way.

**Recommendation:** given the explicit `ClaudeBot` disallow and the default-deny posture for
everyone else, Aurora should reconfirm with the plonkit.net developer that a low-volume,
rate-limited, honestly-identified scrape for this specific purpose is still fine before any
full-scale (all-countries, recurring) run — the out-of-band permission and the site's public
robots.txt are in tension, even though they aren't necessarily contradictory (a site can
default-block generic crawlers while personally authorizing one specific person/tool).

## 1. What kind of site this actually is

plonkit.net is a client-rendered React SPA (Vite build) — a plain GET of e.g. `/netherlands`
returns `<div id="root"></div>` and a script tag; there is no separate JSON API endpoint the
client fetches from. **But** every guide route is pre-rendered server-side: the full page
payload ships inline in the initial HTML as

```html
<script id="__PRELOADED_DATA__" type="application/json">{...}</script>
```

So no headless browser is needed — a plain rate-limited `net/http` GET plus a regex to pull
that script tag's contents (`cmd/plonkit/guide.go`) is enough. This was the single most
important structural discovery: an initial attempt to grep the rendered page text or the JS
bundle for country names found nothing, because the bundle is generic and the rendered DOM is
empty until React hydrates — the data only exists in that one inline script tag, which a
naive "parse the visible HTML" approach would have missed entirely.

The country-guide URL pattern is a bare top-level slug (`/netherlands`, `/germany`, ..., not
`/guide/nl`). `sitemap.xml` marks exactly where these begin with a literal
`<!-- Guide Pages -->` XML comment; everything after it (138 entries as of 2026-07-18) is a
guide page. `cmd/plonkit` uses that marker to discover the guide index generically
(`discoverGuideIndex` in `sitemap.go`) rather than hardcoding which slugs exist.

## 2. The real category taxonomy

plonkit's frontend enforces a fixed tip-category enum via a Zod schema, found directly in its
JS bundle:

```
["architecture", "bollard", "chevron/sign", "coverage", "guardrail", "important",
 "landscape", "language", "license plates", "moving info", "pole", "roadline", "vegetation"]
```

This is the **authoritative** taxonomy — not an inference from headings, an actual constraint
the site's own guide editor enforces on tip authors. Each tip optionally carries a `tags`
array against this enum (usually 0 or 1 tag, rarely 2). Section titles ("Identifying the
Netherlands", "Regional and province-specific clues", "Spotlight", "Maps and resources") are
a separate, coarser structure — three narrative "tip" sections plus one closing "map"
section, consistent across every one of the 5 sampled countries.

### Discovered distribution (5-country sample: NL, DE, FR, GB, JP — 350 tips total)

| category | count | share |
|---|---:|---:|
| `<untagged>` | 190 | 54% |
| pole | 48 | 14% |
| bollard | 27 | 8% |
| chevron/sign | 27 | 8% |
| language | 17 | 5% |
| architecture | 15 | 4% |
| guardrail | 9 | 3% |
| vegetation | 6 | 2% |
| moving info | 4 | 1% |
| roadline | 4 | 1% |
| landscape | 3 | <1% |
| coverage | 2 | <1% |
| important | 2 | <1% |
| license plates | 1 | <1% |

**Honest caveat:** the majority of tips (54%) carry no formal category at all — general
narrative description (landscape, road quality, urban planning, specific regional trivia)
that the site's own authors didn't tag. Only the tagged minority maps cleanly onto a
repeatable "photo → guess the country" quiz mechanic; the untagged tips are prose that would
need a human (or a much smarter extraction pass) to turn into quiz-worthy items, if they can
be at all — see §5.

`license plates` appearing only once in this sample is almost certainly sample-size noise,
not real rarity — it's one of the most classic GeoGuessr metas and the task's own suggested
topic list names it explicitly. A larger sample would very likely surface far more.

## 3. Sample-run stats

```
iso   tips   sections   status
NL    52     3          ok
DE    71     3          ok
FR    92     3          ok
GB    53     3          ok
JP    82     3          ok
```

350 tips across 5 countries (7 sitemap/page fetches total: 1 robots.txt + 1 sitemap.xml + 5
guide pages, all cached under `data/plonkit/.cache/` so a re-run fetches nothing new). No
images were downloaded — only their URLs were recorded, per the task brief.

## 4. Mapping categories to viable geodrill topics

Every **tagged** category below is structurally identical to the existing `roadside`
generator's shape (architecture §6.2): one photo, one correct country, single-choice MCQ,
country-flag-prefixed labels, tier-gated. Each would be its own `internal/topics/<name>/`
package and its own `quiz_kind`, registered independently (architecture §8's conflict-
avoidance rule — none of these share files with each other or with roadside/specialchars/
words).

| plonkit tag | proposed topic path | `quiz_kind` | notes |
|---|---|---|---|
| bollard | `roadside-meta/bollards` | `bollard_country` | photo → country; direct precedent in the task's own example list |
| pole | `roadside-meta/utility-poles` | `pole_country` | largest tagged category (48) — richest starting dataset |
| license plates | `roadside-meta/license-plates` | `plate_country` | classic meta; this sample under-counts it (§2) |
| chevron/sign | `roadside-meta/chevrons-signs` | `chevron_country` | plonkit's tag bundles chevrons and general road-sign style together — may want splitting once more data is seen |
| roadline | `roadside-meta/road-lines` | `roadline_country` | matches the task's "road lines" suggestion directly |
| guardrail | `roadside-meta/guardrails` | `guardrail_country` | smaller dataset (9); worth folding into a broader container rather than standing alone |
| architecture | `roadside-meta/architecture` | `architecture_country` | often building-material/roof-style photos, not roadside furniture — may warrant its own root, not `roadside-meta` |
| vegetation | `roadside-meta/vegetation` | `vegetation_country` | smallest confidently-quizzable category (6) |

Proposed item payload (mirrors `roadside`'s `itemPayload`, architecture §6.2):

```json
{
  "image": "plonkit/bollards/nl_dutch_bollard.png",
  "flag": "🇳🇱",
  "name": "Netherlands",
  "un_member": true,
  "gg_coverage": true,
  "source_tip_id": "R6It",
  "source_url": "https://www.plonkit.net/netherlands"
}
```

`source_tip_id`/`source_url` are new versus roadside's shape — provenance back to the exact
plonkit tip an item came from, useful both for attribution and for the refresh pipeline (§5)
to detect when a source tip has been edited or removed upstream.

**Not mapped to a topic (left as-is):** `language` (this is really the same mechanic as the
*existing* `internal/topics/words` / `internal/topics/specialchars` topics — a vocabulary or
script cue, not a "photo of X" meta — so it's a candidate for *feeding* those topics' seed
files, not a new tier-5 topic of its own). `coverage`, `important`, `moving info`,
`landscape` are editorial/meta commentary (Street View generation notes, "pay attention"
flags, seasonal caveats) with no single consistent visual answer shape — not directly
quizzable without hand-curation per tip.

## 5. Tier-5 mapping (per architecture §4 rubric)

Architecture §4 already reserves tier 5 exclusively for this content: *"plonkit meta:
bollards, license plates, utility poles, road-line colors, chevron/sign styles"* — no other
tier touches these categories, so **every item from every topic in §4's table is tier 5**,
unconditionally, with no per-country tier computation needed (unlike `roadside`, which
computes tier via UN-membership/G20/GG-coverage rules). This is a simpler tier assignment
than any topic built so far, precisely because the rubric already scoped it that way.

## 6. What the full-scrape pipeline needs (not built here)

This prototype deliberately stops at structured YAML/JSON with image *URLs* — the brief is
explicit that images are not downloaded. The full pipeline (a separate, later task) needs:

1. **Image download → `media_files`.** For each item, download the (now-absolutized)
   `image_url`, compute `sha256`, store under a local media root, and insert a `media_files`
   row (architecture §2.8) linked through a `content_items` row of kind `"photo"`. Respect
   the same rate limit and robots courtesy as the page scrape.
2. **Dedupe.** Some tips share images across countries' guides (e.g. a "here's what Belgium
   looks like too" aside embedded in the Netherlands guide) — dedupe by `sha256`, not just by
   `(country, category)`, before creating `media_files` rows, or the same photo will be
   ingested multiple times under different item IDs.
3. **One-item-per-(country, category) decision.** Some countries have *multiple* tips under
   the same tag (e.g. NL's `PKNI` regional hectometre-marker tip is also tagged `bollard`
   alongside the main `R6It` bollard tip) — `items` has `UNIQUE(topic_id, key)`, so the
   pipeline must decide whether `key` is the plain ISO code (one representative photo per
   country, picking or merging duplicates) or `iso:tip_id` (multiple quizzable variants per
   country, all sharing the same correct answer). This prototype does not decide this —
   flagged as an open question in §7.
4. **Refresh cadence.** plonkit content changes (`updatedAt` is already in the parsed
   payload, unused by this prototype) — a full pipeline should diff against the previous
   scrape via `updatedAt` and only re-download/re-ingest countries that actually changed,
   rather than re-scraping everything on every run.
5. **Full ISO coverage.** `cmd/plonkit`'s embedded `isoToSlug` table (`countries.go`) covers
   ~130 codes cross-checked directly against the discovered sitemap, not all ~195 — it
   omits guides for sub-national regions with no ISO2 of their own (Alaska, Hawaii, Azores,
   Madeira) and roughly 60 additional countries plonkit does cover. The full pipeline needs
   either a complete static ISO2 table or fuzzy name-matching against the discovered slug
   list directly.
6. **Untagged-tip triage.** Given 54% of tips carry no category (§2), a full pipeline pass
   should at minimum log/report untagged tips per country so a human can spot-check whether
   any of them describe a de-facto meta plonkit's authors simply forgot to tag.

## 7. Open questions (for Aurora)

1. **The robots.txt finding in §0** — reconfirm scope/authorization before scaling beyond
   this prototype's 5-country sample.
2. **Item key scheme for multi-tip-per-country categories** (§6.3) — one representative photo
   per country, or multiple keyed variants?
3. **Where does `architecture` live** — folded into `roadside-meta` alongside the literally
   roadside categories (bollards/poles/plates/signs/lines/guardrails), or its own root topic,
   given it's about buildings, not roads?
4. **`language`-tagged tips as seed input** — should these be extracted into
   `seeds/common_words.yaml`/`seeds/special_chars.yaml` as a data-enrichment pass on the
   *existing* topics (§4), rather than left unused?
5. **Full ISO/slug table** — worth completing to full coverage now, or deferred until the
   full-scrape pipeline task actually needs every country?

## 8. Prototype limitations (explicit, not glossed over)

- No images downloaded (by design, per brief).
- `centeredImage` and `divider` items are dropped entirely — no tip text/category to attach
  them to (`buildCountryDoc` in `guide.go`). A future pass could keep `centeredImage` URLs as
  section-level illustrations.
- Only 5 countries sampled; category counts in §2 are directionally right but will shift with
  a larger sample (see the `license plates` undercount noted there).
- `isoToSlug` covers ~130 codes, not full ISO 3166-1 (§6.5).
- No test asserts against a *second* country's fixture — `cmd/plonkit/testdata/netherlands.html`
  (trimmed to 7 of the Netherlands guide's real tips, all schema shapes represented) is the
  only fixture; Germany/France/UK/Japan were fetched and inspected manually during
  development (see §3) but have no committed fixture, to avoid reproducing more scraped
  copyrighted text than a single small parser-test fixture already does.
