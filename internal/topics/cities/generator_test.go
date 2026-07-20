package cities

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/supercakecrumb/engram/quiz"

	"github.com/supercakecrumb/geodrill/internal/storage"
	"github.com/supercakecrumb/geodrill/internal/topics"
)

func intPtr(v int) *int { return &v }

// mkItem builds a storage.Item carrying a well-formed cities payload. opts
// mutate the payload so a test can toggle map presence / region / elevation /
// fact.
func mkItem(key string, opts ...func(*itemPayload)) storage.Item {
	p := itemPayload{
		Key:         key,
		CityName:    "Munich",
		Flag:        "🇩🇪",
		CountryName: "Germany",
		ISOA2:       "DE",
		ISOA3:       "DEU",
		Lat:         48.13743,
		Lng:         11.57549,
		Region:      "Bavaria",
		Population:  1512491,
		ElevationM:  intPtr(520),
		MapImage:    "de-munich.png",
	}
	for _, o := range opts {
		o(&p)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}
	return storage.Item{Key: key, Label: p.CityName, Payload: raw}
}

func TestKind(t *testing.T) {
	if got := New().Kind(); got != Kind {
		t.Fatalf("Kind() = %q, want %q", got, Kind)
	}
	if Kind != "city_on_map" {
		t.Fatalf("Kind = %q, want city_on_map", Kind)
	}
}

// TestTopicMode pins the topic's exercise-mode config — the autocomplete
// wiring depends on the leaf declaring "autocomplete" (internal/study maps
// that to ModeText + the "⌨️ Type your answer" button).
func TestTopicMode(t *testing.T) {
	ct := cityTopic()
	leaf := ct[len(ct)-1]
	if leaf.Slug != LeafSlug || LeafSlug != "city-on-map" {
		t.Fatalf("leaf slug = %q, want city-on-map", leaf.Slug)
	}
	if len(leaf.ExerciseModes) != 1 || leaf.ExerciseModes[0] != "autocomplete" {
		t.Fatalf("exercise_modes = %v, want [autocomplete]", leaf.ExerciseModes)
	}
	if leaf.QuizKind != Kind || !leaf.IsQuizzable {
		t.Fatalf("leaf = %+v, want quizzable with quiz_kind %q", leaf, Kind)
	}
	root := ct[0]
	if root.Slug != RootSlug || root.QuizKind != "" {
		t.Fatalf("root = %+v, want a plain container named %q", root, RootSlug)
	}
}

// TestExerciseMapPresent: with a synced map image, the exercise is a photo
// question — MediaPath is the garage:// ref and the prompt is the map question.
func TestExerciseMapPresent(t *testing.T) {
	gen := New()
	item := mkItem("de:munich")
	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: item, Mode: quiz.ModeText})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.Mode != quiz.ModeText {
		t.Fatalf("Mode = %v, want ModeText", ex.Mode)
	}
	if want := "garage://apps-geodrill/citymaps/de-munich.png"; ex.MediaPath != want {
		t.Fatalf("MediaPath = %q, want %q", ex.MediaPath, want)
	}
	if ex.Prompt != reviewSubject {
		t.Fatalf("Prompt = %q, want the map question %q", ex.Prompt, reviewSubject)
	}
	// Answer is the CITY: canonical answer = display name, Accept holds the
	// city spellings.
	if ex.CorrectAnswer != "Munich" {
		t.Fatalf("CorrectAnswer = %q, want Munich", ex.CorrectAnswer)
	}
}

// TestExerciseMissingMapFallback: an empty media root (or an item whose image
// hasn't been synced) degrades to the still-answerable text fallback.
func TestExerciseMissingMapFallback(t *testing.T) {
	// Force the fallback two ways: (1) generator with "" media root, and
	// (2) an item whose payload has no map_image.
	for _, tc := range []struct {
		name string
		gen  *Generator
		item storage.Item
	}{
		{"empty media root", NewWithMediaRoot(""), mkItem("de:munich")},
		{"no map_image", New(), mkItem("de:munich", func(p *itemPayload) { p.MapImage = "" })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ex, err := tc.gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
				topics.ExerciseRequest{Item: tc.item, Mode: quiz.ModeText})
			if err != nil {
				t.Fatalf("BuildExercise: %v", err)
			}
			if ex.MediaPath != "" {
				t.Fatalf("MediaPath = %q, want empty (fallback)", ex.MediaPath)
			}
			want := "🏙 Name the city in 🇩🇪 Germany (Bavaria) with about 1,500,000 people."
			if ex.Prompt != want {
				t.Fatalf("fallback Prompt = %q, want %q", ex.Prompt, want)
			}
			if ex.CorrectAnswer != "Munich" {
				t.Fatalf("CorrectAnswer = %q, want Munich", ex.CorrectAnswer)
			}
		})
	}
}

// TestFallbackPromptNoRegion: the region parenthetical is omitted when Region
// is empty, and population is rounded to 2 significant figures.
func TestFallbackPromptNoRegion(t *testing.T) {
	p := itemPayload{Flag: "🇩🇪", CountryName: "Germany", Population: 1512491}
	if want := "🏙 Name the city in 🇩🇪 Germany with about 1,500,000 people."; fallbackPrompt(p) != want {
		t.Fatalf("fallbackPrompt = %q, want %q", fallbackPrompt(p), want)
	}
}

// TestIntroMapPresent / caption variants.
func TestIntroCaptionFull(t *testing.T) {
	gen := New()
	item := mkItem("de:munich", func(p *itemPayload) {
		p.Fact = "Munich is the capital and most populous city of Bavaria."
		p.FactURL = "https://en.wikipedia.org/wiki/Munich"
	})
	card, err := gen.BuildIntro(context.Background(), item)
	if err != nil {
		t.Fatalf("BuildIntro: %v", err)
	}
	if want := "garage://apps-geodrill/citymaps/de-munich.png"; card.MediaPath != want {
		t.Fatalf("MediaPath = %q, want %q", card.MediaPath, want)
	}
	want := "📍 Munich — 🇩🇪 Germany\n" +
		"🗺 Bavaria\n" +
		"👥 1,512,491 people\n" +
		"⛰ 520 m elevation\n\n" +
		"Munich is the capital and most populous city of Bavaria.\n\n" +
		"📖 en.wikipedia.org/wiki/Munich · CC BY-SA 4.0"
	if card.Text != want {
		t.Fatalf("caption =\n%q\nwant\n%q", card.Text, want)
	}
}

func TestIntroCaptionVariants(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*itemPayload)
		want string
	}{
		{
			name: "no region",
			mut:  func(p *itemPayload) { p.Region = "" },
			want: "📍 Munich — 🇩🇪 Germany\n👥 1,512,491 people\n⛰ 520 m elevation",
		},
		{
			name: "no elevation",
			mut:  func(p *itemPayload) { p.ElevationM = nil },
			want: "📍 Munich — 🇩🇪 Germany\n🗺 Bavaria\n👥 1,512,491 people",
		},
		{
			name: "no population",
			mut:  func(p *itemPayload) { p.Population = 0 },
			want: "📍 Munich — 🇩🇪 Germany\n🗺 Bavaria\n⛰ 520 m elevation",
		},
		{
			name: "no fact (no attribution line)",
			mut:  func(p *itemPayload) { p.Fact = ""; p.FactURL = "https://example.org/x" },
			want: "📍 Munich — 🇩🇪 Germany\n🗺 Bavaria\n👥 1,512,491 people\n⛰ 520 m elevation",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := itemPayload{
				Key: "de:munich", CityName: "Munich", Flag: "🇩🇪", CountryName: "Germany",
				ISOA2: "DE", Region: "Bavaria", Population: 1512491, ElevationM: intPtr(520),
			}
			tc.mut(&p)
			if got := introCaption(p); got != tc.want {
				t.Fatalf("introCaption =\n%q\nwant\n%q", got, tc.want)
			}
			if strings.Contains(tc.name, "no fact") && strings.Contains(introCaption(p), "CC BY-SA") {
				t.Fatalf("attribution line must be gated on a non-empty fact")
			}
		})
	}
}

// TestFormatInt covers the comma grouping used by the caption + fallback.
func TestFormatInt(t *testing.T) {
	cases := map[int64]string{0: "0", 5: "5", 999: "999", 1000: "1,000", 1512491: "1,512,491", 24874500: "24,874,500"}
	for in, want := range cases {
		if got := formatInt(in); got != want {
			t.Fatalf("formatInt(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestRoundToSigFigs(t *testing.T) {
	cases := []struct {
		in   int64
		sig  int
		want int64
	}{
		{1512491, 2, 1500000},
		{24874500, 2, 25000000},
		{99, 2, 99},
		{1234, 2, 1200},
		{1250, 2, 1300},
		{0, 2, 0},
	}
	for _, c := range cases {
		if got := roundToSigFigs(c.in, c.sig); got != c.want {
			t.Fatalf("roundToSigFigs(%d,%d) = %d, want %d", c.in, c.sig, got, c.want)
		}
	}
}

// TestAcceptNameAndAltNames: the Accept list for a real city is its name plus
// its alt_names (São Paulo -> "São Paulo", "Sao Paulo").
func TestAcceptNameAndAltNames(t *testing.T) {
	gen := New()
	item := mkItem("br:sao-paulo", func(p *itemPayload) {
		p.CityName = "São Paulo"
		p.MapImage = ""
	})
	item.Label = "São Paulo"
	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: item, Mode: quiz.ModeText})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	have := map[string]bool{}
	for _, a := range ex.Accept {
		have[a] = true
	}
	for _, w := range []string{"São Paulo", "Sao Paulo"} {
		if !have[w] {
			t.Fatalf("Accept %v missing %q", ex.Accept, w)
		}
	}
	if ex.CorrectAnswer != "São Paulo" {
		t.Fatalf("CorrectAnswer = %q, want São Paulo", ex.CorrectAnswer)
	}
	// The typed exonym must grade correct through the free-text matcher.
	if ok, _ := (quiz.TextMatcher{Accept: ex.Accept, MaxEdits: 2}).Match("sao paulo"); !ok {
		t.Fatalf("typed %q should match %v", "sao paulo", ex.Accept)
	}
}

func TestMalformedPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{"empty", nil},
		{"invalid json", []byte(`{"city_name":`)},
		{"missing key", []byte(`{"city_name":"Munich","iso_a2":"DE","country_name":"Germany"}`)},
		{"missing city_name", []byte(`{"key":"de:munich","iso_a2":"DE","country_name":"Germany"}`)},
		{"missing iso2", []byte(`{"key":"de:munich","city_name":"Munich","country_name":"Germany"}`)},
		{"missing country_name", []byte(`{"key":"de:munich","city_name":"Munich","iso_a2":"DE"}`)},
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

// TestSingleFallbackDeterministic spot-checks the (production-unused but
// descriptor-valid) single-choice fallback: deterministic given rng, includes
// the correct city key.
func TestSingleFallbackDeterministic(t *testing.T) {
	gen := New()
	item := mkItem("de:munich", func(p *itemPayload) { p.MapImage = "" })
	siblings := []storage.Item{
		mkItem("fr:paris", func(p *itemPayload) { p.Key = "fr:paris"; p.CityName = "Paris"; p.MapImage = "" }),
		mkItem("it:rome", func(p *itemPayload) { p.Key = "it:rome"; p.CityName = "Rome"; p.MapImage = "" }),
	}
	var first []topics.Option
	for i := 0; i < 3; i++ {
		ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(7)),
			topics.ExerciseRequest{Item: item, Siblings: siblings, Mode: quiz.ModeSingle})
		if err != nil {
			t.Fatalf("BuildExercise: %v", err)
		}
		if ex.CorrectAnswer != "de:munich" {
			t.Fatalf("single CorrectAnswer = %q, want de:munich", ex.CorrectAnswer)
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
				t.Fatalf("option order not deterministic at %d", j)
			}
		}
	}
}

// TestCaptionUnderLimit is a cheap guard on the constant (the full seed audit
// lives in audit_test.go).
func TestCaptionUnderLimit(t *testing.T) {
	p := itemPayload{
		Key: "de:munich", CityName: "Munich", Flag: "🇩🇪", CountryName: "Germany",
		Region: "Bavaria", Population: 1512491, ElevationM: intPtr(520),
		Fact:    strings.Repeat("x", 400),
		FactURL: "https://en.wikipedia.org/wiki/Munich",
	}
	if n := utf8.RuneCountInString(introCaption(p)); n >= captionLimit {
		t.Fatalf("caption rune count = %d, want < %d", n, captionLimit)
	}
}
