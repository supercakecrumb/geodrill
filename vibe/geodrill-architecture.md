# geodrill v2 — Architecture & Implementation Plan

Scope note: this is the design deliverable. Every "Decisions already made" item in `vibe/geodrill-orchestrator-brief.md` is treated as final. Signatures and DDL below are the contract subagents implement; they are concrete but not full implementations.

Verified baselines that shape the design:
- `engram.CardState` ↔ `fsrs.Card` round-trips losslessly *by design* (see the comment block in `scheduler.go` `toFSRSCard`). The lifecycle layer must NOT be stuffed into `CardState` or that guarantee breaks.
- `engram.NextDue` (queue.go) uses `StateNew` + `MaxNewPerDay` as the novelty gate. In v2 the novelty gate moves upstream to *introductions*, so the review queue must stop re-gating new cards.
- `storage.Store` (store.go / methods.go) issues single pooled statements with **no transactions** anywhere (`pgx.WithTx` is imported-capable but unused). Lifecycle + gating recomputation need atomicity — designed below.
- `sqlc.yaml` enumerates schema files **literally** (all four `*up.sql`). Every new migration must be appended.
- Answer callbacks are prefix-parsed with `strings.SplitN(data, ":", 3)` and a 64-byte budget (`callback.go`). Set/text modes cannot fit answer keys → move to index-based callbacks.
- Integration tests fuse on DB-name-contains-"test" (`testDSN`) and do up→down→up (`freshSchema`); the numbered-migration history must stay linear and each down must reverse its up.

---

## 1. engram v0.3.0 API design

All additions are **domain-agnostic and deterministic** (injected clock/rng, fuzz stays off). Nothing here mentions Telegram, Postgres, countries, or letters.

### 1.1 Item lifecycle (new file `lifecycle.go`)

```go
// Lifecycle is the gating state of an item for one user. It is ORTHOGONAL to
// CardState (the FSRS memory state) and is persisted as a sibling field, never
// inside CardState — CardState must remain a lossless mirror of fsrs.Card.
type Lifecycle int8

const (
    LifecycleNew        Lifecycle = iota // 0: not introduced; no card; eligible for the intro queue; never quizzed
    LifecycleIntroduced                  // 1: intro acknowledged; card exists; FSRS State New/Learning (not yet graduated)
    LifecycleReviewing                   // 2: graduated into durable review; FSRS State Review/Relearning
    LifecycleKnown                       // 3: user asserted mastery; terminal; no active card; reversible later
)

// InFSRS reports whether an item in this lifecycle carries a live FSRS card
// (Introduced or Reviewing). New and Known items do not.
func (l Lifecycle) InFSRS() bool { return l == LifecycleIntroduced || l == LifecycleReviewing }

// LifecycleFor derives the Introduced/Reviewing split from a card's FSRS state.
// Known and New are lifecycle-only (no card) and are never returned here; the
// caller sets those explicitly on the intro outcome. This keeps the coarse
// progress label cheap to store yet always consistent with the card.
func LifecycleFor(cs CardState) Lifecycle {
    if cs.State == StateReview || cs.State == StateRelearning {
        return LifecycleReviewing
    }
    return LifecycleIntroduced
}
```

Semantics: `new → introduced → reviewing` and terminal `known`. `Introduced` vs `Reviewing` is a coarse, cheaply-queried progress label that always agrees with `CardState.State` (via `LifecycleFor`); it is persisted (not derived at read time) so the tier-gating query is a plain index scan, not a per-row FSRS computation.

### 1.2 Introduction — how "introduced" enters FSRS (new file `intro.go`)

```go
// IntroOutcome is the user's response to an introduction card (the three buttons).
type IntroOutcome int8

const (
    IntroGotIt IntroOutcome = iota // → Introduced, fresh FSRS card (StateNew, due now)
    IntroKnown                     // → Known, no card, never quizzed
    IntroTestMe                    // → Reviewing, FSRS card seeded with high initial stability
)

// Introduce applies an introduction outcome at now and returns the resulting
// lifecycle, the CardState to persist, and hasCard (false for IntroKnown, where
// the returned CardState is the zero value). Deterministic; depends only on the
// scheduler's weights and now.
func (s *Scheduler) Introduce(o IntroOutcome, now time.Time) (life Lifecycle, cs CardState, hasCard bool) {
    switch o {
    case IntroKnown:
        return LifecycleKnown, CardState{}, false
    case IntroTestMe:
        return LifecycleReviewing, s.SeedCardKnown(now), true
    default: // IntroGotIt
        return LifecycleIntroduced, s.NewCardState(now), true
    }
}
```

### 1.3 High-initial-stability card for "Know it, but test me"

Rather than invent magic numbers, seed the card as if the user had just answered **Easy on first exposure** — this reuses FSRS's own first-rating initialization, stays deterministic, and moves with the scheduler's weights.

```go
// SeedCardKnown returns a CardState for an item the user asserts they already
// know but still wants tested occasionally: equivalent to a fresh card that
// received a first Easy rating at now. Concretely (go-fsrs default weights):
// State=StateReview, Stability≈w[3]≈15.69 (days), Difficulty=the Easy-initial
// difficulty, Reps=1, Lapses=0, LastReview=now, Due≈now+interval(S)≈now+16d at
// retention 0.9. NO review-log entry is produced — an introduction is not a
// review. Deterministic.
func (s *Scheduler) SeedCardKnown(now time.Time) CardState {
    cs, _ := s.Next(s.NewCardState(now), now, Easy)
    cs.Reps = 1 // an introduction seed is one exposure, not a scored review
    return cs
}
```

Concrete proposed values (retention 0.9, default weights): `Stability ≈ 15.69`, first interval ≈ 16 days, `State = StateReview`. Contrast with `IntroGotIt` (`NewCardState`: `StateNew`, `Stability 0`, due now). This gives "test me" cards a ~2–3 week first interval instead of same-day, exactly the "I know this but keep me honest" behavior. Because `SeedCardKnown` reuses `Next(_, Easy)`, it automatically tracks any `WithWeights`/`WithRetention` override.

### 1.4 Introduction queue helpers (in `intro.go`)

engram does not know about tiers or topics, so the app supplies an **already-ordered** candidate list (order encodes priority: tier, then topic/item position). engram only applies the daily budget.

```go
// IntroItem pairs an item id with its lifecycle for intro selection.
type IntroItem struct {
    SkillID   SkillID
    Lifecycle Lifecycle
}

// IntroConfig configures the daily introduction budget.
type IntroConfig struct { MaxNewPerDay int } // 0 = unlimited

// NextIntroductions returns up to the remaining daily budget of items whose
// Lifecycle == LifecycleNew, PRESERVING the caller's slice order (which encodes
// priority). introducedToday is how many intros were already delivered today.
// Deterministic: no shuffling, stable selection. Returns an empty non-nil slice
// when the budget is exhausted or nothing is New.
func NextIntroductions(items []IntroItem, introducedToday int, cfg IntroConfig) []IntroItem

// RemainingIntroBudget returns max(0, cap-introducedToday); cap==0 → math.MaxInt
// (unlimited). Small pure helper mirroring the review-cap arithmetic.
func RemainingIntroBudget(cap, introducedToday int) int
```

Counting "introduced today" stays app-side (SQL `COUNT` over the introductions table by local day) — engram gets the integer. This mirrors how `CountNewIntroduced` already works but without forcing introductions through the review log.

### 1.5 Review queue for v2 (in `queue.go`)

The daily-new cap is now spent at introduction time, so the review queue must offer *any* introduced card the moment it is due, including a just-"Got it" `StateNew` card.

```go
// NextReview picks the next due card from items, all of which already carry an
// FSRS card (Lifecycle Introduced or Reviewing — the app filters New/Known out
// before calling). A due review/relearning card wins by earliest Due; otherwise
// the earliest-due Learning-or-New card. There is NO daily-new cap: novelty is
// gated upstream at introduction. ok=false means nothing is due at now.
func NextReview(items []QueueItem, now time.Time) (QueueItem, bool)
```

`NextDue` and `CountNewIntroduced` are **kept unchanged** for backward compatibility; the app calls `NextReview` in v2.

### 1.6 Exercise modes (quiz subpackage)

`ModeSingle` is the existing `Generate`/`Grade` path, untouched. Two new modes are additive, in new files `quiz/set.go` and `quiz/text.go`.

```go
// quiz/modes.go
type Mode int8
const (
    ModeSingle Mode = iota // existing single-choice MCQ (key per button)
    ModeSet                 // each button is a SET of keys; grade by set-equality
    ModeText                // free-text typed answer; grade by normalized fuzzy match
)
```

Set-choice (`quiz/set.go`) — a button asserts a *set* of answer keys (e.g. "ø" → {nor,dan}); exactly one candidate set equals the item's true set:

```go
type KeySet []string // canonicalized (sorted, deduped) via CanonSet

type SetOption struct {
    Keys  KeySet // the set this button asserts
    Label string // e.g. "Norwegian / Danish"
}

type SetExercise struct {
    ID      string
    Content Content
    Target  SetOption   // the correct set
    Options []SetOption // shuffled candidates; exactly one equals Target (set-equality)
}

// GenerateSet builds a set-choice exercise: candidates must already include the
// target set (appended if missing, mirroring Generate). Shuffled with the
// injected rng — same seed + input order ⇒ same output order.
func GenerateSet(rng *rand.Rand, target SetOption, candidates []SetOption, c Content) SetExercise

// GradeIndex grades the tapped option position (Telegram sends an index, not the
// set — sets don't fit the 64-byte callback budget). correct = Options[i].Keys
// equals Target.Keys (order-insensitive). Out-of-range ⇒ wrong.
func (e SetExercise) GradeIndex(i int) (correct bool, r engram.Rating)

func CanonSet(keys ...string) KeySet // sort+dedup; used for stable equality & storage
```

Free-text (`quiz/text.go`) — case-insensitive, edit-distance ≤ 2, alias table (alias list lives app-side per item; engram provides the matcher primitive):

```go
type TextMatcher struct {
    Accept   []string // canonical + alias spellings, e.g. ["Norwegian","Norsk","NO"]
    MaxEdits int      // Levenshtein tolerance (e.g. 2); 0 = exact after normalization
}

// Match reports whether typed matches any accepted spelling after Normalize
// (Unicode casefold + trim + internal-space collapse) within MaxEdits
// (Levenshtein). Deterministic, no rng. Empty typed ⇒ false.
func (m TextMatcher) Match(typed string) (correct bool, r engram.Rating)

func Normalize(s string) string          // exported so the app can canonicalize aliases
func Levenshtein(a, b string) int        // exported utility
```

`TipProvider`/`TipRequest` gain a `Mode Mode` field (additive; zero value = `ModeSingle`, so existing providers are unaffected).

### 1.7 Compatibility vs v0.2.0

- **Additive (non-breaking):** `Lifecycle` + helpers, `IntroOutcome`/`Introduce`/`SeedCardKnown`, `IntroItem`/`IntroConfig`/`NextIntroductions`/`RemainingIntroBudget`, `NextReview`, the entire set-choice and free-text APIs, `TipRequest.Mode`.
- **Breaking:** none required. `NewCardState`, `NextDue`, `CountNewIntroduced`, `Generate`, `Grade`, `Scheduler`, all stores keep their signatures. (`SeedCardKnown` uses the existing `Next` path, so no FSRS-internal coupling.)
- **Behavioral note (documented, not a signature break):** apps that adopt lifecycle should call `NextReview` (no new-cap) instead of `NextDue`; the two coexist.

Every one of these gets a `changie new` fragment; batch at the `v0.3.0` tag.

---

## 2. Postgres schema v2

Design principles: (1) nothing hard-codes the hierarchy — code switches on `topics.quiz_kind`/`config`, never on a slug; (2) items are generic with a `jsonb` payload; (3) country facts are typed+normalized and absorb new datasets with zero DDL; (4) FSRS columns map 1:1 to `engram.CardState`.

### 2.1 Topic tree — recommendation: `parent_id + slug`, canonical; path via recursive-CTE view

Trade-off: **materialized path** (`languages.guess-the-language`) makes subtree reads trivial but re-parenting rewrites every descendant's path. **`parent_id`** makes re-parenting a *single-row UPDATE* (exactly what "Aurora will rearrange it later" needs) at the cost of a recursive CTE for subtree reads. The tree is tiny (dozens of nodes), so subtree-read cost is irrelevant. **Choose `parent_id + slug` as the source of truth**, expose a `topic_paths` view (recursive CTE) for the rare subtree/breadcrumb query, and keep an optional `ltree` denormalization in reserve only if a future public map site needs indexed subtree filters.

```sql
CREATE TABLE topics (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  parent_id     uuid REFERENCES topics(id) ON DELETE CASCADE,      -- NULL = root
  slug          text NOT NULL,                                     -- unique among siblings
  name          text NOT NULL,
  position      int  NOT NULL DEFAULT 0,                           -- sibling ordering in the browser
  base_tier     smallint NOT NULL DEFAULT 0,                       -- default tier for this topic's items
  quiz_kind     text NOT NULL DEFAULT 'container',                 -- 'container'|'language_id'|'char_language'|'road_side'|'word_language'|...
  exercise_modes text[] NOT NULL DEFAULT '{single}',               -- allowed engram modes
  is_quizzable  boolean NOT NULL DEFAULT true,                     -- false = pure container node
  config        jsonb NOT NULL DEFAULT '{}',                       -- per-topic knobs (prompt template, media flag, distractor group ref)
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX topics_sibling_slug ON topics(parent_id, slug) WHERE parent_id IS NOT NULL;
CREATE UNIQUE INDEX topics_root_slug    ON topics(slug)            WHERE parent_id IS NULL;
CREATE INDEX topics_parent_pos          ON topics(parent_id, position);
```

`quiz_kind` selects the generator from a registry (§3/§8) — no code path ever `switch`es on a slug, satisfying "nothing hard-codes the hierarchy."

### 2.2 Items — generic quizzable entity

```sql
CREATE TABLE items (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  topic_id    uuid NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
  key         text NOT NULL,                    -- stable answer key within the topic (e.g. 'rus', 'set:nor,dan', 'FR')
  label       text NOT NULL,
  tier        smallint,                         -- NULL ⇒ inherit topics.base_tier (per-item override)
  payload     jsonb NOT NULL DEFAULT '{}',      -- topic-specific: {"char":"ў"} | {"languages":["nor","dan"]} | {"side":"L"} | {"word":"ulica"}
  country_id  uuid REFERENCES countries(id),    -- optional link (road-side, flags, profiles)
  position    int  NOT NULL DEFAULT 0,
  active      boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (topic_id, key)
);
CREATE INDEX items_topic_idx        ON items(topic_id);
CREATE INDEX items_country_idx      ON items(country_id);
CREATE INDEX items_effective_tier   ON items(topic_id, COALESCE(tier, 0)); -- effective tier resolved in query with base_tier
```
Effective tier = `COALESCE(items.tier, topics.base_tier)`, resolved in a view `item_tiers(item_id, topic_id, tier)` used by gating.

### 2.3 Per-user per-item lifecycle + FSRS card (replaces `user_skills`)

```sql
CREATE TABLE user_items (
  user_id       uuid NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
  item_id       uuid NOT NULL REFERENCES items(id)  ON DELETE CASCADE,
  lifecycle     smallint NOT NULL DEFAULT 0,        -- engram.Lifecycle: 0 new,1 introduced,2 reviewing,3 known
  -- engram.CardState (meaningful iff lifecycle IN (1,2)); zeroed otherwise
  due           timestamptz,
  stability     double precision NOT NULL DEFAULT 0,
  difficulty    double precision NOT NULL DEFAULT 0,
  reps          int  NOT NULL DEFAULT 0,
  lapses        int  NOT NULL DEFAULT 0,
  state         smallint NOT NULL DEFAULT 0,        -- FSRS memory state
  last_review   timestamptz,
  introduced_at timestamptz,                        -- when it first left 'new'
  known_at      timestamptz,                        -- when marked known (audit + reversibility)
  updated_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, item_id)
);
CREATE INDEX user_items_due_idx       ON user_items(user_id, due) WHERE lifecycle IN (1,2);
CREATE INDEX user_items_lifecycle_idx ON user_items(user_id, lifecycle);
```
Absence of a row ⇒ item is implicitly `new` for that user (rows are created lazily on first introduction). `known` reversibility is schema-supported today: flip `lifecycle` back to `reviewing` and seed a fresh card; `known_at` and the introductions log preserve the audit trail for the future web UI.

### 2.4 Introductions — re-viewable, three-button outcome (append-only event log)

```sql
CREATE TABLE introductions (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id     uuid NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
  item_id     uuid NOT NULL REFERENCES items(id)  ON DELETE CASCADE,
  seq         int  NOT NULL DEFAULT 1,             -- 1 = first exposure; >1 = re-view
  outcome     smallint,                            -- NULL = shown, not yet answered; 0 got_it,1 known,2 test_me
  shown_at    timestamptz NOT NULL DEFAULT now(),
  answered_at timestamptz,
  message_id  bigint
);
CREATE INDEX introductions_user_item_idx ON introductions(user_id, item_id);
CREATE INDEX introductions_user_day_idx  ON introductions(user_id, answered_at);
```
`user_items.lifecycle` is the current state; `introductions` is the event history (analogous to `reviews` vs `user_skills`). Re-viewing an already-introduced item inserts a new row with `seq+1` and typically `outcome=NULL` (just re-shown); tapping a button may still transition lifecycle. "Introduced today" for the daily budget = `COUNT(DISTINCT item_id) WHERE answered_at::date (local) = today AND seq = 1 AND outcome IS NOT NULL`.

### 2.5 Exercises + reviews (mode-aware)

```sql
CREATE TABLE exercises (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  item_id       uuid NOT NULL REFERENCES items(id),
  content_id    uuid REFERENCES content_items(id),      -- NULL for modes with no content row (e.g. road-side)
  mode          smallint NOT NULL DEFAULT 0,            -- quiz.Mode
  prompt        text,                                   -- rendered prompt (char / word / sentence cached)
  options       jsonb NOT NULL,                         -- mode-specific: [{key,label}] | [{keys:[],label}] | {accept:[]}
  correct_answer text NOT NULL,                         -- serialized key / sorted-set / canonical spelling — grading source of truth
  is_media      boolean NOT NULL DEFAULT false,         -- photo message (edit caption+markup) vs text
  practice      boolean NOT NULL DEFAULT false,
  created_at    timestamptz NOT NULL DEFAULT now(),
  answered_at   timestamptz,                            -- single-use guard (preserves MarkExerciseAnswered semantics)
  message_id    bigint
);

CREATE TABLE reviews (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  item_id       uuid NOT NULL REFERENCES items(id),
  exercise_id   uuid REFERENCES exercises(id),
  content_id    uuid REFERENCES content_items(id),
  mode          smallint NOT NULL DEFAULT 0,
  chosen        text NOT NULL,                          -- chosen key / serialized set / typed string
  correct_answer text NOT NULL,
  correct       boolean NOT NULL,
  rating        smallint NOT NULL,
  response_ms   int,
  stability_before double precision NOT NULL, difficulty_before double precision NOT NULL,
  stability_after  double precision NOT NULL, difficulty_after  double precision NOT NULL,
  state_before  smallint NOT NULL, scheduled_days int NOT NULL, elapsed_days int NOT NULL,
  practice      boolean NOT NULL DEFAULT false,
  reviewed_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reviews_user_time_idx ON reviews(user_id, reviewed_at);
CREATE INDEX reviews_item_idx      ON reviews(item_id);
```
The FSRS column block is byte-for-byte the old `reviews` shape, so `engramstore` and stats math port unchanged (only `skill_id→item_id`, `chosen_key→chosen`, `correct_key→correct_answer`, `+mode`).

### 2.6 Countries — first-class

```sql
CREATE TABLE countries (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  iso_a2            text UNIQUE,                 -- 'FR','GB'; NULL only for subdivisions without one
  iso_a3            text UNIQUE,                 -- 'FRA'
  numeric_code      text,                        -- '250'
  name              text NOT NULL,
  official_name     text,
  flag_emoji        text,                        -- 🇫🇷 (regional-indicator pair); subdivisions use tag sequences (🏴󠁧󠁢󠁳󠁣󠁴󠁿)
  parent_country_id uuid REFERENCES countries(id),   -- England/Scotland/Wales → GB
  is_subdivision    boolean NOT NULL DEFAULT false,
  un_member         boolean NOT NULL DEFAULT false,
  gg_coverage       boolean NOT NULL DEFAULT false,   -- has official Street View
  created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX countries_flags_idx ON countries(un_member, gg_coverage);
```
Full ISO 3166-1 (~250, incl. Isle of Man) + GB subdivisions England/Scotland/Wales (`is_subdivision=true`, `parent_country_id=GB`, tag-sequence flag emoji). `un_member`/`gg_coverage` are first-class booleans (also queryable); everything else lives in typed facts. `region` intentionally is NOT a column — it is a fact, to avoid schema drift.

### 2.7 Typed, normalized country facts — zero-DDL dataset absorption

```sql
CREATE TABLE fact_defs (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  key         text UNIQUE NOT NULL,        -- 'drives_on','main_religion','region','gdp_per_capita','lgbt_legal','tld'
  label       text NOT NULL,
  value_type  text NOT NULL,               -- 'text'|'number'|'bool'   (enums stored as text)
  unit        text,                        -- 'USD','%',NULL
  cardinality text NOT NULL DEFAULT 'single', -- 'single'|'multi' (languages spoken = multi)
  dataset     text,                        -- provenance grouping: 'baseline','cia_factbook','plonkit',...
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE country_facts (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  country_id  uuid NOT NULL REFERENCES countries(id) ON DELETE CASCADE,
  fact_def_id uuid NOT NULL REFERENCES fact_defs(id) ON DELETE CASCADE,
  val_text    text,
  val_num     double precision,
  val_bool    boolean,
  source      text,                        -- attribution / provenance
  observed_at date,                        -- time-series support (e.g. GDP by year); NULL = current
  created_at  timestamptz NOT NULL DEFAULT now(),
  CHECK (num_nonnulls(val_text, val_num, val_bool) = 1)  -- exactly one typed slot populated
);
CREATE INDEX cf_country_def_idx ON country_facts(country_id, fact_def_id);
CREATE INDEX cf_def_text_idx     ON country_facts(fact_def_id, val_text);
CREATE INDEX cf_def_bool_idx     ON country_facts(fact_def_id, val_bool);
CREATE INDEX cf_def_num_idx      ON country_facts(fact_def_id, val_num);
```
Arbitrary filters are plain SQL, e.g. "drive on the left AND Islam main religion":
```sql
SELECT c.name FROM countries c
JOIN country_facts fd ON fd.country_id=c.id JOIN fact_defs dd ON dd.id=fd.fact_def_id AND dd.key='drives_on'
JOIN country_facts fr ON fr.country_id=c.id JOIN fact_defs dr ON dr.id=fr.fact_def_id AND dr.key='main_religion'
WHERE fd.val_text='left' AND fr.val_text='Islam';
```
A brand-new dataset (economics, LGBT-friendliness, safety) is absorbed by inserting `fact_defs` + `country_facts` rows — **no migration**. Multi-valued facts (languages spoken) are just several rows sharing a `multi` def. This is the schema seam the future public map/stats-explorer site plugs into.

### 2.8 Content + media

```sql
CREATE TABLE content_items (              -- sentences today; words/photos too
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  topic_id    uuid REFERENCES topics(id) ON DELETE CASCADE,  -- scope content to a topic (NULL = shared)
  kind        text NOT NULL DEFAULT 'sentence',              -- 'sentence'|'photo'|'word'
  key         text NOT NULL,                                 -- answer key it illustrates
  payload     text NOT NULL,                                 -- sentence text OR relative media path
  source      text NOT NULL,
  char_length int  NOT NULL DEFAULT 0,
  UNIQUE (kind, key, payload)
);
CREATE INDEX content_items_key_idx ON content_items(kind, key);

CREATE TABLE media_files (               -- photos: local path + telegram file_id cache (decision 6)
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  content_id       uuid REFERENCES content_items(id) ON DELETE CASCADE,
  local_path       text NOT NULL UNIQUE,
  sha256           text,
  telegram_file_id text,                 -- cached after first upload; reused to skip re-upload
  width int, height int, bytes int,
  created_at       timestamptz NOT NULL DEFAULT now()
);
```

### 2.9 Tiers + global gating (cache + on-the-fly)

Base tier on `topics.base_tier`; per-item override on `items.tier`; effective tier via `item_tiers` view. Per-user completion cached, recomputed transactionally on each answer/introduction:

```sql
CREATE TABLE user_tier_progress (
  user_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  tier             smallint NOT NULL,
  total_items      int NOT NULL,
  introduced_items int NOT NULL,        -- lifecycle != new
  good_shape_items int NOT NULL,        -- meets the §4 FSRS-shape threshold
  complete         boolean NOT NULL DEFAULT false,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, tier)
);
```
On-the-fly is the source of truth (one `GROUP BY` over `user_items ⋈ items ⋈ item_tiers`); the cache exists so the introduction scheduler and `/topics` render without recomputing every tick. Details in §4.

### 2.10 Users/settings — keep reminders, add intro cap

```sql
ALTER TABLE users
  ADD COLUMN daily_intro_cap int NOT NULL DEFAULT 10;   -- decision 2 (N=10; per-user tunable later)
-- users keeps: daily_new_cap, reminders_enabled, timezone, label_style, reminder_hour,
--              follow_up_enabled, follow_up_delay_min  (all reminder config preserved)
```
Optional per-user topic opt-in/out (preserves the `/decks` on-off affordance, now secondary to tier gating):
```sql
CREATE TABLE user_topics (
  user_id  uuid REFERENCES users(id)  ON DELETE CASCADE,
  topic_id uuid REFERENCES topics(id) ON DELETE CASCADE,
  enabled  boolean NOT NULL DEFAULT true,
  PRIMARY KEY (user_id, topic_id)
);
```

### 2.11 Do decks/skills survive? — No; they become topics/items. Justification.

**Decks → container topics; skills → items; drop `decks`, `skills`, `user_decks`, `user_skills`.** A "confusion group" is exactly a topic whose children are the confusable items, and MCQ distractors are drawn from *sibling items in the same group topic* — the tree models confusion groups natively, so the deck concept is redundant. Concretely: `languages/guess-the-language` (container) → one child topic per group (`romance`, `cjk`, `slavic-cyrillic`, …) each holding the language items; `Generate` draws options from the group topic's items. `user_skills` → `user_items` (FSRS 1:1). `user_decks` → `user_topics`. Keeping decks/skills alongside topics/items would mean two parallel identity systems and a hard-coded "deck = confusion group" assumption — the opposite of the restructurability requirement.

---

## 3. Migration strategy

### 3.1 Incremental `000005+`, not a baseline squash

**Keep `000001`–`000004`; add `000005+`.** Reasons: (1) `migrate-on-startup` and the `freshSchema` up→down→up harness depend on a linear numbered history — a squash forces `migrate force`/`schema_migrations` surgery on every DB and breaks the down-migration test fuse; (2) the numbered chain keeps each change reviewable commit-by-commit; (3) the final stats migration reads the OLD schema from a *restored backup DB* (separate DSN), so production never needs an in-place 4→v2 upgrade — dev data is destroyable and rebuilt from the new baseline. A squash buys nothing here and costs the test harness.

Migration sequence (each reversible; each its own commit + changie fragment where user-visible):
- **`000005_v2_core`** — create `topics`, `items`, `user_items`, `introductions`, `countries`, `fact_defs`, `country_facts`, `media_files`, `user_topics`, `user_tier_progress`; `item_tiers` + `topic_paths` views; `ALTER users ADD daily_intro_cap`. Down drops them.
- **`000006_v2_exercises_reviews`** — add `item_id`, `mode`, generalized answer columns to `exercises`/`reviews` (nullable during transition); add `content_items.topic_id`. Old `skill_id`/`chosen_key`/`correct_key` kept temporarily.
- **`000007_drop_legacy`** (runs in the integration wave, *after* the old-quiz data migration backfills `item_id`): drop `decks`, `skills`, `user_decks`, `user_skills`, and the legacy `exercises`/`reviews` columns; add `NOT NULL` to `item_id`. Down recreates them (empty).

Splitting create (`000005/6`) from drop (`000007`) lets the old-quiz migration (wave 4) run against both schemas in one live DB.

### 3.2 `sqlc.yaml` updates

Append every new up.sql to the literal `schema:` list: `000005_v2_core.up.sql`, `000006_v2_exercises_reviews.up.sql`, `000007_drop_legacy.up.sql`. Regenerate `internal/storage/db/*` after each. This is a required, easily-forgotten step — call it out in every schema task's checklist.

### 3.3 `migrate-on-startup` blast-radius mitigations

The sacred rule (a down once wiped the dev DB, 2026-07-15). Mitigations, in the plan:
- **Wave-1 `pg_dump`** dated backup before any destructive DDL (F/decision).
- **Stop-bot check** in wave 1 (`pgrep -fl geodrill-bot`) so nothing serves live traffic during migration.
- `MigrateUp` already only calls `Up` (never `Down`) at startup — keep it. Add an env fuse `GEODRILL_SKIP_MIGRATE=1` so an operator can run migrations deliberately in a controlled step rather than implicitly on boot (mitigates "boot the new binary against the old prod DB and auto-mutate").
- Down migrations stay strictly test-only (`MigrateDown` is only called by `freshSchema`).

### 3.4 Old sentence-quiz preserved behaviorally

The `language_id` mechanic (sentence → guess-the-language MCQ) is preserved: same content, same distractor-from-group-siblings generation, same grading and tips. What changes is that languages now pass through the introduction step first (introduction-before-review is a *core* v2 mechanic that applies to every topic, per decision 2) and internals sit on the topic/item framework. "Behavior preserved" = the quiz feels the same; it is not exempt from introductions.

### 3.5 User-stats migration — a `cmd/` tool, not a migration

**`cmd/statsmigrate`** (Go tool), because it is inherently cross-database (reads the *restored backup* old schema on one DSN, writes the new schema on another), needs mapping logic, idempotency, dry-run, and a safety fuse — none of which belong in an embedded, single-connection schema migration.

- **Inputs:** `-from` (restored backup DSN, old schema) and `-to` (new/live DSN, new schema), `-dry-run`, and a fuse that refuses unless `-to` is the intended DB (and never equals `-from`).
- **Mapping:** `users` copied by `telegram_id` (upsert; carries all reminder settings + new `daily_intro_cap` default). `skills(deck,key)` → `items(topic,key)` via a deterministic key map (group topic slug + ISO key). `user_skills` → `user_items` with `lifecycle = LifecycleFor(state)` (Review/Relearning → reviewing, else introduced), FSRS columns copied 1:1, `introduced_at = created_at|last_review`, plus a synthesized `introductions` row (`outcome=got_it, seq=1, answered_at=introduced_at`) so migrated items satisfy the "already introduced" invariant and are never re-introduced. `reviews` → `reviews` (skill→item, `chosen_key→chosen`, `correct_key→correct_answer`, `mode=single`, `practice` preserved).
- **Idempotency:** `ON CONFLICT DO NOTHING`/upsert keyed on natural keys; safe to re-run.
- **When:** final wave, only after the wave-1 `pg_dump` exists and the new schema is deployed.

---

## 4. Tier rubric (0–5) for Aurora's sign-off

Philosophy: tier 0 = things almost everyone already knows (seed the system, mostly auto-`known`); rising tiers = rarer / more discriminating GeoGuessr signal; tier 5 = meta/expert cues (bollards, poles). **No item is assigned a tier until Aurora signs off on this rubric.**

| Tier | Meaning | Special characters | Road side | Common words | Flags | Countries |
|---|---|---|---|---|---|---|
| **0** | Universally known | ñ→Spanish | US = right; UK = left | (none) | 🇺🇸 🇬🇧 🇫🇷 🇯🇵, own flag | US, UK, France, Germany, Japan |
| **1** | Very common signal | Cyrillic-vs-Latin script; å→Nordic | All G20 | "ulica/exit" road markers | UN P5 + big EU | All of Europe (major), big economies |
| **2** | Common | ø→{nor,dan} (set); ł→Polish; ă→Romanian | All UN members w/ GG coverage | 5–10 words per major Latin/Cyrillic lang | All UN members | All UN members |
| **3** | Intermediate / discriminating | ў→Belarusian; ђ→Serbian; subgroup sets | Less-covered UN members | Rarer script cues; word→meaning (later) | Subdivisions + major dependencies | Dependencies, low-coverage states |
| **4** | Advanced / rare | Letters unique to one lang within a subgroup; obscure diacritics | Micro-states & territories w/o coverage | Long-tail vocabulary | Obscure dependencies, historical | Microstates, disputed/partial recognition |
| **5** | Meta / expert | (n/a — script cues are ≤4) | (n/a) | (n/a) | (n/a) | **plonkit meta:** bollards, license plates, utility poles, road-line colors, chevron/sign styles |

### 4.1 "Good FSRS shape" — proposed measurable threshold

**An item is in good shape iff `lifecycle = known` OR (`state = StateReview` AND `stability ≥ 21` days).** Justification: `state = StateReview` is a parameter-free, FSRS-native "it graduated out of Learning" signal; adding `stability ≥ 21d` (~3 weeks, comfortably past the learning hump at retention 0.9) rules out cards that *just barely* graduated and would lapse next week; `known` counts because the user explicitly asserted mastery. This avoids an arbitrary bare "stability ≥ S" cutoff (which fires on ungraduated Learning cards) while staying a single, cheap `WHERE`.

**Tier n is "complete" iff every tier-n item is introduced (`lifecycle != new`) AND ≥ 80% are in good shape.** The 80% (not 100%) share stops one stubborn item from hard-blocking all progression while still requiring durable mastery of the bulk.

### 4.2 "Complete tier n unlocks tier n+2" — mechanics

Unlocked set = `{0, 1} ∪ { n+2 : tier n is complete }`. Source of truth is on-the-fly:
```sql
-- per (user,tier): totals, introduced, good-shape, complete
SELECT t.tier,
       count(*)                                              AS total_items,
       count(*) FILTER (WHERE ui.lifecycle IS NOT NULL AND ui.lifecycle <> 0) AS introduced_items,
       count(*) FILTER (WHERE ui.lifecycle = 3
                           OR (ui.state = 2 AND ui.stability >= 21))          AS good_shape_items
FROM item_tiers t
JOIN items i ON i.id = t.item_id
LEFT JOIN user_items ui ON ui.item_id = i.id AND ui.user_id = $1
GROUP BY t.tier;
```
This query populates `user_tier_progress` (the cache), which the introduction scheduler and `/topics` read cheaply. **Where computed:** the affected tier's row is recomputed **inside the same transaction as each answer/introduction write** (only the item's tier needs recompute — cheap), keeping cache and truth consistent; a full recompute is available for `/topics` and as a repair path. Locked tiers surface as 🔒 in the browser and are excluded from both the review queue and the introduction candidate list.

---

## 5. Command surface

Surviving: `/start`, `/practice` (+ Stop), `/stats`, `/settings`, `/help`. New: `/study`, `/train` (retargeted to all topics), `/topics`. Retired: `/decks` (folds into `/topics`). `/introduce` = alias that fetches more intro cards on demand (decision 2).

### 5.1 Introduction card (3 buttons) — message/keyboard flow

`/study` (or the daily push's ▶️ button) pulls the next `NextIntroductions` batch and steps one card per item. Text items are text messages; media items are **photo messages from birth** (edit caption + markup in place — decision 6). A country on any button/label is prefixed with its flag emoji.

```
[photo or text prompt: "New letter: ў  — where is it used?"]  (+ a teaching blurb)
[ ✅ Got it ] [ 🧠 I know this ] [ 🎯 Test me ]
```
- **Got it** → `Introduce(IntroGotIt)`: `user_items` lifecycle=Introduced + `NewCardState`; enters the review queue immediately.
- **I know this** → `Introduce(IntroKnown)`: lifecycle=Known, no card; never quizzed (reversible later).
- **Test me** → `Introduce(IntroTestMe)`: lifecycle=Reviewing + `SeedCardKnown` (~16-day first interval).

Each tap writes `introductions.outcome` + `user_items` + recomputes that item's tier in one transaction (§5.5), edits the card to a confirmation, and advances to the next. Re-viewing later inserts a new `introductions` row (`seq+1`).

### 5.2 Topic browser `/topics` — tree nav with tiers + progress + availability

```
🌍 Topics
  ▸ Languages        �re: 42/50 · introduced 48/50
  ▸ Roads            ▸ 1 topic locked 🔒 (tier 3)
  ▸ Countries        💡 tips
[tap a row → drill in]
```
Drilling into a container lists child topics; drilling into a quizzable topic shows per-tier rows with progress bars and 🔒 on locked tiers and 💡 where a `TipProvider` exists (reuse the existing `DeckHasTips`-style marker). Nav is `parent_id`-based (`topic_paths` view for breadcrumbs); a ⬆️ row goes to the parent.

### 5.3 Daily "N items to introduce" push — extend the existing reminder loop

**Extend `internal/telegram/bot.go`'s reminder loop, do not add a second scheduler.** It already ticks every 30 min, resolves each user's local hour, tracks per-user in-memory state, and caps follow-ups — all of which the intro push needs identically. Extend `decideReminder` to consider *both* due reviews and available introductions, and at the user's hour send a combined nudge:
```
🔔 3 reviews due · 5 new items to introduce
[ ▶️ Start reviewing ]  [ ✨ Introduce new ]
```
The ✨ button launches `/study`; ▶️ launches `/train` (existing `dataStartTrain` pattern). "Available introductions" = `RemainingIntroBudget` > 0 AND candidate list non-empty (unlocked tiers, lifecycle=new).

### 5.4 Callback-data scheme extension (64-byte budget respected)

Current parser is prefix `SplitN(data,":",3)`. Extensions:
- **Answers → index-based** (sets/text can't fit keys): `ans:<ex-uuid>:<idx>`, `prac:<ex-uuid>:<idx>` where `idx` is the option position; the server maps idx→answer via persisted `exercises.options`. Budget: `4+36+1+2 = 43`. This also covers single-choice (idx into the shuffled options).
- **Free-text answers** arrive as a **plain text message** (not a callback): the bot looks up the caller's single open `mode=text` exercise (`answered_at IS NULL`, newest) and grades via `TextMatcher`.
- **Introductions:** `intro:<intro-uuid>:g|k|t` (got/known/test). Budget: `6+36+1+1 = 44`.
- **Study flow:** `study:start`, `study:next`.
- **Topics nav:** `top:<topic-uuid>` (drill), `top:up:<parent-uuid>`, `top:root`.
- Existing settings/deck-style prefixes unchanged; `/decks`-specific ones retired with `/decks`.

`ParseCallback` generalizes to return `(kind, exerciseID, index)` for `ans`/`prac`; a new `ParseIntro`/`ParseNav` handles the rest. `handleCallback`'s prefix switch gains `intro:`, `study:`, `top:` branches.

### 5.5 Atomicity — new transaction seam on `storage.Store`

Because lifecycle transitions and gating recompute must be consistent, add (the currently-unused) transaction support:
```go
// WithTx runs fn inside a single pgx transaction, exposing a tx-bound *db.Queries.
func (s *Store) WithTx(ctx context.Context, fn func(q *db.Queries) error) error
```
(implemented via `s.pool.BeginTx` + `s.q.WithTx(tx)` — sqlc/pgx supports `*Queries.WithTx`). Two flows wrap in one tx: **answer** (`PutCard` + `InsertReview` + recompute item's `user_tier_progress`) and **introduce** (`user_items` upsert + `introductions` insert + tier recompute). `MarkExerciseAnswered`'s single-use guard is preserved inside the tx.

---

## 6. Topic implementations (build now)

All three register a `TopicGenerator` (see §8) keyed by `topics.quiz_kind`, so no topic worker edits `internal/train/service.go`. Data audits follow the tips precedent (`internal/tips/audit_test.go`): an opt-in test gated on an env DSN, checked against the ingested Tatoeba corpus.

### 6.1 Special characters (`quiz_kind='char_language'`, topic `languages/special-characters`)

- **Data shape:** per unique letter → the language(s) that use it. Two sub-modes: **unique-to-one-language** (single MCQ char→language, plus free-text typed) and **unique-to-a-subgroup** (set-choice, e.g. "ø" → {nor,dan}). `items.payload = {"char":"ў","script":"cyrillic","mode":"single|set","languages":[...]}`.
- **Where data lives:** seed file `seeds/special_chars.yaml` (haiku compiles it), ingested via an extended `cmd/ingest` into `items` under the topic. Not a migration — data churns.
- **Audit:** `internal/topics/specialchars/audit_test.go`, gated on `SPECIALCHARS_AUDIT_DATABASE_URL`, mirroring the tips corpus audit — for each claimed unique letter, assert it appears in that language's `content_items` sentences AND does NOT appear (above a small threshold) in sibling languages (uniqueness check). No fabricated claims.
- **Counts:** ~30 languages → ~80–150 single-letter items; ~15–30 subgroup set items.

### 6.2 Road sides (`quiz_kind='road_side'`, topic `roads/which-side`)

- **Data shape:** country → L/R. Single source of truth = `fact_defs('drives_on')` + `country_facts`; each item links `country_id` and caches the correct side in `items.payload={"side":"L|R"}` for generation speed. Buttons: **Left / Right**, country label flag-prefixed (decision 6).
- **Where data lives:** seed `seeds/road_sides.yaml` (haiku compiles from an authoritative list) → facts + items. Countries dataset itself (§2.6) is a separate haiku task feeding `countries`.
- **Audit:** `internal/topics/roadside/audit_test.go` cross-checks every `gg_coverage` country has exactly one `drives_on` fact and that `items.payload.side` matches the fact (no contradictions).
- **Counts:** every country (~250), tier-gated; ~75 left-driving, ~175 right.

### 6.3 Common words (`quiz_kind='word_language'`, topic `languages/common-words`)

- **Data shape:** 5–10 high-value words per Cyrillic/Latin language ("street", "road", "exit"…). `items.payload={"word":"ulica","meaning":"street","romanization":"…"}`. **word→language built now**; **word→meaning designed only** (a second mode/topic reusing the same items).
- **Where data lives:** `seeds/common_words.yaml` (haiku), ingested into items.
- **Audit:** `internal/topics/words/audit_test.go` verifies each word occurs in that language's Tatoeba corpus (frequency floor) and is reasonably discriminating vs siblings.
- **Counts:** ~20 script-relevant languages × 5–10 ≈ 150–200 items.

---

## 7. CI/CD design

`.github/workflows/` in **both** public repos, created early (wave 2) and maintained continuously. `changie init` in each repo first (wave 1) if `.changie.yaml` is absent.

### 7.1 engram (library — no image)
- **`ci.yml`** on push/PR: `golangci-lint run`; `go build ./... && go vet ./... && go test -race ./...`. No Postgres service (engram has no DB). Also fold in the `.gitignore` fix (add `.env`, `*.log`).
- **`release.yml`** on tag `v*`: `changie batch`/verify notes → **GitHub Release only** (no container). Push tags trigger it.

### 7.2 geodrill (bot — GHCR image)
- **`ci.yml`** on push/PR:
  - lint: `golangci-lint run`.
  - test: unit `go test ./...`, then integration with a **Postgres service container** whose DB name contains "test" (`GEODRILL_TEST_DATABASE_URL=postgres://…/geodrill_test`), run `-p 1` (matches `testDSN` fuse + `freshSchema`).
  - build: `go build ./...` (uses `go.work` in CI where both repos are checked out — see 7.3).
- **`release.yml`** on tag: build + push `ghcr.io/supercakecrumb/geodrill` (`GITHUB_TOKEN`, `permissions: packages: write`) + GitHub Release with changie notes.

### 7.3 engram-unpublished Docker problem — concrete resolution

Until v0.3.0 is pinned and `go.work` dropped (final wave), the geodrill image can't resolve engram from the network. Resolution: **check out both repos in the workflow and copy engram into the build context.**
- In `release.yml` (and the CI build job): `actions/checkout` geodrill, then a second `actions/checkout` with `repository: supercakecrumb/engram`, `path: engram-src`.
- `docker build --build-context engram=./engram-src -t ghcr.io/...` and a Dockerfile build stage that does `COPY --from=engram . /engram` then `go mod edit -replace github.com/supercakecrumb/engram=/engram` (or sets `GOFLAGS` / a workspace) before `go build`. This removes the current `GOWORK=off` deadlock without publishing.
- After the final wave (v0.3.0 tagged, `require` pinned, `go.work` dropped, Aurora-approved), delete the second checkout + `--build-context` + `replace` — the plain module build works.

### 7.4 act validation + GHCR skip path
- Validate every workflow change locally with `act push` before pushing.
- The GHCR push step is guarded `if: ${{ !env.ACT }}` (act sets `ACT=true`), so lint/test/build run fully under act and only the push is skipped/dry-run — the rest stays verifiable.

### 7.5 Deploy seam
A committed but inert `deploy.yml` skeleton: `on: release: [published]`, a single `TODO` job (self-hosted PaaS pulls the GHCR image). No logic — just the clean trigger seam.

---

## 8. Wave / task decomposition

Rules honored: concurrent tasks never touch the same files; opus = architecture/API only, sonnet = implementation, haiku = data/doc grunt work; one atomic building commit per logical change; never `go mod tidy` until the final wave; CI bootstrapped early and maintained; `pg_dump` + `changie init` + stop-bot in wave 1; user-stats migration last.

Key conflict-avoidance device: a **`TopicGenerator` registry** (`internal/topics/registry.go`, defined once in wave 2) keyed by `quiz_kind`, so §6 topic workers each add an isolated package and never edit `train/service.go` or each other.

### Wave 1 — Bootstrap & safety (mostly orchestrator; tiny disjoint tasks)
| Task | Model | Repo/dirs | Depends | Deliverable | Verify |
|---|---|---|---|---|---|
| W1.1 changie init (geodrill) | haiku | geodrill `.changie.yaml`,`.changes/` | — | `changie init` commit | `changie batch --dry-run` |
| W1.2 changie init (engram) | haiku | engram `.changie.yaml`,`.changes/` | — | `changie init` commit | `changie batch --dry-run` |
| W1.3 pg_dump backup + stop-bot | orchestrator | (outside repo) | — | dated dump file (never committed); `pgrep -fl geodrill-bot` clean | `pg_restore --list` |

### Wave 2 — Foundations (serialized on shared surfaces; CI + engram parallel)
| Task | Model | Repo/dirs | Depends | Deliverable | Verify |
|---|---|---|---|---|---|
| W2.1 engram v0.3.0 impl | sonnet | engram `lifecycle.go`,`intro.go`,`queue.go`,`quiz/*`,`.gitignore` + tests | design (this doc) | §1 API + tests; `.gitignore` fix | `go build/vet/test -race ./...` |
| W2.2 schema+migrations+sqlc | sonnet | geodrill `migrations/000005,000006`,`sqlc.yaml`,`internal/storage/db/*`,`query/*.sql`,`storage/store.go`+`methods.go`(new methods, `WithTx`) | lifecycle int mapping (fixed here) | §2 tables + §5.5 tx seam | integration `-p 1`; `sqlc generate` clean |
| W2.3 topic framework skeleton | sonnet | geodrill `internal/topics/registry.go`,`internal/topic/*` | W2.2 | `TopicGenerator` iface + registry + tier/gating service | `go test ./internal/topics/...` |
| W2.4 CI bootstrap (both repos) | sonnet | `.github/workflows/*` in both | — (disjoint) | §7 workflows, act-validated, GHCR skip path | `act push`; `golangci-lint run` |

W2.1 and W2.4 are fully disjoint from geodrill core and run parallel with W2.2→W2.3.

### Wave 3 — Parallel topic workers + UI (disjoint dirs)
| Task | Model | Repo/dirs | Depends | Deliverable | Verify |
|---|---|---|---|---|---|
| W3.1 special chars | sonnet | `internal/topics/specialchars/*` | W2.3 | generator + audit test | `go test`; audit DSN |
| W3.1d chars data | haiku | `seeds/special_chars.yaml` | — | letter table | audit test in W3.1 |
| W3.2 road sides + generator | sonnet | `internal/topics/roadside/*` | W2.3 | generator + audit test | `go test`; audit DSN |
| W3.2d countries+roadside data | haiku | `seeds/countries.yaml`,`seeds/road_sides.yaml` | — | ~250 countries + sides + flag emoji | audit test |
| W3.3 words | sonnet | `internal/topics/words/*` | W2.3 | word→lang generator | `go test` |
| W3.3d words data | haiku | `seeds/common_words.yaml` | — | 5–10 words/lang | audit test |
| W3.4 Telegram UI + study/intro | sonnet | `internal/telegram/*`, `internal/study/*`, callback scheme, reminder-loop extension | W2.2, W2.3 | §5 flows (study card, /topics, /train, daily push) | `go test ./internal/telegram/...` |

W3.4 is the **sole writer** of `internal/telegram/` (incl. the reminder loop), so the intro-scheduler extension lives there; per-topic business logic stays in the topic packages. Topic workers never touch `telegram/` or `train/service.go`.

### Wave 4 — Integration (serialized; shared files)
| Task | Model | Repo/dirs | Depends | Deliverable | Verify |
|---|---|---|---|---|---|
| W4.1 old-quiz onto framework | sonnet | `internal/train/service.go`, `cmd/ingest`, `cmd/bot/main.go`, seed of language topics/items | W3.* | languages = `languages/guess-the-language`; in-DB backfill of `item_id`; register `language_id` generator | integration `-p 1` |
| W4.2 drop legacy | sonnet | `migrations/000007`, `sqlc.yaml`, `db/*` | W4.1 | drop decks/skills/user_skills/user_decks + legacy columns | integration up→down→up |
| W4.3 end-to-end wiring + gating | sonnet | `cmd/bot/main.go`, `internal/study`, `internal/topic` | W4.1/2 | intro→review→gating loop wired | `go build ./...`; live smoke |

### Wave 5 — Verification & closure
| Task | Model | Repo/dirs | Depends | Deliverable | Verify |
|---|---|---|---|---|---|
| W5.1 full test + live smoke | sonnet | — | W4 | every topic: intro (3 buttons) + review + gating live | `go test`; integration; single `pgrep` |
| W5.2 user-stats migration | sonnet | `cmd/statsmigrate/*` | W5.1, W1.3 backup | §3.5 tool; run backup→new | dry-run then apply; idempotent re-run |
| W5.3 release | orchestrator/sonnet | both repos | W5.1 | `changie batch`+merge; engram v0.3.0 tag+Release; geodrill pin `require`+drop `go.work`+`go mod tidy` (only now); GHCR image | Actions green; image pulled |
| W5.4 design docs | haiku/sonnet | `vibe/` (flags, country-profiles, TLDs, cities, rivers/mountains, plonkit topics) | — (parallel) | one design doc each | markdown review |
| W5.5 plonkit scraper prototype | sonnet | `cmd/plonkit/*` (isolated) | — (parallel) | learns site structure → seed JSON/YAML | manual run |
| W5.6 vault docs | haiku | `../../Projects/wiki/*` | W5.3 | `hot.md`, geodrill/engram pages, `log.md` top entry (incl. scrapped ideas) | link check |

Fold-in fixes: engram `.gitignore` (W2.1), `sqlc.yaml` appended (W2.2/W4.2). CI kept green by W2.4 owners as code lands. W5.4/W5.5 are parallelizable early since they touch only `vibe/` and an isolated `cmd/`, but the brief sequences them at closure.

---

## 9. Risks & open questions (for Aurora)

1. **Tier assignments** — the rubric (§4) needs sign-off *before* any item gets a tier; the per-item tier tables (which letter/country/word is tier k) are the sign-off artifact.
2. **"Good shape" threshold** — proposed `state=Review AND stability≥21d` (or `known`), and tier-complete at **80%** introduced-and-good-shape. Confirm the 21-day / 80% numbers.
3. **`daily_intro_cap` default = 10** (from decision 2) — confirm it is a `/settings` control now or later.
4. **Subdivision flag emoji** — England/Scotland/Wales use Unicode tag sequences (🏴󠁧󠁢󠁳󠁣󠁴󠁿); rendering varies by client. Fallback text (e.g. "🏴 Scotland") may be needed — confirm acceptable.
5. **`user_topics` opt-in/out** — keep the `/decks`-style on/off affordance (secondary to tier gating) or fully tier-gate and drop it? Proposed: keep, default-on.
6. **word→meaning mode** — designed, not built (decision D). Confirm it stays deferred.
7. **plonkit topic list** — genuinely unknown until the scraper (W5.5) runs; tier-5 meta topics are provisional until then.

---

### Critical Files for Implementation
- /Users/val-kiel/Personal/PersonalProjectsMD/Packages/engram/scheduler.go — add `Introduce`/`SeedCardKnown`; the lossless `CardState`↔`fsrs.Card` contract constrains where `Lifecycle` may live.
- /Users/val-kiel/Personal/PersonalProjectsMD/Packages/engram/queue.go — add `NextReview` + `NextIntroductions`; move the novelty gate from review to introductions.
- /Users/val-kiel/Personal/PersonalProjectsMD/PersonalProjects/geodrill/migrations/000001_init.up.sql — reference shape for the `000005+` v2 DDL; `sqlc.yaml` must list every new up.sql.
- /Users/val-kiel/Personal/PersonalProjectsMD/PersonalProjects/geodrill/internal/train/service.go — the sole engram+quiz→storage wiring; gains the `TopicGenerator` registry and the introduction/gating transaction seam.
- /Users/val-kiel/Personal/PersonalProjectsMD/PersonalProjects/geodrill/internal/telegram/handlers.go and /Users/val-kiel/Personal/PersonalProjectsMD/PersonalProjects/geodrill/internal/telegram/bot.go — the prefix-based callback router and the reminder loop that the study card, `/topics`, and the daily intro push extend.
