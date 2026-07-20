# Design — Multi-language sign questions (type N official languages)

Status: **DRAFT for review** (ticket "Multi-language sign questions (type N official
languages)" — the ticket says *needs design before build*; this is that design).
Owner: Aurora. Drafted: 2026-07-21.

Cross-refs: [`design-country-profiles.md`](design-country-profiles.md) (the existing
`profiles` topic this evolves), `internal/topics/profiles/generator.go`,
`internal/topics/specialchars/alias.go` (language alias/accept table),
`internal/suggest/suggest.go` (`DomainLanguage`, wired 2026-07-21),
`internal/study/trainer.go` (`AnswerText`, `AnswerDontKnow`, grading),
`migrations/000001_init.up.sql` (`exercises` table).

## 1. Problem

The `profiles` topic asks "What language do you see on signs in 🇪🇸 Spain?" today as a
**single-choice** MCQ. Two shortcomings:

1. **Option list, not typable.** Aurora's standing preference is free-text entry with
   autocomplete (like country/capital/TLD questions), not a button list.
2. **Multi-language countries are lossy.** To keep `ModeSingle` available the generator
   collapses each country to its **primary** language only (`Languages[0]`), discarding the
   secondary official languages (see the `profiles` package doc: Kenya → Swahili only, English
   dropped). A learner who knows "English is also official in Kenya" gets no credit, and the
   whole point of the topic — *which* languages appear on signs — is under-tested.

The fix has two parts, and they are coupled by the answer-entry mechanic.

## 2. Building blocks already in place (as of 2026-07-21)

- **Autocomplete for languages.** `suggest.DomainLanguage` exists; the inline index now carries
  one entry per language (`specialchars.Languages()` → `suggest.LanguageEntry`), and
  `DomainForAnswer` resolves a language-name `CorrectAnswer` to `DomainLanguage`. Any ModeText
  profiles exercise whose `CorrectAnswer` is a language name gets language autocomplete for free.
- **Generator-driven autocomplete opt-in.** `engine.Descriptor.Autocomplete bool` → stamped onto
  `Exercise.Autocomplete` → renders the "⌨️ Type your answer" button.
- **Reveal-on-wrong** (`answerText`, `AnswerResult.CorrectAnswer`) and the **"🤷 I don't know"**
  button (`AnswerDontKnow`) both exist and will be reused by the N-answer flow.
- Quiz modes today: `ModeSingle`, `ModeSet` (multi-select **buttons**, graded by set-equality),
  `ModeText` (one free-typed answer, fuzzy-matched).

## 3. Part 1 — Typable single-answer (low risk, buildable after sign-off)

Convert `profiles` from `ModeSingle` to `ModeText` + autocomplete.

- Set `Autocomplete: true` on the profiles descriptor; give the descriptor an `Accept`/`PromptText`
  so the engine emits `ModeText`. `CorrectAnswer` stays the display language name (already the
  case), so `DomainLanguage` autocomplete resolves.
- **Accept spellings.** Need language-name → accepted-spellings (English name + native aliases +
  ISO code), like `specialchars.acceptSpellings`. The two topics key languages differently
  (`specialchars` by ISO-639-3 code; `profiles` by display name/slug). **Decision needed (Q1):**
  extract a shared `languages` helper package keyed by English name, or give `profiles` its own
  minimal alias set. Recommendation: small shared resolver `name → []acceptSpellings`, since #6
  already needed the language list and a third consumer (N-answer) is coming.
- **Multi-language fairness in Part 1 (interim).** For a multi-language country in single-answer
  mode, **accept ANY of the country's official languages** as correct (not only the primary), so
  the learner isn't marked wrong for a genuinely-correct secondary language. `CorrectAnswer` shown
  on reveal = the primary (or the full list). This is the honest single-answer behaviour until
  Part 2 lands. **Decision needed (Q2):** accept-any vs still-primary-only.
- The own-language distractor-exclusion logic (`BuildExercise` post-filter, `capOptions`) becomes
  dead for text mode — remove it in the same change (no parallel paths; edit in place).

Part 1 alone already satisfies ticket bullet 1 ("typable, with autocomplete") and materially
improves multi-language fairness. It can ship as one atomic commit.

## 4. Part 2 — N-answer mode ("type all N official languages")

The new capability: "Which languages are official in 🇨🇭 Switzerland?" where the user must type
**all N** (German, French, Italian, Romansh), tracked across turns.

### 4.1 New quiz mode

Add `ModeMultiText` to `engram/quiz` (a "collect N distinct accepted answers" matcher). Engram
stays domain-agnostic: the matcher takes N accepted-spelling sets + the already-collected set +
the new typed answer, and reports {matched-which, already-had, no-match}. **Decision needed (Q3):**
new `ModeMultiText` vs overloading `ModeText` with an expected-count — recommend a new mode so
`ModeText` grading stays a simple yes/no.

### 4.2 Grading rule (Aurora's proposal, made precise)

- There are **N** expected answers (the country's official languages).
- Each typed reply is normalised (`quiz.Normalize`) and matched against the **outstanding** set.
  - **New correct** → mark it collected; if all N collected → **card passes**.
  - **Duplicate** (already collected) → no-op, gentle nudge ("already got German — 1 left"),
    does **not** consume the mistake budget.
  - **No match** → consumes one **mistake**.
- The card **fails only after N mistakes** (budget = N wrong attempts; correct/duplicate replies
  are unlimited). On the N-th mistake → fail, reveal the remaining answers. **Decision needed
  (Q4):** budget = N mistakes (recommended, matches the ticket) vs N total attempts.
- **"I don't know" / idk** at any point → immediate fail + reveal all remaining (reuses `#8`).

### 4.3 FSRS rating

Binary (`engram.RatingForAnswer`): **pass** iff all N collected within the mistake budget, else
**fail**. **Decision needed (Q5):** binary vs partial credit (e.g. Good if ≥⌈N/2⌉). Recommend
binary for v1 — engram's rating is binary and partial credit muddies the SRS signal.

### 4.4 Persistence of partial progress

The `exercises` table has no "collected so far" column. Two options:
- **(A, recommended)** Add a nullable `progress jsonb` to `exercises`
  (`{"collected":["deu","fra"],"mistakes":1}`), updated in the same tx that records each partial
  turn. Per the one-squashed-migration rule: **edit `000001_init.up.sql` in place + reset the dev
  DB**, never a new migration file.
- **(B)** Reconstruct progress from `reviews` rows for the open exercise. Avoids a schema change
  but overloads the append-only review log with intra-question state. Not recommended.

**Decision needed (Q6):** A vs B.

### 4.5 Multi-turn UX

- `AnswerText` today routes a typed reply to the caller's single open `ModeText` exercise. Extend
  the open-exercise lookup to include `ModeMultiText`; on a partial correct/duplicate/miss it
  **edits the question message in place** to a progress view — e.g.
  `🇨🇭 Switzerland — official languages (2/4): ✅ German ✅ French · ◻️ ◻️ · mistakes 1/4` — and
  keeps the exercise **open** (does NOT advance, does NOT stamp `answered_at`).
- Only on completion (all N) or fail (N mistakes / idk) does it stamp `answered_at`, write the
  review, run the scheduler, and advance — the existing `finishAnswer` path.
- Autocomplete suggestions exclude already-collected languages.
- `N == 1` collapses exactly to Part 1 behaviour (no special-casing needed).

### 4.6 Which items get N-answer

**Decision needed (Q7):** all multi-language countries in `seeds/country_profiles.yaml`, or a
curated subset? Some countries list many minority languages; "official languages" should be a
deliberate, seeded field, not "everything spoken". Recommend: N-answer only over an explicit
`official_languages` seed field (curated), separate from `languages_spoken`.

## 5. Recommended phasing & commits

1. **Phase A (buildable now):** shared `languages` accept-spelling helper (Q1); `profiles` →
   `ModeText` + `Autocomplete`; accept-any-official-language fairness (Q2); delete the dead
   distractor-exclusion path. One atomic commit. Closes ticket bullet 1.
2. **Phase B (after sign-off):** `ModeMultiText` in engram + matcher + tests (engram commit);
   `exercises.progress` schema (Q6); the N-answer collect/grade/persist flow in `study`; the
   multi-turn edit-in-place UX in `telegram`; profiles `official_languages` seed (Q7). Several
   atomic commits, engram-first (co-developed via `go.work`).

## 6. Decisions needed from Aurora before Phase B build

Q1 shared language helper vs per-topic · Q2 accept-any vs primary-only in Part 1 · Q3 new
`ModeMultiText` vs overload `ModeText` · Q4 budget = N mistakes vs N attempts · Q5 binary vs
partial rating · Q6 `progress jsonb` column vs reconstruct-from-reviews · Q7 curated
`official_languages` seed field vs all spoken languages.
