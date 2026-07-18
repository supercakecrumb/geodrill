# geodrill v2 — multi-topic GeoGuessr trainer: orchestration brief

## Your role: orchestrator ONLY — read this first

You are the orchestrator of a subagent swarm. **You do no work yourself. None.**

- You never Read, Grep, Edit, Write, or run Bash with your own hands. Even code exploration is delegated to Explore subagents that report back to you.
- Your only jobs: decompose the work into tasks, dispatch subagents, sequence waves so no two concurrent tasks touch the same files, integrate reports, dispatch verification subagents, and talk to Aurora.
- Delegate as many tasks as reasonable — prefer many small, precisely scoped tasks over a few big ones. Every task brief must be self-contained (paths, context, constraints, verification commands) because workers start cold.
- Model tiers: **opus** for architecture, schema design, and the engram API; **sonnet** for topic implementations, Telegram UI, migrations, tests; **haiku** for data grunt work (letter tables, country data, word lists) and doc formatting. Keep it reasonable — do not run everything on opus.
- Nesting is realistically one level (you → workers). Do not design plans that require workers to spawn their own swarms.
- Start in **plan mode**. Build the plan from subagent exploration reports, present it to Aurora — including the tier rubric (below) — and exit plan mode only with approval.

## Vision

geodrill (Telegram bot @geodriller_bot; Go 1.26, Postgres, FSRS via the engram library) currently does one thing: guess-the-language quizzes on sentences. It becomes a **multi-topic GeoGuessr trainer** where language guessing is just one feature. Topics live in a tree, top level roughly `languages / roads / countries / …`, with e.g. `languages/guess-the-language`, `languages/special-characters` beneath. Aurora does not know the final structure yet and will rearrange it later — **the tree must be trivially easy to restructure; nothing may hard-code the hierarchy.**

New core mechanic: **introduction before review.** No entity (letter, word, fact, flag…) ever appears as a quiz question before it has been introduced to that user.

## What exists (subagents explore this; you don't)

- Repos: `PersonalProjects/geodrill` (app) and `Packages/engram` (SRS library), co-developed via the committed `go.work`. Both have **public GitHub remotes** on `origin/master`: `github.com/supercakecrumb/geodrill` and `github.com/supercakecrumb/engram` (tags `v0.1.0`/`v0.2.0` pushed). The full pre-restructure state is already committed and pushed.
- Context for exploration agents: `Projects/wiki/hot.md`, `Projects/wiki/noncommercial/geodrill/geodrill.md`, `Projects/wiki/packages/engram/engram.md`, each repo's `PROGRESS.md` and `CLAUDE.md`.
- Current features: `/decks`, `/train`, `/practice` (with stop control), `/stats`, `/settings`, daily reminders with follow-up nudges, FSRS scheduling, recognition tips (Romance pilot; engram `TipProvider`, v0.2.0).
- Neither repo has CI yet (no `.github/`). Local tooling installed and ready: `changie`, `act`, `golangci-lint`, `docker`.

## Decisions already made — do not relitigate

1. **Lifecycle lives in engram (v0.3.0).** Item states: new → introduced → reviewing, plus a terminal-ish `known/learnt`. An item enters FSRS only after the user confirms its introduction.
2. **Introduction UX.** The bot proactively messages daily: "you have N items to introduce" (default N=10, per-user later) with a start button, then steps through one card per item; `/introduce` fetches more on demand. Every introduction card has **three buttons**: (a) **Got it** → into the review queue as a new FSRS card; (b) **I know this** → state `known`, never shown again; (c) **Know it, but test me** → into FSRS with high initial stability. This applies to every topic, including letters and words. Introductions must be re-viewable later (design the mechanism). State is per-item per-user. Undoing `known` arrives later via a web UI — the schema must support it now.
3. **Exercise modes in engram:** single-choice MCQ (existing); **set-choice**, where each button is a set of answers (e.g. "ø" → "Norwegian/Danish" vs "Swedish/Icelandic" vs "Norwegian/Icelandic"); **free-text typed** (case-insensitive, typo tolerance edit distance ≤2, alias table e.g. "Norsk" → Norwegian). No reverse modes that require typing foreign characters.
4. **Tiers.** Every item has a tier; each topic has a base tier and may override per item. Gating is **global across all topics**: tiers 0 and 1 are open from the start; completing tier n unlocks tier n+2. "Complete" = every tier-n item introduced AND a threshold share of them in good FSRS shape (propose the threshold). The plan must include a written **tier rubric** (tier 0 = universally known … tier ~5 = meta knowledge like bollards) for Aurora's sign-off before any item is assigned a tier.
5. **Storage: Postgres only** — the graph-DB idea was evaluated and rejected. Full schema redesign is allowed and expected. **Countries are a first-class entity**: full ISO 3166-1 (~250, incl. Isle of Man etc.) plus GB subdivisions (England/Scotland/Wales), each with status flags `un_member` and `gg_coverage`; road sides, languages, flags, profiles, and all future topics link to countries. **Country facts must be modeled flexibly and queryably**: normalized, typed fact storage that answers arbitrary filters in plain SQL ("which countries drive on the left AND have Islam as the main religion") and absorbs entirely new datasets later — economics, LGBT-friendliness, safety, whatever Aurora adds — without schema surgery. This data will eventually power a public **map/stats-explorer website**; the website itself is NOT in scope, but the schema must not paint us out of it. Keep sqlc + the migrations tooling.
6. **Media.** Photos via local files + Telegram file_ids. Telegram cannot edit a text message into a photo message, so media questions are photo messages from birth (edit caption + markup in place). Whenever a country appears in text or on a button, prefix its flag emoji.
7. **Command surface redesigned** around the topic tree — e.g. `/study` (introductions), `/train` (reviews across topics), `/topics` (tree, tiers, progress). The old sentence quiz becomes `languages/guess-the-language` inside the framework — behavior preserved, internals refactored onto the new framework.

## Build now (implement, with data)

A. **Framework:** schema, engram v0.3.0 lifecycle + exercise modes, topic tree, tiers + global gating, introduction flow, daily push scheduler, redesigned commands.
B. **Special characters topic** — Cyrillic + Latin, all GeoGuessr-relevant languages (start from the current ~30-language deck list): letters unique to one language AND letters unique to a subgroup (quizzed via set-choice); char → language in both choice and typed modes. Data must be verifiable — no fabricated claims; use an audit step against authoritative sources/corpus, like the tips feature did.
C. **Road-side topic** — every country, Left/Right buttons, flag emoji on the country.
D. **Common words topic** — 5–10 high-value GeoGuessr words per Cyrillic/Latin language ("street", "road", …); word → language mode built now; word → meaning designed but not built.
E. **Old-quiz migration** into the framework (see decision 7).
F. **User-data migration.** Before ANY destructive schema change, `pg_dump` the live dev DB to a dated backup file. Dev data may be destroyed freely during the build, but the final wave must migrate existing users' FSRS statistics from that backup into the new schema. (History: a migrate-down once wiped this DB. Treat this as sacred.)
G. **CI/CD — GitHub Actions in both repos**, created early and maintained continuously by dedicated subagents as the code evolves (not bolted on at the end):
   - **lint** (golangci-lint), **test** (unit always; integration against a disposable Postgres service container whose DB name contains "test"), **build** — on every push.
   - **Release on tag:** geodrill builds its container and pushes **`ghcr.io/supercakecrumb/geodrill`** (GITHUB_TOKEN, `packages: write`); GitHub Releases in both repos with changie-generated notes. engram is a library — tag + Release only, no image.
   - **Validate workflows locally with `act` before pushing them** (installed). Jobs that can't run under act (GHCR pushes) get a dry-run/skip path so the rest is still verifiable.
   - **Deploy workflows are out of scope** — Aurora's deployment method isn't decided yet (direction: self-hosted PaaS pulling GHCR images). Leave a clean seam for a future deploy job triggered off releases.

## Architecture only — design docs + concrete implementation steps, no data fill

Each of these gets a short design doc in geodrill's `vibe/` with step-by-step implementation instructions a future agent can execute:

- **Flags quiz** (incl. subdivision flags; flag images from public-domain sets → local files → file_ids).
- **Country profiles** (languages spoken, scripts, dominant religion, region — extensible fact schema; e.g. "what language is used in Kenya / Nigeria").
- **Domains/TLDs.**
- **Cities** (studied biggest → smallest; map position, terrain, elevation, rivers).
- **Rivers, mountains.**
- **plonkit-derived topics** (bollards, license plates, poles, …) — the full topic list emerges only after scraping plonkit.net.
- **plonkit scraper:** a `cmd/` tool that learns the site structure and writes structured seed files (JSON/YAML); manual runs now, cron-able later. Aurora has the site developer's permission to scrape and reuse. A subagent may prototype it in parallel — fully isolated, must not touch shared code.

## Constraints — non-negotiable

- **NEVER run `go mod tidy` in geodrill during the build.** engram is published now, but v0.3.0 is co-developed *unreleased* — resolution stays via the committed `go.work`. Only in the final wave, after the v0.3.0 tag is pushed and with Aurora's approval, may geodrill pin `require github.com/supercakecrumb/engram v0.3.0` and drop `go.work` — which also unblocks the geodrill Docker build in CI (until then, the image build must copy engram into the build context).
- **Git workflow — commit-by-commit, never batched.** Both repos now have public GitHub remotes (`origin/master`): `github.com/supercakecrumb/geodrill` and `github.com/supercakecrumb/engram`. You push to GitHub yourself as you go — **one atomic, self-contained, building commit per logical change**, pushed immediately. Never lump many unrelated changes into one commit. Each commit must compile and keep tests green on its own.
- **Changelogs via `changie`.** Use [changie](https://changie.dev) for changelog management in both repos. If a repo has no `.changie.yaml` yet, initialize it first (`changie init`) as its own commit. Every change that users/maintainers would care about gets a `changie new` fragment in the same commit; batch fragments into a release (`changie batch` + `changie merge`) at each version bump.
- **Secrets never leave local.** `.env` and `*.log` are gitignored — keep it that way; both repos are **public**. Never commit tokens, `.env`, logs, or DB dumps. Scan staged content before every push.
- **Test safety:** any test DB URL must contain "test". Never point tests or migrations at the live dev DB — the only sanctioned touch is the final stats migration, and only after the pg_dump backup exists.
- **Done =** code + tests green + bot builds and runs locally (single `./bin/geodrill-bot`; verify via startup logs and exactly one process in `pgrep`) + a live local smoke test of every built topic (introduction flow, all three buttons, review flow, gating).
- Tag engram **v0.3.0** after green and push the tag to `origin` (a changie release commit accompanies the bump).
- **Final wave: update the Obsidian vault** — `hot.md`, the geodrill page, the engram page, and a new top entry in `log.md` telling the true story, including anything scrapped along the way.

## Progress pings to Aurora (non-blocking)

After **every wave completes** (and after any standalone milestone worth seeing — e.g. a topic worker finishing, a live smoke test passing), the orchestrator sends Aurora a short Telegram message via `curl` and then **immediately continues to the next wave — this is a status ping, not an approval gate. Do not wait for a reply.** (The one real blocking gate is the plan-mode approval in wave 1; everything after that is fire-and-forget notifications.)

- Chat ID: `371532208`.
- Token: **read `TELEGRAM_TOKEN` out of `.env` at send time — never print it, log it, or write it into any file** (this repo is public; `vibe/` gets committed).
- Send pattern (bash):
  ```bash
  set -a; source .env; set +a
  curl -s -X POST "https://api.telegram.org/bot${TELEGRAM_TOKEN}/sendMessage" \
    --data-urlencode "chat_id=371532208" \
    --data-urlencode "text=✅ <what just finished — 1-3 sentences>. Bot is running locally at ./bin/geodrill-bot — go ahead and check it out. Continuing to <next stage>."
  ```
- Keep messages short and concrete: what shipped, that it's runnable locally right now, and what's next. A failed `curl` (network hiccup, bad token) must never block or fail the build — log it and move on.
- **Only send a ping once the bot is actually runnable in that state** — rebuild `./bin/geodrill-bot`, restart it, confirm exactly one process (`pgrep -fl geodrill-bot`) and a clean startup log, *then* notify. Don't claim something is checkable if it isn't running.

## Suggested wave shape (adapt, but keep the conflict-free property)

0. **Exploration** (parallel Explore agents): geodrill code map; engram API surface; current schema; current Telegram flow. Reports only.
1. **Architecture** (opus): schema, engram v0.3.0 contract, topic-tree model, tier rubric, migration strategy → assemble the plan → Aurora approves → exit plan mode → confirm no `geodrill-bot` process is running (`pgrep -fl geodrill-bot`; stop it if so — it must not be serving live traffic against a DB about to be migrated) → `changie init` in each repo (if absent) + pg_dump backup of the dev DB before any destructive schema work. (The pre-restructure state is already committed and pushed to `origin/master` in both repos.)
2. **Foundations** (serialized — shared surfaces): engram v0.3.0 → schema + sqlc + migrations → topic framework skeleton. In parallel (disjoint files): **CI bootstrap worker** — lint/test/build workflows in both repos, `act`-validated, so every subsequent push is checked.
3. **Parallel topic workers** (sonnet; disjoint directories): special characters; road sides + countries dataset (haiku compiles data); words; Telegram UI/commands.
4. **Integration** (serialized): old-quiz migration, wiring, end-to-end pass.
5. **Verification + closure:** full test run, live smoke test, user-stats migration from the backup, **release** (changie batch → engram v0.3.0 tag + geodrill release tag → Actions green → GHCR image published), future-topic design docs, scraper prototype, vault docs.

If context runs short, finish in this order: framework → special characters → old-quiz migration → road sides → words → stats migration → docs.
