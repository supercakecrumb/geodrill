package telegram

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"
	tele "gopkg.in/telebot.v4"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/suggest"
)

// ── buildMarkup ──────────────────────────────────────────────────────────

func TestBuildMarkup_CallbackButton(t *testing.T) {
	markup := buildMarkup([][]Btn{{{Label: "Spain", Data: "ans:x:0"}}})
	if len(markup.InlineKeyboard) != 1 || len(markup.InlineKeyboard[0]) != 1 {
		t.Fatalf("expected one row of one button, got %+v", markup.InlineKeyboard)
	}
	btn := markup.InlineKeyboard[0][0]
	if btn.Data != "ans:x:0" {
		t.Fatalf("expected Data threaded through, got %q", btn.Data)
	}
	if btn.Text != "Spain" {
		t.Fatalf("expected the label threaded through, got %q", btn.Text)
	}
}

func TestBuildMarkup_InlineQueryChatButton(t *testing.T) {
	markup := buildMarkup([][]Btn{{{Label: autocompleteButtonLabel, InlineQueryChat: true}}})
	btn := markup.InlineKeyboard[0][0]
	if btn.Text != autocompleteButtonLabel {
		t.Fatalf("expected the label threaded through, got %q", btn.Text)
	}
	// Data must stay unset: a button can't be both a callback and a
	// switch_inline_query_current_chat button (buildMarkup's doc). This is
	// the assertion that actually distinguishes an inline-query button from
	// a plain one — InlineButton.InlineQueryChat itself always marshals as
	// "" regardless (telebot's own MarshalJSON quirk, verified against the
	// vendored v4.0.0-beta.10 source), so it can't be asserted on here.
	if btn.Data != "" {
		t.Fatalf("expected no callback data on an inline-query button, got %q", btn.Data)
	}
}

func TestBuildMarkup_MixedRowKeepsButtonsIndependent(t *testing.T) {
	markup := buildMarkup([][]Btn{
		{{Label: "Spain", Data: "ans:x:0"}},
		{{Label: autocompleteButtonLabel, InlineQueryChat: true}},
	})
	if markup.InlineKeyboard[0][0].Data != "ans:x:0" {
		t.Fatalf("expected the callback row's Data untouched, got %+v", markup.InlineKeyboard[0])
	}
	if markup.InlineKeyboard[1][0].Data != "" {
		t.Fatalf("expected the inline-query row to carry no Data, got %+v", markup.InlineKeyboard[1])
	}
}

// ── buildQueryResults ────────────────────────────────────────────────────

// fakeSuggester implements Suggester in memory, recording the last call so
// tests can assert on it without a real suggest.Index.
type fakeSuggester struct {
	result []suggest.Suggestion
	// domainForAnswer is what DomainForAnswer returns.
	domainForAnswer suggest.Domain

	query  string
	domain suggest.Domain
	ggOnly bool
	limit  int

	domainForAnswerCalledWith string
}

func (f *fakeSuggester) MatchDomain(query string, domain suggest.Domain, ggOnly bool, limit int) []suggest.Suggestion {
	f.query = query
	f.domain = domain
	f.ggOnly = ggOnly
	f.limit = limit
	return f.result
}

func (f *fakeSuggester) DomainForAnswer(correct string) suggest.Domain {
	f.domainForAnswerCalledWith = correct
	return f.domainForAnswer
}

// fakeExerciseStore implements openExerciseStore in memory.
type fakeExerciseStore struct {
	exercise storage.Exercise
	found    bool
	err      error
}

func (f *fakeExerciseStore) GetOpenExerciseByMode(ctx context.Context, userID uuid.UUID, mode int16) (storage.Exercise, bool, error) {
	return f.exercise, f.found, f.err
}

func TestBuildQueryResults_RendersBareLabelNoEmoji(t *testing.T) {
	sg := &fakeSuggester{result: []suggest.Suggestion{
		{Label: "France", Emoji: "🇫🇷", Key: "FR"},
		{Label: "Chad", Key: "TD"}, // no emoji
	}}

	got := buildQueryResults(sg, "fra", suggest.DomainCountry, false)
	if sg.query != "fra" || sg.limit != maxQueryResults {
		t.Fatalf("expected MatchDomain called with (query, _, maxQueryResults), got (%q, _, %d)", sg.query, sg.limit)
	}
	if sg.domain != suggest.DomainCountry {
		t.Fatalf("expected MatchDomain called with DomainCountry, got %v", sg.domain)
	}
	// Title carries no flag-emoji prefix even though the matched
	// suggestion has one — the flag was dropped as noise (kanban card
	// "Autocomplete must be scoped to the question's answer domain").
	want := []QueryResult{
		{Title: "France", Text: "France"},
		{Title: "Chad", Text: "Chad"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildQueryResults = %+v, want %+v", got, want)
	}
}

func TestBuildQueryResults_NilSuggesterYieldsNoResults(t *testing.T) {
	if got := buildQueryResults(nil, "anything", suggest.DomainCountry, false); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

// ── handleQuery ──────────────────────────────────────────────────────────

// fakeQueryContext implements queryContext in memory, recording the
// Answer() call so tests can assert on it without a bot token or the
// network.
type fakeQueryContext struct {
	query *tele.Query

	answered  *tele.QueryResponse
	answerErr error
}

func (f *fakeQueryContext) Query() *tele.Query { return f.query }

func (f *fakeQueryContext) Answer(resp *tele.QueryResponse) error {
	f.answered = resp
	return f.answerErr
}

func TestHandleQuery_AnswersWithSuggestMatches(t *testing.T) {
	b := newTestBot(&stubStore{})
	b.suggest = &fakeSuggester{result: []suggest.Suggestion{{Label: "France", Emoji: "🇫🇷"}}}

	c := &fakeQueryContext{query: &tele.Query{Text: "fra"}}
	if err := b.handleQuery(c); err != nil {
		t.Fatalf("handleQuery: %v", err)
	}
	if c.answered == nil {
		t.Fatalf("expected Answer to be called")
	}
	if !c.answered.IsPersonal {
		t.Fatalf("expected IsPersonal=true (spike §2: per-exercise state must never leak across users)")
	}
	if c.answered.CacheTime != queryCacheTimeSeconds {
		t.Fatalf("expected CacheTime=%d, got %d", queryCacheTimeSeconds, c.answered.CacheTime)
	}
	if len(c.answered.Results) != 1 {
		t.Fatalf("expected one result, got %d", len(c.answered.Results))
	}
	article, ok := c.answered.Results[0].(*tele.ArticleResult)
	if !ok {
		t.Fatalf("expected an *ArticleResult, got %T", c.answered.Results[0])
	}
	if article.Title != "France" {
		t.Fatalf("expected the emoji-free title, got %q", article.Title)
	}
	if article.Text != "" {
		t.Fatalf("expected the legacy ArticleResult.Text shortcut left unset, got %q", article.Text)
	}
	content, ok := article.Content.(*tele.InputTextMessageContent)
	if !ok {
		t.Fatalf("expected Content to be *InputTextMessageContent, got %T", article.Content)
	}
	if content.Text != "France" {
		t.Fatalf("expected the bare label as the sent text, got %q", content.Text)
	}
}

func TestHandleQuery_NoMatchesStillAnswers(t *testing.T) {
	b := newTestBot(&stubStore{})
	b.suggest = &fakeSuggester{result: nil}

	c := &fakeQueryContext{query: &tele.Query{Text: "zzzzzzzz"}}
	if err := b.handleQuery(c); err != nil {
		t.Fatalf("handleQuery: %v", err)
	}
	if c.answered == nil || len(c.answered.Results) != 0 {
		t.Fatalf("expected an empty-but-answered response, got %+v", c.answered)
	}
}

func TestHandleQuery_AnswerErrorIsLoggedNotReturned(t *testing.T) {
	b := newTestBot(&stubStore{})
	b.suggest = &fakeSuggester{}

	c := &fakeQueryContext{query: &tele.Query{Text: ""}, answerErr: errEditMessage}
	if err := b.handleQuery(c); err != nil {
		t.Fatalf("handleQuery must never return an Answer error, got %v", err)
	}
}

// ── domain scoping (kanban "Autocomplete must be scoped to the question's
// answer domain") ────────────────────────────────────────────────────────

func TestHandleQuery_ScopesToCapitalDomainAndDropsEmoji(t *testing.T) {
	b := newTestBot(&stubStore{user: storage.User{ID: uuid.New()}})
	sg := &fakeSuggester{
		result:          []suggest.Suggestion{{Label: "Canberra", Emoji: "🇦🇺", Domain: suggest.DomainCapital}},
		domainForAnswer: suggest.DomainCapital,
	}
	b.suggest = sg
	b.exerciseStore = &fakeExerciseStore{found: true, exercise: storage.Exercise{CorrectAnswer: "Canberra"}}

	c := &fakeQueryContext{query: &tele.Query{Text: "can", Sender: &tele.User{ID: 42}}}
	if err := b.handleQuery(c); err != nil {
		t.Fatalf("handleQuery: %v", err)
	}

	if sg.domainForAnswerCalledWith != "Canberra" {
		t.Fatalf("expected DomainForAnswer called with the open exercise's CorrectAnswer, got %q", sg.domainForAnswerCalledWith)
	}
	if sg.domain != suggest.DomainCapital {
		t.Fatalf("expected MatchDomain called with DomainCapital, got %v", sg.domain)
	}
	if c.answered == nil || len(c.answered.Results) != 1 {
		t.Fatalf("expected one result, got %+v", c.answered)
	}
	article, ok := c.answered.Results[0].(*tele.ArticleResult)
	if !ok {
		t.Fatalf("expected an *ArticleResult, got %T", c.answered.Results[0])
	}
	if article.Title != "Canberra" {
		t.Fatalf("expected an emoji-free capital title, got %q", article.Title)
	}
}

func TestQueryDomain_DefaultsToCountryWhenNoOpenExercise(t *testing.T) {
	b := newTestBot(&stubStore{user: storage.User{ID: uuid.New()}})
	// Would return DomainCapital if it were ever consulted — it must not
	// be, since there's no open exercise to resolve a domain from.
	b.suggest = &fakeSuggester{domainForAnswer: suggest.DomainCapital}
	b.exerciseStore = &fakeExerciseStore{found: false}

	got, _ := b.queryScope(context.Background(), &tele.Query{Text: "x", Sender: &tele.User{ID: 42}})
	if got != suggest.DomainCountry {
		t.Fatalf("queryScope with no open exercise = %v, want DomainCountry", got)
	}
}

func TestQueryDomain_DefaultsToCountryWhenNoSender(t *testing.T) {
	b := newTestBot(&stubStore{user: storage.User{ID: uuid.New()}})
	b.suggest = &fakeSuggester{domainForAnswer: suggest.DomainCapital}
	b.exerciseStore = &fakeExerciseStore{found: true, exercise: storage.Exercise{CorrectAnswer: "Canberra"}}

	got, _ := b.queryScope(context.Background(), &tele.Query{Text: "x"}) // no Sender
	if got != suggest.DomainCountry {
		t.Fatalf("queryScope with no Sender = %v, want DomainCountry", got)
	}
}

func TestQueryDomain_DefaultsToCountryWhenSuggesterNil(t *testing.T) {
	b := newTestBot(&stubStore{user: storage.User{ID: uuid.New()}})
	b.exerciseStore = &fakeExerciseStore{found: true, exercise: storage.Exercise{CorrectAnswer: "Canberra"}}

	got, _ := b.queryScope(context.Background(), &tele.Query{Text: "x", Sender: &tele.User{ID: 42}})
	if got != suggest.DomainCountry {
		t.Fatalf("queryScope with nil Suggester = %v, want DomainCountry", got)
	}
}
