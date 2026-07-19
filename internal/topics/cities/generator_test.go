package cities

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

// mkItem builds a storage.Item carrying a well-formed cities payload, keyed
// the way Seed keys items (the yaml's "<iso2>:<slug>" city key, NOT the
// country iso2 — a country hosts several cities).
func mkItem(key, city, flag, countryName, iso2, iso3 string) storage.Item {
	raw, err := json.Marshal(itemPayload{
		CityName:    city,
		Flag:        flag,
		CountryName: countryName,
		ISOA2:       iso2,
		ISOA3:       iso3,
	})
	if err != nil {
		panic(err)
	}
	return storage.Item{Key: key, Label: city, Payload: raw}
}

func TestKind(t *testing.T) {
	if got := New().Kind(); got != Kind {
		t.Fatalf("Kind() = %q, want %q", got, Kind)
	}
}

// TestTopicMode pins the topic's exercise-mode config — the end-to-end
// autocomplete wiring depends on the leaf declaring "autocomplete"
// (internal/study.buildExerciseForItem renders the country-suggestion button
// on any turn whose configured mode string is literally "autocomplete").
func TestTopicMode(t *testing.T) {
	ct := cityTopic()
	leaf := ct[len(ct)-1]
	if leaf.Slug != LeafSlug {
		t.Fatalf("leaf slug = %q, want %q", leaf.Slug, LeafSlug)
	}
	if len(leaf.ExerciseModes) != 1 || leaf.ExerciseModes[0] != "autocomplete" {
		t.Fatalf("exercise_modes = %v, want [autocomplete]", leaf.ExerciseModes)
	}
	if !leaf.IsQuizzable {
		t.Fatalf("leaf should be quizzable")
	}
	root := ct[0]
	if root.Slug != RootSlug || root.QuizKind != "" {
		t.Fatalf("root = %+v, want a plain container named %q", root, RootSlug)
	}
}

// TestCityToCountry_Text is the autocomplete answer path: a ModeText
// exercise whose prompt names the city, accepts the country's name + iso
// codes + aliases, and whose canonical CorrectAnswer is the country name.
func TestCityToCountry_Text(t *testing.T) {
	gen := New()
	item := mkItem("fr:paris", "Paris", "🇫🇷", "France", "FR", "FRA")

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: item, Mode: quiz.ModeText})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.Mode != quiz.ModeText {
		t.Fatalf("Mode = %v, want ModeText", ex.Mode)
	}
	if want := "🏙 Which country is Paris in?"; ex.Prompt != want {
		t.Fatalf("Prompt = %q, want %q", ex.Prompt, want)
	}
	if ex.CorrectAnswer != "France" {
		t.Fatalf("CorrectAnswer = %q, want %q", ex.CorrectAnswer, "France")
	}
	assertAccepts(t, ex.Accept, "France", "FR", "FRA")

	// A picked autocomplete suggestion arrives as the plain country name and
	// must grade correct through the same free-text matcher the trainer uses.
	if ok, _ := (quiz.TextMatcher{Accept: ex.Accept, MaxEdits: 2}).Match("france"); !ok {
		t.Fatalf("typed %q should match accepted spellings %v", "france", ex.Accept)
	}
}

// TestAliases checks the curated alias table reaches Accept for the
// ambiguous big countries autocomplete users type by nickname.
func TestAliases(t *testing.T) {
	gen := New()
	us := mkItem("us:new-york-city", "New York City", "🇺🇸", "United States", "US", "USA")
	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: us, Mode: quiz.ModeText})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	assertAccepts(t, ex.Accept, "United States", "USA", "America")

	gb := mkItem("gb:london", "London", "🇬🇧", "United Kingdom", "GB", "GBR")
	ex, err = gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: gb, Mode: quiz.ModeText})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	assertAccepts(t, ex.Accept, "United Kingdom", "UK", "Britain")
	if want := "🏙 Which country is London in?"; ex.Prompt != want {
		t.Fatalf("GB prompt = %q, want %q", ex.Prompt, want)
	}
}

func TestBuildIntro(t *testing.T) {
	gen := New()
	item := mkItem("fr:paris", "Paris", "🇫🇷", "France", "FR", "FRA")
	card, err := gen.BuildIntro(context.Background(), item)
	if err != nil {
		t.Fatalf("BuildIntro: %v", err)
	}
	if want := "🏙 Paris is a city in 🇫🇷 France."; card.Text != want {
		t.Fatalf("intro = %q, want %q", card.Text, want)
	}
}

func TestMalformedPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{"empty", nil},
		{"invalid json", []byte(`{"city_name":`)},
		{"missing city_name", []byte(`{"iso_a2":"FR","country_name":"France"}`)},
		{"missing iso2", []byte(`{"city_name":"Paris","country_name":"France"}`)},
		{"missing country_name", []byte(`{"city_name":"Paris","iso_a2":"FR"}`)},
	}
	gen := New()
	for _, tc := range cases {
		item := storage.Item{Key: "bad", Payload: tc.payload}
		if _, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
			topics.ExerciseRequest{Item: item, Mode: quiz.ModeText}); !errors.Is(err, ErrMalformedPayload) {
			t.Fatalf("%s: BuildExercise err = %v, want ErrMalformedPayload", tc.name, err)
		}
		if _, err := gen.BuildIntro(context.Background(), item); !errors.Is(err, ErrMalformedPayload) {
			t.Fatalf("%s: BuildIntro err = %v, want ErrMalformedPayload", tc.name, err)
		}
	}
}

// TestSingleFallbackDeterministic spot-checks that the
// (production-unused but descriptor-valid) single-choice fallback is
// deterministic given rng and includes the correct answer — cheap insurance
// the descriptor is well-formed (mirrors tld's equivalent test).
func TestSingleFallbackDeterministic(t *testing.T) {
	gen := New()
	item := mkItem("fr:paris", "Paris", "🇫🇷", "France", "FR", "FRA")
	siblings := []storage.Item{
		mkItem("de:berlin", "Berlin", "🇩🇪", "Germany", "DE", "DEU"),
		mkItem("it:rome", "Rome", "🇮🇹", "Italy", "IT", "ITA"),
		mkItem("es:madrid", "Madrid", "🇪🇸", "Spain", "ES", "ESP"),
		mkItem("jp:tokyo", "Tokyo", "🇯🇵", "Japan", "JP", "JPN"),
	}
	var first []topics.Option
	for i := 0; i < 3; i++ {
		ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(7)),
			topics.ExerciseRequest{Item: item, Siblings: siblings, Mode: quiz.ModeSingle})
		if err != nil {
			t.Fatalf("BuildExercise: %v", err)
		}
		if ex.CorrectAnswer != "FR" {
			t.Fatalf("single CorrectAnswer = %q, want FR", ex.CorrectAnswer)
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
	for _, o := range first {
		if o.Key == "FR" && o.Label != "France" {
			t.Fatalf("correct option label = %q, want %q", o.Label, "France")
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
