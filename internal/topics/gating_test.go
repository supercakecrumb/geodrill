package topics

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

func tp(tier int, complete bool) storage.TierProgress {
	return storage.TierProgress{Tier: int16(tier), Complete: complete}
}

func TestUnlockedTiers(t *testing.T) {
	tests := []struct {
		name string
		in   []storage.TierProgress
		want []int
	}{
		{
			name: "empty progress unlocks only the base tiers",
			in:   nil,
			want: []int{0, 1},
		},
		{
			name: "nothing complete unlocks only the base tiers",
			in:   []storage.TierProgress{tp(0, false), tp(1, false)},
			want: []int{0, 1},
		},
		{
			name: "tier 0 complete unlocks tier 2",
			in:   []storage.TierProgress{tp(0, true)},
			want: []int{0, 1, 2},
		},
		{
			name: "tier 1 complete unlocks tier 3",
			in:   []storage.TierProgress{tp(1, true)},
			want: []int{0, 1, 3},
		},
		{
			name: "gap: tier 0 and tier 2 complete but tier 1 is not",
			in:   []storage.TierProgress{tp(0, true), tp(1, false), tp(2, true)},
			want: []int{0, 1, 2, 4},
		},
		{
			name: "full chain 0..3 complete unlocks every tier up to 5",
			in:   []storage.TierProgress{tp(0, true), tp(1, true), tp(2, true), tp(3, true)},
			want: []int{0, 1, 2, 3, 4, 5},
		},
		{
			name: "unordered input still produces sorted output",
			in:   []storage.TierProgress{tp(3, true), tp(0, true)},
			want: []int{0, 1, 2, 5},
		},
		{
			name: "duplicate rows for the same tier are idempotent",
			in:   []storage.TierProgress{tp(0, true), tp(0, true)},
			want: []int{0, 1, 2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UnlockedTiers(tt.in)
			if !equalInts(got, tt.want) {
				t.Fatalf("UnlockedTiers(%+v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// fakeTierStore is an in-memory tierStore for testing Service's passthrough
// behavior without a database.
type fakeTierStore struct {
	progress    []storage.TierProgress
	progressErr error

	recomputed   storage.TierProgress
	recomputeOK  bool
	recomputeErr error
	gotUserID    uuid.UUID
	gotTier      int16
}

func (f *fakeTierStore) ListTierProgressForUser(ctx context.Context, userID uuid.UUID) ([]storage.TierProgress, error) {
	if f.progressErr != nil {
		return nil, f.progressErr
	}
	return f.progress, nil
}

func (f *fakeTierStore) RecomputeTierProgressForTier(ctx context.Context, userID uuid.UUID, tier int16) (storage.TierProgress, bool, error) {
	f.gotUserID = userID
	f.gotTier = tier
	if f.recomputeErr != nil {
		return storage.TierProgress{}, false, f.recomputeErr
	}
	return f.recomputed, f.recomputeOK, nil
}

func TestServiceAllowedTiers(t *testing.T) {
	store := &fakeTierStore{progress: []storage.TierProgress{tp(0, true)}}
	svc := NewService(store)

	got, err := svc.AllowedTiers(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("AllowedTiers() error = %v", err)
	}
	want := []int{0, 1, 2}
	if !equalInts(got, want) {
		t.Fatalf("AllowedTiers() = %v, want %v", got, want)
	}
}

func TestServiceAllowedTiersPropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	store := &fakeTierStore{progressErr: wantErr}
	svc := NewService(store)

	if _, err := svc.AllowedTiers(context.Background(), uuid.New()); !errors.Is(err, wantErr) {
		t.Fatalf("AllowedTiers() error = %v, want %v", err, wantErr)
	}
}

func TestServiceRecomputePassthrough(t *testing.T) {
	want := storage.TierProgress{Tier: 2, TotalItems: 10, GoodShapeItems: 8}
	store := &fakeTierStore{recomputed: want, recomputeOK: true}
	svc := NewService(store)

	userID := uuid.New()
	got, ok, err := svc.Recompute(context.Background(), userID, 2)
	if err != nil {
		t.Fatalf("Recompute() error = %v", err)
	}
	if !ok {
		t.Fatal("Recompute() ok = false, want true")
	}
	if got != want {
		t.Fatalf("Recompute() = %+v, want %+v", got, want)
	}
	if store.gotUserID != userID || store.gotTier != 2 {
		t.Fatalf("Recompute() forwarded (userID=%v, tier=%d), want (%v, 2)", store.gotUserID, store.gotTier, userID)
	}
}
