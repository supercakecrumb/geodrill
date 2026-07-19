package capitals

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"testing"

	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

// mkItem builds a storage.Item carrying a well-formed capitals payload,
// keyed the way Seed keys items (iso_a2).
func mkItem(iso, name, flag, iso3, capital, note string) storage.Item {
	raw, err := json.Marshal(itemPayload{
		Flag:    flag,
		Name:    name,
		ISOA2:   iso,
		ISOA3:   iso3,
		Capital: capital,
		Note:    note,
	})
	if err != nil {
		panic(err)
	}
	return storage.Item{Key: iso, Label: name, Payload: raw}
}

func TestKinds(t *testing.T) {
	if got := NewCountryToCapital().Kind(); got != KindCountryToCapital {
		t.Fatalf("country->capital Kind() = %q, want %q", got, KindCountryToCapital)
	}
	if got := NewCapitalToCountry().Kind(); got != KindCapitalToCountry {
		t.Fatalf("capital->country Kind() = %q, want %q", got, KindCapitalToCountry)
	}
}

// TestTopicModes pins the two direction-topics' exercise-mode configs — both
// directions declare "autocomplete" (internal/study.buildExerciseForItem
// renders the suggestion button on any turn whose configured mode string is
// literally "autocomplete").
func TestTopicModes(t *testing.T) {
	c2c := countryToCapitalTopic()
	leaf := c2c[len(c2c)-1]
	if leaf.Slug != CountryToCapitalSlug {
		t.Fatalf("country->capital leaf slug = %q, want %q", leaf.Slug, CountryToCapitalSlug)
	}
	if len(leaf.ExerciseModes) != 1 || leaf.ExerciseModes[0] != "autocomplete" {
		t.Fatalf("country->capital exercise_modes = %v, want [autocomplete]", leaf.ExerciseModes)
	}

	cap2c := capitalToCountryTopic()
	leaf = cap2c[len(cap2c)-1]
	if leaf.Slug != CapitalToCountrySlug {
		t.Fatalf("capital->country leaf slug = %q, want %q", leaf.Slug, CapitalToCountrySlug)
	}
	if len(leaf.ExerciseModes) != 1 || leaf.ExerciseModes[0] != "autocomplete" {
		t.Fatalf("capital->country exercise_modes = %v, want [autocomplete]", leaf.ExerciseModes)
	}
}

// TestCountryToCapital_Text is the country-side autocomplete answer path: a
// ModeText exercise whose prompt names the country, accepts the country's
// primary capital + iso codes... accepts the capital's name, and whose
// canonical CorrectAnswer is the primary capital.
func TestCountryToCapital_Text(t *testing.T) {
	gen := NewCountryToCapital()
	item := mkItem("CO", "Colombia", "🇨🇴", "COL", "Bogotá", "")

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: item, Mode: quiz.ModeText})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.Mode != quiz.ModeText {
		t.Fatalf("Mode = %v, want ModeText", ex.Mode)
	}
	if want := "What's the capital of 🇨🇴 Colombia?"; ex.Prompt != want {
		t.Fatalf("Prompt = %q, want %q", ex.Prompt, want)
	}
	if ex.CorrectAnswer != "Bogotá" {
		t.Fatalf("CorrectAnswer = %q, want %q", ex.CorrectAnswer, "Bogotá")
	}
	assertAccepts(t, ex.Accept, "Bogotá")

	// A picked autocomplete suggestion arrives as the plain capital name and
	// must grade correct through the same free-text matcher the trainer uses.
	if ok, _ := (quiz.TextMatcher{Accept: ex.Accept, MaxEdits: 2}).Match("bogota"); !ok {
		t.Fatalf("typed %q should match accepted spellings %v", "bogota", ex.Accept)
	}
}

// TestCountryToCapital_MultiCapital checks the generous-Accept /
// strict-CorrectAnswer rule for multi-capital countries: every listed
// capital grades correct, but the displayed answer is always the primary.
func TestCountryToCapital_MultiCapital(t *testing.T) {
	gen := NewCountryToCapital()
	za := mkItem("ZA", "South Africa", "🇿🇦", "ZAF", "Pretoria",
		"South Africa has three capitals: Pretoria (executive), Cape Town (legislative), Bloemfontein (judicial)")

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: za, Mode: quiz.ModeText})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.CorrectAnswer != "Pretoria" {
		t.Fatalf("ZA CorrectAnswer = %q, want Pretoria (the primary/executive capital)", ex.CorrectAnswer)
	}
	assertAccepts(t, ex.Accept, "Pretoria", "Cape Town", "Bloemfontein")

	bo := mkItem("BO", "Bolivia", "🇧🇴", "BOL", "Sucre",
		"Sucre is constitutional capital, La Paz is administrative seat")
	ex, err = gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: bo, Mode: quiz.ModeText})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.CorrectAnswer != "Sucre" {
		t.Fatalf("BO CorrectAnswer = %q, want Sucre", ex.CorrectAnswer)
	}
	assertAccepts(t, ex.Accept, "Sucre", "La Paz")
}

// TestCountryToCapital_Aliases checks the curated alias table reaches Accept
// for capitals users are likely to type differently than the canonical name.
func TestCountryToCapital_Aliases(t *testing.T) {
	gen := NewCountryToCapital()
	us := mkItem("US", "United States", "🇺🇸", "USA", "Washington, D.C.", "")
	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: us, Mode: quiz.ModeText})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	assertAccepts(t, ex.Accept, "Washington, D.C.", "Washington", "Washington DC")
}

// TestCapitalToCountry_Text is the capital-side autocomplete answer path: a
// ModeText exercise whose prompt names the capital, accepts the country's
// name + iso codes + aliases, and whose canonical CorrectAnswer is the
// country name.
func TestCapitalToCountry_Text(t *testing.T) {
	gen := NewCapitalToCountry()
	item := mkItem("CO", "Colombia", "🇨🇴", "COL", "Bogotá", "")

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: item, Mode: quiz.ModeText})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.Mode != quiz.ModeText {
		t.Fatalf("Mode = %v, want ModeText", ex.Mode)
	}
	if want := "🏛 Bogotá is the capital of which country?"; ex.Prompt != want {
		t.Fatalf("Prompt = %q, want %q", ex.Prompt, want)
	}
	if ex.CorrectAnswer != "Colombia" {
		t.Fatalf("CorrectAnswer = %q, want %q", ex.CorrectAnswer, "Colombia")
	}
	assertAccepts(t, ex.Accept, "Colombia", "CO", "COL")

	if ok, _ := (quiz.TextMatcher{Accept: ex.Accept, MaxEdits: 2}).Match("colombia"); !ok {
		t.Fatalf("typed %q should match accepted spellings %v", "colombia", ex.Accept)
	}
}

func TestBuildIntro(t *testing.T) {
	cases := []struct {
		name string
		item storage.Item
		want string
	}{
		{
			name: "plain",
			item: mkItem("CO", "Colombia", "🇨🇴", "COL", "Bogotá", ""),
			want: "🇨🇴 Colombia's capital is Bogotá.",
		},
		{
			name: "multi-capital note kept verbatim",
			item: mkItem("ZA", "South Africa", "🇿🇦", "ZAF", "Pretoria",
				"South Africa has three capitals: Pretoria (executive), Cape Town (legislative), Bloemfontein (judicial)"),
			want: "🇿🇦 South Africa's capital is Pretoria. (South Africa has three capitals: Pretoria (executive), Cape Town (legislative), Bloemfontein (judicial))",
		},
	}

	// Both directions render the same direction-agnostic intro.
	for _, gen := range []*genWrap{{"country->capital", NewCountryToCapital()}, {"capital->country", NewCapitalToCountry()}} {
		for _, tc := range cases {
			card, err := gen.g.BuildIntro(context.Background(), tc.item)
			if err != nil {
				t.Fatalf("%s/%s BuildIntro: %v", gen.name, tc.name, err)
			}
			if card.Text != tc.want {
				t.Fatalf("%s/%s intro = %q, want %q", gen.name, tc.name, card.Text, tc.want)
			}
		}
	}
}

type genWrap struct {
	name string
	g    interface {
		BuildIntro(context.Context, storage.Item) (topics.IntroCard, error)
	}
}

func TestMalformedPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{"empty", nil},
		{"invalid json", []byte(`{"capital":`)},
		{"missing name", []byte(`{"iso_a2":"CO","capital":"Bogotá"}`)},
		{"missing iso2", []byte(`{"name":"Colombia","capital":"Bogotá"}`)},
		{"missing capital", []byte(`{"name":"Colombia","iso_a2":"CO"}`)},
	}
	gens := []*engineGen{{"country->capital", NewCountryToCapital()}, {"capital->country", NewCapitalToCountry()}}
	for _, gw := range gens {
		for _, tc := range cases {
			item := storage.Item{Key: "bad", Payload: tc.payload}
			if _, err := gw.g.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
				topics.ExerciseRequest{Item: item, Mode: quiz.ModeText}); !errors.Is(err, ErrMalformedPayload) {
				t.Fatalf("%s/%s BuildExercise err = %v, want ErrMalformedPayload", gw.name, tc.name, err)
			}
			if _, err := gw.g.BuildIntro(context.Background(), item); !errors.Is(err, ErrMalformedPayload) {
				t.Fatalf("%s/%s BuildIntro err = %v, want ErrMalformedPayload", gw.name, tc.name, err)
			}
		}
	}
}

type engineGen struct {
	name string
	g    interface {
		BuildExercise(context.Context, *rand.Rand, topics.ExerciseRequest) (topics.Exercise, error)
		BuildIntro(context.Context, storage.Item) (topics.IntroCard, error)
	}
}

// TestSingleFallbackDeterministic spot-checks that the (production-unused
// but descriptor-valid) single-choice fallback is deterministic given rng
// and includes the correct answer — cheap insurance the descriptor is
// well-formed (mirrors tld's equivalent test).
func TestSingleFallbackDeterministic(t *testing.T) {
	gen := NewCountryToCapital()
	item := mkItem("CO", "Colombia", "🇨🇴", "COL", "Bogotá", "")
	siblings := []storage.Item{
		mkItem("PE", "Peru", "🇵🇪", "PER", "Lima", ""),
		mkItem("EC", "Ecuador", "🇪🇨", "ECU", "Quito", ""),
		mkItem("VE", "Venezuela", "🇻🇪", "VEN", "Caracas", ""),
		mkItem("BR", "Brazil", "🇧🇷", "BRA", "Brasília", ""),
	}
	var first []topics.Option
	for i := 0; i < 3; i++ {
		ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(7)),
			topics.ExerciseRequest{Item: item, Siblings: siblings, Mode: quiz.ModeSingle})
		if err != nil {
			t.Fatalf("BuildExercise: %v", err)
		}
		if ex.CorrectAnswer != "CO" {
			t.Fatalf("single CorrectAnswer = %q, want CO", ex.CorrectAnswer)
		}
		if i == 0 {
			first = ex.Options
			continue
		}
		if len(ex.Options) != len(first) {
			t.Fatalf("option count not deterministic: %d vs %d", len(ex.Options), len(first))
		}
		for j := range first {
			if ex.Options[j] != first[j] {
				t.Fatalf("option order not deterministic at %d across same rng seed", j)
			}
		}
	}
	// The correct option is labeled with the primary capital, not the
	// uppercased iso key.
	for _, o := range first {
		if o.Key == "CO" && o.Label != "Bogotá" {
			t.Fatalf("correct option label = %q, want %q", o.Label, "Bogotá")
		}
	}
}

func assertAccepts(t *testing.T, accept []string, want ...string) {
	t.Helper()
	have := make(map[string]bool, len(accept))
	for _, a := range accept {
		have[a] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Fatalf("Accept %v missing %q", accept, w)
		}
	}
}
