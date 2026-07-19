package profiles

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

// mkItem builds a storage.Item carrying a well-formed profiles payload, keyed
// the way Seed keys items (iso_a2).
func mkItem(iso, name, flag, region string, languages ...string) storage.Item {
	raw, err := json.Marshal(itemPayload{
		Flag:      flag,
		Name:      name,
		ISOA2:     iso,
		ISOA3:     iso + "X",
		Region:    region,
		Languages: languages,
	})
	if err != nil {
		panic(err)
	}
	return storage.Item{Key: iso, Label: name, Payload: raw}
}

func TestKind(t *testing.T) {
	if got := New().Kind(); got != Kind {
		t.Fatalf("Kind() = %q, want %q", got, Kind)
	}
}

// TestTopicMode pins the topic's exercise-mode config: single-choice only,
// no autocomplete/text mode (this is a fixed set of sign-visible languages,
// not a free-text quiz).
func TestTopicMode(t *testing.T) {
	path := languageTopic()
	leaf := path[len(path)-1]
	if leaf.Slug != LanguageSlug {
		t.Fatalf("leaf slug = %q, want %q", leaf.Slug, LanguageSlug)
	}
	if len(leaf.ExerciseModes) != 1 || leaf.ExerciseModes[0] != "single" {
		t.Fatalf("exercise_modes = %v, want [single]", leaf.ExerciseModes)
	}
	if leaf.QuizKind != Kind {
		t.Fatalf("quiz_kind = %q, want %q", leaf.QuizKind, Kind)
	}
	if !leaf.IsQuizzable {
		t.Fatalf("leaf topic should be quizzable")
	}
}

// TestBuildExercise_SameRegionOnly is the core same-region-distractor
// contract: given siblings spanning multiple regions, the built exercise
// never offers a language from a region other than the target's.
func TestBuildExercise_SameRegionOnly(t *testing.T) {
	gen := New()
	target := mkItem("KE", "Kenya", "🇰🇪", "East Africa", "Swahili", "English")
	siblings := []storage.Item{
		mkItem("TZ", "Tanzania", "🇹🇿", "East Africa", "Swahili"),
		mkItem("RW", "Rwanda", "🇷🇼", "East Africa", "Kinyarwanda"),
		mkItem("SO", "Somalia", "🇸🇴", "East Africa", "Somali"),
		mkItem("ET", "Ethiopia", "🇪🇹", "East Africa", "Amharic"),
		// Far-region siblings: must never appear as a distractor.
		mkItem("FR", "France", "🇫🇷", "Western Europe", "French"),
		mkItem("DE", "Germany", "🇩🇪", "Western Europe", "German"),
		mkItem("JP", "Japan", "🇯🇵", "East Asia", "Japanese"),
	}

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: target, Siblings: siblings, Mode: quiz.ModeSingle})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.CorrectAnswer != languageKey("Swahili") {
		t.Fatalf("CorrectAnswer = %q, want %q", ex.CorrectAnswer, languageKey("Swahili"))
	}
	wantPrompt := "What language do you see on signs in 🇰🇪 Kenya?"
	if ex.Prompt != wantPrompt {
		t.Fatalf("Prompt = %q, want %q", ex.Prompt, wantPrompt)
	}

	farRegionKeys := map[string]bool{
		languageKey("French"):   true,
		languageKey("German"):   true,
		languageKey("Japanese"): true,
	}
	foundCorrect := false
	for _, o := range ex.Options {
		if farRegionKeys[o.Key] {
			t.Fatalf("option %+v is from a far region, must never be offered", o)
		}
		if o.Key == ex.CorrectAnswer {
			foundCorrect = true
		}
	}
	if !foundCorrect {
		t.Fatalf("options %v missing the correct answer %q", ex.Options, ex.CorrectAnswer)
	}
}

// TestBuildExercise_ExcludesOwnOtherLanguages is the exclusion-filter
// contract described in generator.go's package doc: Kenya's own secondary
// language (English) must never be offered as a distractor for Kenya's
// question, even though a same-region sibling's PRIMARY language is English
// (the real-world collision this package's wrapper exists to fix).
func TestBuildExercise_ExcludesOwnOtherLanguages(t *testing.T) {
	gen := New()
	target := mkItem("KE", "Kenya", "🇰🇪", "East Africa", "Swahili", "English")
	siblings := []storage.Item{
		mkItem("UG", "Uganda", "🇺🇬", "East Africa", "English"), // primary = Kenya's own secondary
		mkItem("RW", "Rwanda", "🇷🇼", "East Africa", "Kinyarwanda"),
		mkItem("SO", "Somalia", "🇸🇴", "East Africa", "Somali"),
	}

	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
		topics.ExerciseRequest{Item: target, Siblings: siblings, Mode: quiz.ModeSingle})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	for _, o := range ex.Options {
		if o.Key == languageKey("English") {
			t.Fatalf("options %v must never include English — it's also correct for Kenya", ex.Options)
		}
	}
	// Rwanda and Somalia's languages remain legitimate same-region distractors.
	got := make(map[string]bool, len(ex.Options))
	for _, o := range ex.Options {
		got[o.Key] = true
	}
	if !got[languageKey("Kinyarwanda")] || !got[languageKey("Somali")] {
		t.Fatalf("options %v should still include the non-colliding same-region distractors", ex.Options)
	}
}

// TestBuildExercise_MultiLanguageCountry checks the CorrectAnswer is always
// the PRIMARY (first-listed) language, regardless of how many languages the
// country has.
func TestBuildExercise_MultiLanguageCountry(t *testing.T) {
	gen := New()
	target := mkItem("AD", "Andorra", "🇦🇩", "Southern Europe", "Catalan", "French", "Spanish")
	siblings := []storage.Item{
		mkItem("ES", "Spain", "🇪🇸", "Southern Europe", "Spanish"),
		mkItem("IT", "Italy", "🇮🇹", "Southern Europe", "Italian"),
	}
	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(3)),
		topics.ExerciseRequest{Item: target, Siblings: siblings, Mode: quiz.ModeSingle})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if ex.CorrectAnswer != languageKey("Catalan") {
		t.Fatalf("CorrectAnswer = %q, want %q (the primary language)", ex.CorrectAnswer, languageKey("Catalan"))
	}
	// Spain's "Spanish" is also one of Andorra's own languages: must be excluded.
	for _, o := range ex.Options {
		if o.Key == languageKey("Spanish") {
			t.Fatalf("options %v must exclude Spanish — it's also correct for Andorra", ex.Options)
		}
	}
}

// TestBuildExercise_Determinism spot-checks that repeated calls with the same
// rng seed produce identical exercises (dedup/shuffle/cap all go through the
// injected rng, per the engine's own contract; this package's post-filter
// must preserve that).
func TestBuildExercise_Determinism(t *testing.T) {
	gen := New()
	target := mkItem("KE", "Kenya", "🇰🇪", "East Africa", "Swahili", "English")
	siblings := []storage.Item{
		mkItem("TZ", "Tanzania", "🇹🇿", "East Africa", "Swahili"),
		mkItem("RW", "Rwanda", "🇷🇼", "East Africa", "Kinyarwanda"),
		mkItem("SO", "Somalia", "🇸🇴", "East Africa", "Somali"),
		mkItem("ET", "Ethiopia", "🇪🇹", "East Africa", "Amharic"),
		mkItem("UG", "Uganda", "🇺🇬", "East Africa", "English"),
		mkItem("SS", "South Sudan", "🇸🇸", "East Africa", "English"),
	}

	var first []topics.Option
	for i := 0; i < 3; i++ {
		ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(42)),
			topics.ExerciseRequest{Item: target, Siblings: siblings, Mode: quiz.ModeSingle})
		if err != nil {
			t.Fatalf("BuildExercise: %v", err)
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
}

// TestBuildExercise_ThinRegionUnderfill checks the engine's under-fill
// fallback: when there are zero eligible same-region distractors (e.g. this
// item is the only one of its region among the siblings given, mirroring
// North Africa's real all-Arabic thin-region case), the exercise still
// builds successfully with just the correct answer — never an error.
func TestBuildExercise_ThinRegionUnderfill(t *testing.T) {
	gen := New()
	target := mkItem("EG", "Egypt", "🇪🇬", "North Africa", "Arabic")
	siblings := []storage.Item{
		mkItem("LY", "Libya", "🇱🇾", "North Africa", "Arabic"), // same region, same language as target -> deduped away
		mkItem("FR", "France", "🇫🇷", "Western Europe", "French"),
	}
	ex, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(9)),
		topics.ExerciseRequest{Item: target, Siblings: siblings, Mode: quiz.ModeSingle})
	if err != nil {
		t.Fatalf("BuildExercise: %v", err)
	}
	if len(ex.Options) != 1 {
		t.Fatalf("Options = %v, want exactly the correct answer alone (thin-region under-fill)", ex.Options)
	}
	if ex.Options[0].Key != ex.CorrectAnswer {
		t.Fatalf("sole option = %+v, want the correct answer %q", ex.Options[0], ex.CorrectAnswer)
	}
}

// TestBuildIntro checks both the singular and plural phrasing.
func TestBuildIntro(t *testing.T) {
	gen := New()

	single, err := gen.BuildIntro(context.Background(), mkItem("EG", "Egypt", "🇪🇬", "North Africa", "Arabic"))
	if err != nil {
		t.Fatalf("BuildIntro: %v", err)
	}
	if want := "🇪🇬 Egypt's sign-visible language is Arabic."; single.Text != want {
		t.Fatalf("intro = %q, want %q", single.Text, want)
	}

	multi, err := gen.BuildIntro(context.Background(), mkItem("KE", "Kenya", "🇰🇪", "East Africa", "Swahili", "English"))
	if err != nil {
		t.Fatalf("BuildIntro: %v", err)
	}
	if want := "🇰🇪 Kenya's sign-visible languages are Swahili and English."; multi.Text != want {
		t.Fatalf("intro = %q, want %q", multi.Text, want)
	}

	triple, err := gen.BuildIntro(context.Background(), mkItem("AD", "Andorra", "🇦🇩", "Southern Europe", "Catalan", "French", "Spanish"))
	if err != nil {
		t.Fatalf("BuildIntro: %v", err)
	}
	if want := "🇦🇩 Andorra's sign-visible languages are Catalan, French and Spanish."; triple.Text != want {
		t.Fatalf("intro = %q, want %q", triple.Text, want)
	}
}

func TestMalformedPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{"empty", nil},
		{"invalid json", []byte(`{"languages":`)},
		{"missing name", []byte(`{"iso_a2":"KE","region":"East Africa","languages":["Swahili"]}`)},
		{"missing iso2", []byte(`{"name":"Kenya","region":"East Africa","languages":["Swahili"]}`)},
		{"missing region", []byte(`{"name":"Kenya","iso_a2":"KE","languages":["Swahili"]}`)},
		{"empty languages", []byte(`{"name":"Kenya","iso_a2":"KE","region":"East Africa","languages":[]}`)},
		{"blank language entry", []byte(`{"name":"Kenya","iso_a2":"KE","region":"East Africa","languages":[""]}`)},
	}
	gen := New()
	for _, tc := range cases {
		item := storage.Item{Key: "bad", Payload: tc.payload}
		if _, err := gen.BuildExercise(context.Background(), rand.New(rand.NewSource(1)),
			topics.ExerciseRequest{Item: item, Mode: quiz.ModeSingle}); !errors.Is(err, ErrMalformedPayload) {
			t.Fatalf("%s: BuildExercise err = %v, want ErrMalformedPayload", tc.name, err)
		}
		if _, err := gen.BuildIntro(context.Background(), item); !errors.Is(err, ErrMalformedPayload) {
			t.Fatalf("%s: BuildIntro err = %v, want ErrMalformedPayload", tc.name, err)
		}
	}
}

func TestLanguageKeyStable(t *testing.T) {
	cases := map[string]string{
		"Swahili":             "swahili",
		"Cook Islands Māori":  "cook-islands-m-ori",
		"Cape Verdean Creole": "cape-verdean-creole",
	}
	for name, want := range cases {
		if got := languageKey(name); got != want {
			t.Fatalf("languageKey(%q) = %q, want %q", name, got, want)
		}
	}
}
