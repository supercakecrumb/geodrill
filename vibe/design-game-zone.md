# Game zone — guess-the-language leaves the study pipeline

Decision (Aurora, 2026-07-19): sentences are not SRS material — there is no value in
"repeating" a random sentence, so guess-the-language must not go through
introductions/FSRS per language item. It becomes a **game**: a quick test of your
recognition ability, with streaks and a personal best. This doc fixes the design;
the board card "Move sentence quiz into a new game zone" tracks it.

## The game zone

- **`/game`** is the entry command — a small menu of available games. Today it has
  one game, **Language Roulette**; the future timed city-listing game (board
  Backlog) will slot into the same menu.
- Games are ephemeral sessions, not scheduled study. Only aggregate stats persist.

## Language Roulette

Endless streak run:

1. Each round shows a random sentence from the ingested corpus (any of the
   languages with content) + 4 language buttons (1 correct, 3 distractors).
2. Correct → streak +1, message edits in place to the next round (no repeat
   sentences within a run).
3. Wrong → run over: final streak, personal best, runs played, the language tip
   for the missed language when one exists, and «▶️ Play again» / «🏁 Done».

**Difficulty ramps with the streak** (distractors get closer to the answer):

| Streak | Distractor policy |
|--------|-------------------|
| 0–4    | different language groups (cross-script, visually distinct) |
| 5–14   | at least one distractor from the same group |
| 15+    | all distractors from the same group when it has ≥3 siblings; else the 5–14 policy |

Round generation is deterministic given the injected rng (same testing pattern as
topic generators).

## Persistence

New table `game_stats` (edited into `000001_init` in place, single-migration rule):
`user_id`, `game` (text key, `language_roulette`), `best_streak`, `runs`,
`last_played_at`; PK `(user_id, game)`. No per-run log — the `reviews` table stays
FSRS-only. Run state (streak, open answer, used content ids) is in-memory per chat,
like the open-exercise tracking in the telegram layer.

## What happens to the FSRS state of language items

- Guess-the-language topics are seeded with **`is_quizzable = false`**: no
  introductions, no reviews, excluded from /study, /train and tier gating. The
  language items stay seeded — they carry the group structure, names, flags and
  payload the game needs.
- The /topics browser hides subtrees with no quizzable descendants (generic rule,
  no slug switches) — the languages subtree disappears from the study surface.
- Existing `user_items` / `introductions` rows for language items are not
  migrated; the dev DB is reset after the schema change (data disposable —
  Aurora, 2026-07-19).
- The guesslang generator/intro code is superseded and gets deleted in the same
  change; its tips content moves to the game. Package layout: game logic in
  `internal/game` (round building, difficulty, stats), telegram surface in
  `internal/telegram/game.go`, callback namespace `game:`.
