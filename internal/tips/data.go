package tips

// tells maps a deck language (ISO-639-3 key) to its recognition cues,
// strongest/most-discriminating first. Text is a SHORT cue (a letter, word,
// or script feature) — the tip shows just the first one present in the
// sentence. Word cues carry an English gloss, e.g. `“sono” means “I am”`;
// letter/suffix/substring cues get a short dash-description instead, e.g.
// `ã/õ — nasal vowels, Portuguese-only`. A cue only has to discriminate
// within its own deck (seeds/decks.yaml).
//
// Plain quoted-word cues (e.g. “você”, “что”) are MINED from the ingested
// Tatoeba corpus: words with ≥1.2%% document frequency in their own language
// and ≤0.4%% (and ≤own/8) in every deck sibling — then hand-filtered to drop
// proper nouns and corpus artifacts. Re-mine after big re-ingests with the
// miner in the repo (mine_test.go).

// tipDecks lists the deck slugs (seeds/decks.yaml) whose languages have a
// curated + corpus-audited cue set. Tips are rolled out deck by deck —
// extend this (and tells) together when a new group is ready.
var tipDecks = map[string]bool{
	"romance": true,
}

// DeckHasTips reports whether the deck with the given slug has tips.
func DeckHasTips(slug string) bool { return tipDecks[slug] }

// tells currently covers the Romance deck only (spa por ita fra cat ron).
// Extend it alongside tipDecks above when the next deck is curated and
// corpus-audited.
var tells = map[string][]Tell{
	// ─── Romance: spa por ita fra cat ron ───
	"spa": {
		{Kind: KindLetters, Pattern: "¿¡", Text: "¿ ¡ — inverted marks, Spanish-only"},
		{Kind: KindLetters, Pattern: "ñ", Text: "ñ — Spanish-only letter"},
		{Kind: KindSuffix, Pattern: "ción", Text: "-ción ending (Portuguese: -ção)"},
		{Kind: KindWord, Pattern: "muy", Text: "“muy” means “very”"},
		{Kind: KindWord, Pattern: "pero", Text: "“pero” means “but”"},
		{Kind: KindWord, Pattern: "los", Text: "“los” means “the”"},
		{Kind: KindWord, Pattern: "él", Text: "“él” means “he”"},
		{Kind: KindWord, Pattern: "más", Text: "“más” means “more”"},
		{Kind: KindWord, Pattern: "qué", Text: "“qué” means “what”"},
		{Kind: KindWord, Pattern: "yo", Text: "“yo” means “I”"},
		{Kind: KindWord, Pattern: "hay", Text: "“hay” means “there is”"},
		{Kind: KindWord, Pattern: "estoy", Text: "“estoy” means “I am”"},
	},
	"por": {
		{Kind: KindLetters, Pattern: "ãõ", Text: "ã/õ — nasal vowels, Portuguese-only"},
		{Kind: KindSuffix, Pattern: "ção", Text: "-ção ending (Spanish: -ción)"},
		{Kind: KindWord, Pattern: "não", Text: "“não” means “no”"},
		{Kind: KindWord, Pattern: "é", Text: "“é” means “is”"},
		{Kind: KindSubstring, Pattern: "lh", Text: "lh — Portuguese digraph"},
		{Kind: KindWord, Pattern: "você", Text: "“você” means “you”"},
		{Kind: KindWord, Pattern: "um", Text: "“um” means “a”"},
		{Kind: KindWord, Pattern: "uma", Text: "“uma” means “a”"},
		{Kind: KindWord, Pattern: "ele", Text: "“ele” means “he”"},
		{Kind: KindWord, Pattern: "isso", Text: "“isso” means “that”"},
		{Kind: KindWord, Pattern: "muito", Text: "“muito” means “very”"},
	},
	"ita": {
		{Kind: KindWord, Pattern: "è", Text: "“è” means “is”"},
		{Kind: KindWord, Pattern: "che", Text: "“che” means “that”"},
		{Kind: KindSuffix, Pattern: "zione", Text: "-zione ending, Italian-only"},
		{Kind: KindSubstring, Pattern: "gli", Text: "gli — Italian-only cluster"},
		{Kind: KindWord, Pattern: "perché", Text: "“perché” means “why”"},
		{Kind: KindWord, Pattern: "non", Text: "“non” means “not”"},
		{Kind: KindWord, Pattern: "di", Text: "“di” means “of”"},
		{Kind: KindWord, Pattern: "sono", Text: "“sono” means “I am”"},
		{Kind: KindWord, Pattern: "io", Text: "“io” means “I”"},
		{Kind: KindWord, Pattern: "questo", Text: "“questo” means “this”"},
		{Kind: KindWord, Pattern: "più", Text: "“più” means “more”"},
	},
	"fra": {
		{Kind: KindLetters, Pattern: "œ", Text: "œ — French-only ligature"},
		{Kind: KindWord, Pattern: "est", Text: "“est” means “is”"},
		{Kind: KindWord, Pattern: "je", Text: "“je” means “I”"},
		{Kind: KindSubstring, Pattern: "eau", Text: "-eau ending, French-only"},
		{Kind: KindWord, Pattern: "pas", Text: "“pas” means “not”"},
		{Kind: KindWord, Pattern: "vous", Text: "“vous” means “you”"},
		{Kind: KindWord, Pattern: "une", Text: "“une” means “a”"},
		{Kind: KindWord, Pattern: "nous", Text: "“nous” means “we”"},
		{Kind: KindWord, Pattern: "dans", Text: "“dans” means “in”"},
		{Kind: KindWord, Pattern: "pour", Text: "“pour” means “for”"},
		{Kind: KindWord, Pattern: "suis", Text: "“suis” means “am”"},
	},
	"cat": {
		{Kind: KindSubstring, Pattern: "l·l", Text: "l·l — Catalan-only"},
		{Kind: KindWord, Pattern: "amb", Text: "“amb” means “with”"},
		{Kind: KindWord, Pattern: "és", Text: "“és” means “is”"},
		{Kind: KindSuffix, Pattern: "ció", Text: "-ció ending (Spanish: -ción)"},
		{Kind: KindSubstring, Pattern: "ny", Text: "ny — Catalan's ñ"},
		{Kind: KindWord, Pattern: "els", Text: "“els” means “the”"},
		{Kind: KindWord, Pattern: "més", Text: "“més” means “more”"},
		{Kind: KindWord, Pattern: "vaig", Text: "“vaig” means “I go”"},
		{Kind: KindWord, Pattern: "molt", Text: "“molt” means “very”"},
		{Kind: KindWord, Pattern: "què", Text: "“què” means “what”"},
		{Kind: KindWord, Pattern: "hi", Text: "“hi” means “there”"},
	},
	"ron": {
		{Kind: KindLetters, Pattern: "șț", Text: "ș/ț — Romanian-only letters"},
		{Kind: KindLetters, Pattern: "ă", Text: "ă — Romanian-only letter"},
		{Kind: KindWord, Pattern: "și", Text: "“și” means “and”"},
		{Kind: KindWord, Pattern: "nu", Text: "“nu” means “no”"},
		{Kind: KindWord, Pattern: "să", Text: "“să” means “to”"},
		{Kind: KindWord, Pattern: "în", Text: "“în” means “in”"},
		{Kind: KindWord, Pattern: "că", Text: "“că” means “that”"},
		{Kind: KindWord, Pattern: "sunt", Text: "“sunt” means “I am”"},
		{Kind: KindWord, Pattern: "pentru", Text: "“pentru” means “for”"},
		{Kind: KindWord, Pattern: "asta", Text: "“asta” means “this”"},
	},
}
