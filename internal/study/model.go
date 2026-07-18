package study

// model.go defines the exercises.options jsonb shapes for each quiz.Mode
// (architecture §2.5: "options jsonb NOT NULL, -- mode-specific: [{key,label}]
// | [{keys:[],label}] | {accept:[]}"). These are internal/study's own
// serialization contract — the topics package never touches storage
// (registry.go's doc), so it has no opinion on JSON shape; this package owns
// persisting and re-reading it for grading.

// singleOptionJSON is one ModeSingle button.
type singleOptionJSON struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// setOptionJSON is one ModeSet button: asserts a whole (already-canonicalized
// by the generator) set of keys.
type setOptionJSON struct {
	Keys  []string `json:"keys"`
	Label string   `json:"label"`
}

// textOptionsJSON is the single jsonb object persisted for a ModeText
// exercise (no buttons — quiz.TextMatcher grades a free-typed reply against
// Accept).
type textOptionsJSON struct {
	Accept []string `json:"accept"`
}
