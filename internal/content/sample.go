package content

import "math/rand"

// Candidate is a filtered, not-yet-inserted content row.
type Candidate struct {
	ID    string // Tatoeba sentence id
	Text  string
	Runes int
}

// DedupeCandidates removes exact-text duplicates, keeping the first
// occurrence and preserving order.
func DedupeCandidates(items []Candidate) []Candidate {
	seen := make(map[string]struct{}, len(items))
	out := make([]Candidate, 0, len(items))
	for _, c := range items {
		if _, ok := seen[c.Text]; ok {
			continue
		}
		seen[c.Text] = struct{}{}
		out = append(out, c)
	}
	return out
}

// SampleCap returns at most cap items from items. If len(items) <= cap, it
// returns items as-is (stable order, no copy of order semantics changed).
// Otherwise it deterministically shuffles a copy of items using
// math/rand.New(rand.NewSource(seed)) (Fisher-Yates) and returns the first
// cap elements. The same seed and input always produce the same output.
func SampleCap[T any](items []T, cap int, seed int64) []T {
	if cap < 0 {
		cap = 0
	}
	if len(items) <= cap {
		return items
	}

	shuffled := make([]T, len(items))
	copy(shuffled, items)

	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	return shuffled[:cap]
}
