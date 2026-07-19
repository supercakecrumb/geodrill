package tld

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

// mkItem builds a storage.Item carrying a well-formed tld payload, keyed the
// way Seed keys items (iso_a2).
func mkItem(iso, name, flag, iso3, tld, note string) storage.Item {
	raw, err := json.Marshal(itemPayload{
		Flag:  flag,
		Name:  name,
		ISOA2: iso,
		ISOA3: iso3,
		TLD:   tld,
		Note:  note,
	})
	if err != nil {
		panic(err)
	}
	return storage.Item{Key: iso, Label: name, Payload: raw}
}

func TestKinds(t *testing.T) {
	if got := NewTLDToCountry().Kind(); got != KindTLDToCountry {
		t.Fatalf("tld->country Kind() = %q, want %q", got, KindTLDToCountry)
	}
	if got := NewCountryToTLD().Kind(); got != KindCountryToTLD {
		t.Fatalf("country->tld Kind() = %q, want %q", got, KindCountryToTLD)
	}
}

// TestTopicModes pins the two direction-topics' exercise-mode configs — the
// end-to-end autocomplete wiring depends on tld->country declaring
// "autocomplete" (internal/study.buildExerciseForItem renders the
// country-suggestion button on any turn whose configured mode string is
// literally "autocomplete") and country->tld declaring plain "text".
func TestTopicModes(t *testing.T) {
	tc := tldToCountryTopic()
	leaf := tc[len(tc)-1]
	if leaf.Slug != TLDToCountrySlug {
		t.Fatalf("tld->country leaf slug = %q, want %q", leaf.Slug, TLDToCountrySlug)
	}
	if len(leaf.ExerciseModes) != 1 || leaf.ExerciseModes[0] != "autocomplete" {
		t.Fatalf("tld->country exercise_modes = %v, want [autocomplete]", leaf.ExerciseModes)
	}

	ct := countryToTLDTopic()
	leaf = ct[len(ct)-1]
	if leaf.Slug != CountryToTLDSlug {
		t.Fatalf("country->tld leaf slug = %q, want %q", leaf.Slug, CountryToTLDSlug)
	}
	if len(leaf.ExerciseModes) != 1 || leaf.ExerciseModes[0] != "text" {
		t.Fatalf("country->tld exercise_modes = %v, want [text]", leaf.ExerciseModes)
	}
}

// TestTLDToCountry_Text is the country-side autocomplete answer path: a
// ModeText exercise whose prompt names the TLD, accepts the country's name +
// iso codes + aliases, and whose canonical CorrectAnswer is the country name.
func TestTLDToCountry_Text(t *testing.T) {
	gen := NewTLDToCountry()
	item := mkItem("DE", "Germany", "🇩🇪", "DEU", ".de", "")

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: item, Mode: quiz.ModeText})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.Mode != quiz.ModeText {
		t.Fatalf("Mode = %v, want ModeText", ex.Mode)
	}
	if want := "Which country uses the domain .de?"; ex.Prompt != want {
		t.Fatalf("Prompt = %q, want %q", ex.Prompt, want)
	}
	if ex.CorrectAnswer != "Germany" {
		t.Fatalf("CorrectAnswer = %q, want %q", ex.CorrectAnswer, "Germany")
	}
	assertAccepts(t, ex.Accept, "Germany", "DE", "DEU")

	// A picked autocomplete suggestion arrives as the plain country name and
	// must grade correct through the same free-text matcher the trainer uses.
	if ok, _ := (quiz.TextMatcher{Accept: ex.Accept, MaxEdits: 2}).Match("germany"); !ok {
		t.Fatalf("typed %q should match accepted spellings %v", "germany", ex.Accept)
	}
}

// TestTLDToCountry_Aliases checks the curated alias table reaches Accept for
// the ambiguous big countries autocomplete users type by nickname.
func TestTLDToCountry_Aliases(t *testing.T) {
	gen := NewTLDToCountry()
	us := mkItem("US", "United States", "🇺🇸", "USA", ".us", "")
	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: us, Mode: quiz.ModeText})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	assertAccepts(t, ex.Accept, "United States", "USA", "America")

	gb := mkItem("GB", "United Kingdom", "🇬🇧", "GBR", ".uk", "United Kingdom uses .uk, not .gb")
	ex, err = gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: gb, Mode: quiz.ModeText})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	assertAccepts(t, ex.Accept, "United Kingdom", "UK", "Britain")
	if want := "Which country uses the domain .uk?"; ex.Prompt != want {
		t.Fatalf("GB prompt = %q, want %q", ex.Prompt, want)
	}
}

// TestCountryToTLD_Text is the typed-answer path: a ModeText exercise whose
// prompt is the flag+name, whose canonical answer is the dot-prefixed TLD, and
// which accepts the TLD with or without the leading dot.
func TestCountryToTLD_Text(t *testing.T) {
	gen := NewCountryToTLD()
	item := mkItem("DE", "Germany", "🇩🇪", "DEU", ".de", "")

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: item, Mode: quiz.ModeText})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.Mode != quiz.ModeText {
		t.Fatalf("Mode = %v, want ModeText", ex.Mode)
	}
	if want := "🇩🇪 Germany — which top-level domain?"; ex.Prompt != want {
		t.Fatalf("Prompt = %q, want %q", ex.Prompt, want)
	}
	if ex.CorrectAnswer != ".de" {
		t.Fatalf("CorrectAnswer = %q, want %q", ex.CorrectAnswer, ".de")
	}
	assertAccepts(t, ex.Accept, ".de", "de")
	// Both "de" and ".DE" (case-insensitive) grade correct.
	for _, typed := range []string{"de", ".DE", ".de"} {
		if ok, _ := (quiz.TextMatcher{Accept: ex.Accept, MaxEdits: 2}).Match(typed); !ok {
			t.Fatalf("typed %q should match accepted spellings %v", typed, ex.Accept)
		}
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
			item: mkItem("DE", "Germany", "🇩🇪", "DEU", ".de", ""),
			want: "🇩🇪 Germany's country-code domain is .de.",
		},
		{
			name: "note with Name; prefix stripped",
			item: mkItem("TV", "Tuvalu", "🇹🇻", "TUV", ".tv", "Tuvalu; famously licensed for television sites"),
			want: "🇹🇻 Tuvalu's country-code domain is .tv. (famously licensed for television sites)",
		},
		{
			name: "standalone note kept verbatim",
			item: mkItem("GB", "United Kingdom", "🇬🇧", "GBR", ".uk", "United Kingdom uses .uk, not .gb"),
			want: "🇬🇧 United Kingdom's country-code domain is .uk. (United Kingdom uses .uk, not .gb)",
		},
	}

	// Both directions render the same direction-agnostic intro.
	for _, gen := range []*genWrap{{"tld->country", NewTLDToCountry()}, {"country->tld", NewCountryToTLD()}} {
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
		{"invalid json", []byte(`{"tld":`)},
		{"missing name", []byte(`{"iso_a2":"DE","tld":".de"}`)},
		{"missing iso2", []byte(`{"name":"Germany","tld":".de"}`)},
		{"missing tld", []byte(`{"name":"Germany","iso_a2":"DE"}`)},
	}
	gens := []*engineGen{{"tld->country", NewTLDToCountry()}, {"country->tld", NewCountryToTLD()}}
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

// TestSingleFallbackDeterministic spot-checks that the (production-unused but
// descriptor-valid) single-choice fallback is deterministic given rng and
// includes the correct answer — cheap insurance the descriptor is well-formed.
func TestSingleFallbackDeterministic(t *testing.T) {
	gen := NewCountryToTLD()
	item := mkItem("DE", "Germany", "🇩🇪", "DEU", ".de", "")
	siblings := []storage.Item{
		mkItem("FR", "France", "🇫🇷", "FRA", ".fr", ""),
		mkItem("IT", "Italy", "🇮🇹", "ITA", ".it", ""),
		mkItem("ES", "Spain", "🇪🇸", "ESP", ".es", ""),
		mkItem("JP", "Japan", "🇯🇵", "JPN", ".jp", ""),
	}
	var first []topics.Option
	for i := 0; i < 3; i++ {
		ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(7)),
			topics.ExerciseRequest{Item: item, Siblings: siblings, Mode: quiz.ModeSingle})
		if err != nil {
			t.Fatalf("BuildExercise: %v", err)
		}
		if ex.CorrectAnswer != "DE" {
			t.Fatalf("single CorrectAnswer = %q, want DE", ex.CorrectAnswer)
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
	// The correct option is labeled with the TLD, not the uppercased iso key.
	for _, o := range first {
		if o.Key == "DE" && o.Label != ".de" {
			t.Fatalf("correct option label = %q, want %q", o.Label, ".de")
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
