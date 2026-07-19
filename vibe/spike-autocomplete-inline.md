# Spike: type-ahead answers via Telegram inline mode

Research-only spike (no implementation) for Google-style autocomplete on
exercise answers: tap a `switch_inline_query_current_chat` button → prefilled
`@geodriller_bot ` → type → inline suggestions → tap one → it lands in the
chat → grade it against the open exercise. Answers below cite exact types
from the installed `gopkg.in/telebot.v4 v4.0.0-beta.10`
(`$GOMODCACHE/gopkg.in/telebot.v4@v4.0.0-beta.10/`) plus the official Bot API
docs. Current `internal/telegram/bot.go` registers only `/command` handlers,
`telebot.OnCallback`, and `telebot.OnText` (`b.wrap(...)` pattern) — no
inline-mode handler exists yet.

## 1. telebot v4 inline support

- **Handler**: `telebot.OnQuery` (`= "\aquery"`, `telebot.go:102`). Registered
  the same way as existing handlers: `tb.Handle(telebot.OnQuery, b.wrap(...))`
  — except the handler signature must accept `telebot.Context` directly
  (`c.Query()` returns `*telebot.Query{ID, Sender, Location, Text, Offset,
  ChatType}`, `inline.go:12-30`), it won't fit the repo's `Session` interface
  without an adapter (`Session` has no `Query()`/`Answer()` accessors today).
- **Answering**: `c.Answer(&telebot.QueryResponse{...})` (`context.go:611`),
  which delegates to `Bot.Answer(query, resp)` →
  `b.Raw("answerInlineQuery", resp)` (`bot.go:939-947`). `QueryResponse`
  (`inline.go:33-69`) fields: `Results Results`, `CacheTime int`,
  `IsPersonal bool`, `NextOffset string`, plus `SwitchPMText`/
  `SwitchPMParameter`/`Button` for the "no results, deep-link to /start"
  case. **A query can only be answered once** — the doc comment on
  `Bot.Answer` is explicit that a second attempt errors.
- **Result type**: `*telebot.ArticleResult` (`inline_types.go:74-98`, embeds
  `ResultBase`) is the right shape for "text suggestion, no media" — fields
  `Title`, `Description`, `ThumbURL`. Content should be set via
  `ResultBase.Content = &telebot.InputTextMessageContent{Text: "France"}`
  (`input_types.go:9-25`), **not** `ArticleResult.Text` (`json:"message_text"`,
  `inline_types.go:82`) — see risk below, that field is a shortcut telebot
  carries from a pre-2016 Bot API shape that no longer exists for articles.
  `Results.MarshalJSON` (`inline.go:136-147`) auto-assigns an FNV-1 hash ID
  when `ResultBase.ID` is left blank, and auto-sets `Type` via `inferIQR`
  (`"article"` for `*ArticleResult`) — no need to set either manually.
- **50-result cap**: not present in the vendored source (it's a Telegram
  server-side limit, not a client-library one) — confirmed via Bot API docs
  and third-party API mirrors: `answerInlineQuery` accepts **at most 50**
  `InlineQueryResult` entries per call.
- **`cache_time`** (`QueryResponse.CacheTime`): "maximum time in seconds
  results may be cached server-side" (doc comment, `inline.go:42-44`),
  **defaults to 300s** when 0/omitted per Bot API docs — for typo-tolerant,
  per-keystroke matching this must be set low (e.g. `0`) or Telegram will
  serve a stale cached list for repeated prefixes instead of re-querying.
- **`is_personal`** (`QueryResponse.IsPersonal`): "pass true if results may
  be cached server-side only for the sender; by default cached results may
  be returned to *any* user who sends the same query text" (`inline.go:46-49`).
  Matters here because per-user grading context (which exercise is open) is
  *not* encoded in the result at all (see §2) — but `is_personal: true` is
  still the right call so one user's cached "France…" list can't leak to a
  different user who happens to type the same prefix while their own
  `cache_time` window is live.
- **Pagination**: `QueryResponse.NextOffset` (`inline.go:51-55`) — echoed back
  to the bot as `Query.Offset` on the client's next request for the same
  text once the user scrolls past the current page. Given the answer lists
  here are small (country/city names), realistically this can stay `""`
  (no pagination) for the whole first cut.

## 2. How the chosen answer gets back to the bot — the load-bearing question

**Verdict: use the free-text path, not `ChosenInlineResult`.** Two independent
signals converge on this:

- **telebot's dispatch never special-cases `via_bot` messages.** In
  `update.go`'s `ProcessContext`, an incoming `Update.Message` with non-empty
  `Text` runs through the *exact* same branch as any typed message —
  command-regex check, then `b.handle(m.Text, c)` ("1:1 satisfaction"), then
  `b.handle(OnText, c)` (`update.go:58-88`). There is no `if m.Via != nil`
  branch anywhere in `update.go`, `bot.go`, or `context.go` (grepped — zero
  hits). The `Message` struct does carry `Via *User` (`json:"via_bot"`,
  `message.go:80`, doc: "Bot through which the message was sent" per the
  official Message object) — so the field is *readable* if a handler wants
  to detect "this text arrived via inline mode" for logging/UX, but nothing
  filters or reroutes on it. A tapped inline result that lands in the private
  chat with the bot itself therefore surfaces as an ordinary `OnText` update,
  with `c.Text()` / the repo's `Session.MessageText()` returning whatever
  string was in the result's `InputTextMessageContent.Text`.
- **`ChosenInlineResult` (`telebot.OnInlineResult`, `"\ainline_result"`,
  `telebot.go:103`) is opt-in and lossy.** Bot API docs
  (`core.telegram.org/bots/inline#collecting-feedback`) state plainly:
  *"To know which of the provided results your users are sending to their
  chat partners, send @Botfather the `/setinlinefeedback` command."* — i.e.
  **`chosen_inline_result` updates are not delivered at all until that
  BotFather toggle is set**, and even after enabling it, Telegram
  recommends sampling ("1/10, 1/100, 1/1000 of the results") for popular
  bots because of caching load — so it is not guaranteed to fire for every
  selection even when turned on. Building grading around it would mean a
  silent no-op path in production until a manual BotFather step is done,
  and possibly *sampled* delivery afterward.

**Recommended linking mechanism**: don't encode anything in the inline
result at all. Grade the arriving message's plain text
(`Session.MessageText()`) against whatever exercise is currently open for
that user (the same state `handleText`/free-text grading already looks up
today, per `bot.go`'s doc comment: "the free-text `OnText` handler is only
registered when [Trainer] is non-nil"). This reuses the exact code path that
already handles a user typing an answer by hand — inline mode just becomes
another way to produce the same kind of message. Confirms the user's own
framing in the task: **id-encoding is a dead end anyway**, since
`ResultBase.ID` (`inline_types.go:7`) only round-trips inside
`ChosenInlineResult.ResultID` (`json:"result_id"`, `inline_types.go:112`) —
the message that actually lands in the chat carries only the
`input_message_content` text, never the result ID. So even if
`OnInlineResult` were reliable, the ID would arrive on a *different*,
unreliable update, decoupled from the message the user sees and the bot
must grade.

**Verify live**: I could not find an authoritative Telegram doc page stating
in so many words "a bot receives a normal message update for inline results
picked inside its own private chat with the picker" (fetches of
`core.telegram.org/bots/inline` and `.../bots/api#inline-mode` truncated
before reaching that exact passage). The `via_bot` field's existence and
telebot's unconditional `OnText` dispatch both strongly imply it (this is
also the standard mechanism countless "inline search" bots — GIF pickers,
@like, @vote, translate bots — rely on), but it should be smoke-tested
against the real bot before the design is finalized: send `/train`, tap the
new button, pick a suggestion, confirm `handleText`/`OnText` actually fires
with the expected string and `via_bot` populated.

## 3. `switch_inline_query_current_chat` button

- **Field**: `telebot.Btn.InlineQueryChat` (`json:"switch_inline_query_current_chat,omitempty"`,
  `markup.go:91`) — construct with
  `tele.Btn{Text: "🔎 Type an answer", InlineQueryChat: ""}` (empty string is
  valid: per `SwitchInlineQuery.Query`'s doc comment, "if left empty, only
  the bot's username will be inserted" — i.e. tapping it prefills exactly
  `@geodriller_bot ` with the cursor ready, no extra text). A non-empty
  string (e.g. a topic hint) gets appended after the mention. There's also
  a convenience constructor `tele.Btn` isn't built via one here, but
  `markup.go:194` shows telebot's own helper does
  `Btn{Text: text, InlineQueryChat: query}` — same shape, just a wrapper.
  The wire type (`telebot.InlineButton.InlineQueryChat`,
  `markup.go:314`) is the one actually marshalled into the outgoing
  `reply_markup`; the repo's existing `buildMarkup` (`bot.go:599-611`) only
  ever copies `Text`/`Data` today, so wiring this button means extending
  `buildMarkup` (or `Btn`/`buildMarkup`'s data model in `session.go`) to
  carry an inline-query field through.
- **UX quirks worth noting** (general Telegram-client knowledge, not
  verifiable from source): the button only ever affects the **current**
  chat's input field — it does not open a chat picker (that's the newer,
  separate `switch_inline_query_chosen_chat` / `SwitchInlineQuery` struct,
  `inline_types.go:87-105`, Bot API 6.7+, not needed here). On some older
  Telegram Desktop builds there was a known lag between the input field
  being prefilled and the first empty-query inline results appearing —
  practically this just means handling `Query.Text == ""` gracefully
  (return a short "type to search" or a default/trending list rather than
  an error). This should be smoke-tested on both a phone client and desktop
  before shipping, since inline-mode client behavior has historically had
  more platform variance than regular message handling.

## 4. Manual BotFather dependencies (cannot be done via the Bot API)

- **`/setinline`** — mandatory. Inline mode is off by default for every bot;
  there is no `Bot.SetX` API call that turns it on. Someone with control of
  `@BotFather` must send it `/setinline` once for `@geodriller_bot` and
  supply the placeholder text shown before the user types anything.
- **`/setinlinefeedback`** — only needed if `ChosenInlineResult` is ever
  wanted for analytics/telemetry later. **Not required** for the recommended
  design (§2), since grading rides the ordinary message/`OnText` path.
  Flagging it here only so a future "which suggestions do people actually
  pick" instrumentation task doesn't get blocked rediscovering this.
- Both are one-time BotFather chat commands, not `Bot.Raw(...)` calls —
  confirmed via `core.telegram.org/bots/inline`, which frames both as "send
  BotFather the command," with no corresponding Bot API method in
  `bot.go`/`bot_raw.go` for either (grepped, no `setinline`/
  `setinlinefeedback` string anywhere in the vendored client).

## 5. Latency, rate limits, matcher reuse

- **Response window**: not a fixed published number the way
  `answerPreCheckoutQuery`'s hard 10s is documented — Telegram's own error
  surface ("Bad Request: query is too old and response timeout expired or
  query ID is invalid") confirms queries *do* expire if answered too slowly,
  but no exact seconds figure is in the official reference. Practical
  guidance from the wider ecosystem: treat it like any other Telegram
  "answer quickly" query (sub-second to a couple of seconds is safe; there
  is no server-side grace period to lean on). This makes the matcher
  latency budget tight enough that it should run in-process against an
  already-loaded index, not hit a DB per keystroke.
- **Rate limits**: same global Bot API limits apply as any other method (no
  inline-specific carve-out documented) — the practical concern is Telegram
  clients firing a new `answerInlineQuery`-triggering request on every
  keystroke, so debouncing happens client-side (nothing to build) but the
  bot must still be fast per-call since there's no batching.
- **Matcher reuse**: the task's framing that engram's typo-tolerant
  `TextMatcher` (casefold + Levenshtein ≤2 + aliases) is reusable here lines
  up with what inline search needs — a prefix/fuzzy scorer over a small,
  static-ish candidate set (country/city names for the open exercise's
  topic), called synchronously inside the `OnQuery` handler, with results
  capped to the top ~10-20 (well under the 50 hard cap — more than ~10 is
  noise for a human picking from a dropdown anyway) and `CacheTime: 0` /
  `IsPersonal: true` so per-user, per-keystroke freshness isn't fighting
  Telegram's server cache. Not verified against the actual `TextMatcher`
  API/signature — out of scope per this spike's repo-read restriction
  (only `go.mod` and `internal/telegram/bot.go` were read; engram's source
  wasn't opened).

## Design sketch (handler flow, for the follow-up implementation task)

1. **Button**: exercise-card `buildMarkup`/`Btn` gains an `InlineQueryChat`
   field (currently only `Label`/`Data` exist, `bot.go:599-611` and whatever
   `Btn` looks like in `session.go`); render one extra button, e.g.
   "🔎 Type an answer", alongside the existing choice buttons.
2. **`OnQuery` handler**: new `tb.Handle(telebot.OnQuery, ...)` registration
   in `New()` (only when `Trainer != nil`, mirroring the existing `OnText`
   gate at `bot.go:193-195`). Handler reads `c.Query().Text` and
   `c.Query().Sender.ID`, looks up that user's currently-open exercise (same
   state `handleText` already consults) to get the candidate answer set for
   that exercise's topic, runs `TextMatcher` fuzzy-prefix scoring, builds up
   to ~10-20 `*telebot.ArticleResult`s with
   `Content: &telebot.InputTextMessageContent{Text: candidate}`, and calls
   `c.Answer(&telebot.QueryResponse{Results: results, CacheTime: 0,
   IsPersonal: true})`.
3. **Answer arrival**: nothing new — the existing `OnText`/`handleText` path
   (`bot.go`, gated on `cfg.Trainer != nil`) already grades free-typed text
   against the open exercise; a message that arrived via a tapped inline
   result is indistinguishable from one the user typed by hand, so it flows
   through unchanged. Optionally read `s.ctx.Message().Via` for
   logging/metrics ("answered via inline suggestion") without changing
   grading behavior.

## Open risks

- **`ArticleResult.Text` shortcut may be dead on modern Telegram servers.**
  telebot's doc comment ("Shortcut... to specifying InputMessageContent")
  matches a pre-Bot-API-2.0 (pre-2016) schema where `InlineQueryResultArticle`
  had a top-level `message_text` field; the current official schema requires
  `input_message_content` and has no such field. Always set
  `ResultBase.Content`, never rely on `ArticleResult.Text` alone — verify
  live if this is ever tempting as a shortcut.
- **Answer-arrival mechanism is inferred, not confirmed against the live
  bot** (see §2's "verify live" callout) — highest-priority thing to
  smoke-test before committing to the free-text design, though the fallback
  if it *didn't* work (which would be surprising) is `OnInlineResult` +
  `/setinlinefeedback`, with the sampling caveat above.
- **`cache_time`/`is_personal` interaction with per-exercise state**: because
  the candidate list depends on which exercise is currently open (not just
  the query text), two different users typing the same prefix on two
  different open exercises must never share a cached result list —
  `IsPersonal: true` is necessary, not just nice-to-have, and `CacheTime`
  should probably still be small (a few seconds, not 0) purely for
  duplicate-keystroke efficiency, re-tested once live.
- **No local reference for `Session`/`Btn` types** (`internal/telegram/session.go`
  wasn't in the allowed read list) — the design sketch above assumes
  `Btn` needs a new field; the exact edit site should be confirmed against
  that file when implementation starts.
