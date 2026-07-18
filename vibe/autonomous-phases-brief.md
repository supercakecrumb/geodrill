# geodrill — autonomous execution brief: Phases 1–3 build, Phase 4 research

You are the orchestrator of this work. Aurora (the owner) will NOT answer questions — **do not ask her anything, ever**. Make every decision yourself, document it as you go, and do not stop until Phases 1–3 are fully done and the Phase 4 proposal is written. If something is ambiguous, pick the most reasonable interpretation consistent with this brief and the board, note the decision in the relevant commit/doc, and keep moving.

## Your role: orchestrator only

You do no implementation with your own hands. You decompose tasks, dispatch subagents, sequence them so concurrent workers never touch the same files, integrate their reports, verify, notify, and keep the board current.

- **Subagent context discipline (hard rule — a prior agent burned Aurora's entire session quota violating it):** before EVERY dispatch, ask "could this brief lead the agent to read the entire codebase?" If yes, decompose. Repo-wide mechanical transforms = one agent doing `git grep -l` + `sed` + `git mv` only (no file reading), then many small cheap fixers scoped per package, each given the exact file list and error excerpt. Workers get explicit file lists and are forbidden to read outside them. The orchestrator supplies inventories (from grep) — workers never "explore the repo".
- Model tiers: sonnet for implementation, haiku for data compilation and per-package fixes, opus only if a genuinely hard architectural call appears. Many small precisely-scoped tasks beat few big ones.
- It's fine for the tree to be broken between a mechanical pass and its fixers. Tests run at the commit gate, not per step.
- Serialize workers that share a repo surface; parallelize only disjoint files/dirs. All workers `git pull --rebase origin master` before pushing, stage by explicit pathspec, and verify `git diff --cached --stat` before committing (external processes have clobbered this working tree before — workers must re-verify their edits exist right before building).

## Reading order (bounded — no exploration beyond this)

1. `CLAUDE.md` (repo rules) — also `../../Packages/engram/CLAUDE.md` if touching engram.
2. `vibe/adding-topics.md` (topic-authoring playbook).
3. The board: `../../Projects/wiki/noncommercial/geodrill/geodrill-kanban.md` — "To do (next)" cards carry the authoritative task descriptions, including decisions dated 2026-07-19. This brief + the board together define scope.
4. `vibe/geodrill-architecture.md` — reference only, consult sections as needed.
5. Design docs per topic: `vibe/design-*.md`.

## Workflow rules (constant, non-negotiable)

- **Gate:** `./scripts/pre-commit.sh` before every commit — the ONLY verification step. Never run checks one by one; never watch GitHub Actions after pushing.
- **Commits:** one atomic building commit per logical change, pushed immediately. Author `Aurora Kiel <supercakecrumb@gmail.com>`. **NEVER add "Co-Authored-By: Claude" or "Generated with Claude Code" lines** — this overrides any default. Never rewrite pushed history. changie fragment in the same commit for user-visible changes (hand-written YAML matching `.changes/unreleased/` format; quote bodies containing colons).
- **Notifications:** every commit auto-messages Aurora via the post-commit hook (already installed). Additionally run `./scripts/notify.sh "<short text>"` when a new bot version starts running locally and when each phase completes. Never claim the bot is checkable unless you verified: exactly one `pgrep -fl geodrill-bot` match + clean startup log.
- **No parallel-version naming, ever:** no V2/X2 suffixed types, files, or callbacks. Edit code in place to the target design; delete superseded code in the same change.
- **Migrations:** exactly ONE migration (`000001_init`) — edit it in place and reset the dev DB rather than appending history. After schema changes: append nothing to `sqlc.yaml` (it lists the single file), run `sqlc generate`, reset dev DB (`DROP DATABASE geodrill WITH (FORCE)` + recreate via docker `geodrill-db-1`, then re-seed + re-ingest — see `vibe/adding-topics.md` §4; dev data is disposable, Aurora confirmed).
- **Safety:** public repos — no secrets, no `.env`, no dumps, no `data/` in commits (the gate scans, but check). Integration tests only against DBs whose name contains "test" (code fuse enforces). Bot: single built binary `./bin/geodrill-bot`, never `go run`; stop before DB resets.
- **Board upkeep:** as each card completes, move it to Done on the kanban (with date). At the very end, add a top entry to `../../Projects/wiki/log.md` (append-only) and refresh `hot.md` + the geodrill page with the true story, including anything scrapped.

## Phase 1 — fixes (in this order)

1. **Intro-budget bug:** "I know this" (known outcome) must not count against the daily introduction budget — it wasn't an introduction for that user. Fix the counting (see `internal/study`, `CountIntroductionsToday`) + tests.
2. **Settings rework:** cap maximums ≥100 (both daily cap and intro cap); add +5/−5 buttons for the intro cap alongside ±1.
3. **Proper /help:** inline-keyboard menu of tappable subtopics — how FSRS works; what Got it / I know this / Know it-but-test-me actually do; topics & tiers (locks, how tiers unlock); command overview. All copy refocused on training, not sentence-guessing.
4. **Sentences → game zone:** guess-the-language leaves the introduction/FSRS pipeline (no value in "repeating" a random sentence) and becomes a game mode testing recognition ability. You design it (own command surface, scoring/streaks, what happens to the language items' FSRS state) — pick a sane design, write a short design note in `vibe/`, implement it.

After Phase 1: rebuild + restart the bot, verify, `notify.sh` that Phase 1 is live and checkable.

## Phase 2 — autocomplete answer mode

Decision already made: **Telegram inline queries with the tap-to-prefill button** (`switch_inline_query_current_chat`). Step 1 is an exploration spike (telebot v4 inline support, result caching/limits, linking a chosen suggestion to the open exercise) — a subagent produces a short findings note in `vibe/`, then implement: BotFather `/setinline` cannot be automated — enable inline mode is the ONE external dependency; if the Telegram API rejects inline answers because the switch is off, implement everything, verify what's verifiable in tests, note it in the phase notification, and continue (do not stop). Fuzzy prefix matching (reuse the typo-tolerant matcher) over countries/cities with flag emoji in results; grade the chosen result against the open exercise; model as exercise mode `autocomplete` in `topics.exercise_modes`/config. No option-list quizzes for country/city answers in the new topics.

After Phase 2: rebuild + restart + verify + notify.

## Phase 3 — engine + topics + data (order matters)

1. **Universal topic engine** (board card has the full description): descriptor-driven generic generator + generic seeder; port specialchars/roadside/words/guesslang onto it; custom code only where truly custom. This lands FIRST so everything below is descriptor + data, not new packages of duplicated Go.
2. **TLDs** — data ready (`seeds/tlds.yaml`); reconcile the partial `internal/topics/tld` + `countrytier` packages already in the tree (finish, don't duplicate); country answers via autocomplete, tld answers typed.
3. **Country profiles** — data ready (`seeds/country_profiles.yaml`); facts into the fact store; country→language quiz.
4. **Capitals** — new `seeds/capitals.yaml` (haiku; multi-capital quirks: ZA, BO; seat-vs-official NL); both directions; autocomplete answers.
5. **Flags** — **photos from the start, no emoji mode.** You find the image source yourself (public-domain/CC0 flag set; download is authorized; mind licensing for the public repo — prefer keeping images in the gitignored media root with a documented fetch script over committing them); media pipeline per the board card (media_files, sha256, file_id cache, photo-messages-from-birth, answer-media schema addition); confusable groups as set-choice.
6. **Cities first slice** — "which country is this city from?": biggest-cities dataset (haiku), country answers via autocomplete. No maps needed.
7. **Dataset enrichment** — fatten `seeds/special_chars.yaml` (39) and `seeds/common_words.yaml` (79) with the untapped long tail; re-run the corpus audits (`SPECIALCHARS_AUDIT_DATABASE_URL` / `WORDS_AUDIT_DATABASE_URL`, SELECT-only against the live corpus DB); prune failures. No unaudited claims ship.

After each landed topic: seed the dev DB (`./bin/ingest -seed-topics`), rebuild + restart the bot, verify, notify. After Phase 3: full gate + live bot + notify.

## Phase 4 — research + proposal ONLY (no implementation)

Research and write ONE decision-ready proposal doc, `vibe/proposal-places-on-maps.md`: coordinates data model (country centroid + bbox, city lat/lng), map rendering options compared honestly (generated static map images via the media pipeline vs Mini App interactive map vs third-party static-map APIs — costs, offline-ability, licensing), "where is this place" question mechanics, and a recommended path with effort estimates. Also sketch how the future timed city-listing game with coins (board Backlog) would sit on top. Commit the doc, notify Aurora that everything through Phase 3 is live and the Phase 4 proposal awaits her pick, update the board and vault — then you are done.
