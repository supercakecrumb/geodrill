package content

import (
	"reflect"
	"testing"
)

func TestDedupeCandidates(t *testing.T) {
	in := []Candidate{
		{ID: "1", Text: "hello"},
		{ID: "2", Text: "world"},
		{ID: "3", Text: "hello"}, // duplicate text, later id - dropped
		{ID: "4", Text: "again"},
		{ID: "5", Text: "world"}, // duplicate text - dropped
	}
	want := []Candidate{
		{ID: "1", Text: "hello"},
		{ID: "2", Text: "world"},
		{ID: "4", Text: "again"},
	}
	got := DedupeCandidates(in)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DedupeCandidates() = %+v, want %+v", got, want)
	}
}

func TestDedupeCandidates_empty(t *testing.T) {
	got := DedupeCandidates(nil)
	if len(got) != 0 {
		t.Errorf("DedupeCandidates(nil) = %+v, want empty", got)
	}
}

func makeCandidates(n int) []Candidate {
	out := make([]Candidate, n)
	for i := 0; i < n; i++ {
		out[i] = Candidate{ID: string(rune('a' + i)), Text: string(rune('A' + i))}
	}
	return out
}

func TestSampleCap_passthroughWhenUnderCap(t *testing.T) {
	items := makeCandidates(5)
	got := SampleCap(items, 10, 42)
	if !reflect.DeepEqual(got, items) {
		t.Errorf("SampleCap under cap = %+v, want unchanged %+v", got, items)
	}
}

func TestSampleCap_passthroughWhenEqualToCap(t *testing.T) {
	items := makeCandidates(5)
	got := SampleCap(items, 5, 42)
	if !reflect.DeepEqual(got, items) {
		t.Errorf("SampleCap at cap = %+v, want unchanged %+v", got, items)
	}
}

func TestSampleCap_respectsCap(t *testing.T) {
	items := makeCandidates(100)
	got := SampleCap(items, 10, 42)
	if len(got) != 10 {
		t.Fatalf("SampleCap len = %d, want 10", len(got))
	}
}

func TestSampleCap_deterministicForSameSeed(t *testing.T) {
	items := makeCandidates(100)

	got1 := SampleCap(items, 10, 42)
	got2 := SampleCap(items, 10, 42)

	if !reflect.DeepEqual(got1, got2) {
		t.Errorf("SampleCap with same seed produced different results:\n%+v\nvs\n%+v", got1, got2)
	}
}

func TestSampleCap_differentSeedsCanDiffer(t *testing.T) {
	items := makeCandidates(100)

	got1 := SampleCap(items, 10, 1)
	got2 := SampleCap(items, 10, 2)

	if reflect.DeepEqual(got1, got2) {
		t.Errorf("SampleCap with different seeds produced identical results (suspicious): %+v", got1)
	}
}

func TestSampleCap_doesNotMutateInput(t *testing.T) {
	items := makeCandidates(100)
	orig := make([]Candidate, len(items))
	copy(orig, items)

	_ = SampleCap(items, 10, 42)

	if !reflect.DeepEqual(items, orig) {
		t.Error("SampleCap mutated its input slice")
	}
}

func TestSampleCap_negativeCap(t *testing.T) {
	items := makeCandidates(5)
	got := SampleCap(items, -1, 42)
	if len(got) != 0 {
		t.Errorf("SampleCap with negative cap = %+v, want empty", got)
	}
}
