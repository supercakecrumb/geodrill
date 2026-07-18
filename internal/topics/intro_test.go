package topics

import (
	"testing"

	"github.com/google/uuid"
	"github.com/supercakecrumb/engram"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

func newCandidate(lifecycle engram.Lifecycle) IntroCandidate {
	return IntroCandidate{
		Item:      storage.Item{ID: uuid.New()},
		Lifecycle: int16(lifecycle),
	}
}

func idsOf(items []storage.Item) []uuid.UUID {
	out := make([]uuid.UUID, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func TestSelectIntroductionsRespectsBudget(t *testing.T) {
	candidates := make([]IntroCandidate, 5)
	for i := range candidates {
		candidates[i] = newCandidate(engram.LifecycleNew)
	}

	got := SelectIntroductions(candidates, 0, 3)
	if len(got) != 3 {
		t.Fatalf("len(SelectIntroductions(...)) = %d, want 3", len(got))
	}
}

func TestSelectIntroductionsIntroducedTodayReducesBudget(t *testing.T) {
	candidates := make([]IntroCandidate, 5)
	for i := range candidates {
		candidates[i] = newCandidate(engram.LifecycleNew)
	}

	// dailyCap 4, already introduced 3 today -> only 1 remaining.
	got := SelectIntroductions(candidates, 3, 4)
	if len(got) != 1 {
		t.Fatalf("len(SelectIntroductions(...)) = %d, want 1", len(got))
	}
}

func TestSelectIntroductionsBudgetExhaustedReturnsEmptyNonNil(t *testing.T) {
	candidates := []IntroCandidate{newCandidate(engram.LifecycleNew)}

	got := SelectIntroductions(candidates, 5, 5)
	if got == nil {
		t.Fatal("SelectIntroductions(...) = nil, want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("len(SelectIntroductions(...)) = %d, want 0", len(got))
	}
}

func TestSelectIntroductionsUnlimitedCap(t *testing.T) {
	candidates := make([]IntroCandidate, 50)
	for i := range candidates {
		candidates[i] = newCandidate(engram.LifecycleNew)
	}

	got := SelectIntroductions(candidates, 0, 0) // 0 = unlimited
	if len(got) != 50 {
		t.Fatalf("len(SelectIntroductions(...)) = %d, want 50 (unlimited cap)", len(got))
	}
}

func TestSelectIntroductionsPreservesOrder(t *testing.T) {
	candidates := make([]IntroCandidate, 4)
	for i := range candidates {
		candidates[i] = newCandidate(engram.LifecycleNew)
	}

	got := SelectIntroductions(candidates, 0, 0)
	wantIDs := idsOf([]storage.Item{candidates[0].Item, candidates[1].Item, candidates[2].Item, candidates[3].Item})
	gotIDs := idsOf(got)
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("SelectIntroductions(...) order = %v, want %v", gotIDs, wantIDs)
		}
	}
}

func TestSelectIntroductionsFiltersNonNew(t *testing.T) {
	introduced := newCandidate(engram.LifecycleIntroduced)
	reviewing := newCandidate(engram.LifecycleReviewing)
	known := newCandidate(engram.LifecycleKnown)
	fresh := newCandidate(engram.LifecycleNew)

	// Interleave so a naive "take the first N" implementation would fail.
	got := SelectIntroductions([]IntroCandidate{introduced, fresh, reviewing, known}, 0, 0)

	if len(got) != 1 {
		t.Fatalf("len(SelectIntroductions(...)) = %d, want 1 (only the New candidate)", len(got))
	}
	if got[0].ID != fresh.Item.ID {
		t.Fatalf("SelectIntroductions(...)[0].ID = %v, want the New candidate's ID %v", got[0].ID, fresh.Item.ID)
	}
}

func TestSelectIntroductionsNoCandidatesReturnsEmptyNonNil(t *testing.T) {
	got := SelectIntroductions(nil, 0, 10)
	if got == nil {
		t.Fatal("SelectIntroductions(nil, ...) = nil, want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("len(SelectIntroductions(nil, ...)) = %d, want 0", len(got))
	}
}
