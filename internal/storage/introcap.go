package storage

import (
	"context"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

// introCapMin/introCapMax bound SetIntroCap (architecture §8 IntroCapStore:
// "clamp sane range"). The lower bound is 0, NOT 1: 0 means "unlimited"
// (engram.RemainingIntroBudget's convention, mirrored by users.daily_new_cap
// already meaning unlimited at 0) and internal/telegram/handlers.go's own
// minIntroCap/maxIntroCap constants (0/50, frozen — see that file's
// clamp(cur+delta, minIntroCap, maxIntroCap) call) already rely on 0 being a
// legal value the /settings −5/−1 buttons can reach. Clamping here to 1..50
// instead would silently turn a user's "unlimited" setting back into 1.
const (
	introCapMin = 0
	introCapMax = 50
)

// GetIntroCap returns userID's daily introduction budget (architecture
// §2.10 users.daily_intro_cap; 0 = unlimited). Implements
// internal/telegram.IntroCapStore.
func (s *Store) GetIntroCap(ctx context.Context, userID uuid.UUID) (int, error) {
	u, err := s.GetUserByID(ctx, userID)
	if err != nil {
		return 0, err
	}
	return u.DailyIntroCap, nil
}

// SetIntroCap sets userID's daily introduction budget, clamped to
// [introCapMin, introCapMax]. Implements internal/telegram.IntroCapStore.
func (s *Store) SetIntroCap(ctx context.Context, userID uuid.UUID, cap int) error {
	if cap < introCapMin {
		cap = introCapMin
	}
	if cap > introCapMax {
		cap = introCapMax
	}
	return s.q.SetIntroCap(ctx, db.SetIntroCapParams{ID: userID, DailyIntroCap: int32(cap)})
}
