package storage

import (
	"context"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage/db"
)

func topicFrom(t db.Topic) Topic {
	return Topic{
		ID:            t.ID,
		ParentID:      t.ParentID,
		Slug:          t.Slug,
		Name:          t.Name,
		Position:      int(t.Position),
		BaseTier:      t.BaseTier,
		QuizKind:      t.QuizKind,
		ExerciseModes: t.ExerciseModes,
		IsQuizzable:   t.IsQuizzable,
		Config:        t.Config,
		CreatedAt:     tsTime(t.CreatedAt),
	}
}

// UpsertTopic inserts or updates a topic by (parent_id, slug). parentID nil
// upserts a root topic (topics_root_slug); otherwise a child topic
// (topics_sibling_slug) — the two partial unique indexes require separate
// conflict targets (architecture §2.1).
func (s *Store) UpsertTopic(ctx context.Context, parentID *uuid.UUID, slug, name string, position int, baseTier int16, quizKind string, exerciseModes []string, isQuizzable bool, config []byte) (Topic, error) {
	if len(exerciseModes) == 0 {
		exerciseModes = []string{"single"}
	}
	if parentID == nil {
		t, err := s.q.UpsertRootTopic(ctx, db.UpsertRootTopicParams{
			Slug:          slug,
			Name:          name,
			Position:      int32(position),
			BaseTier:      baseTier,
			QuizKind:      quizKind,
			ExerciseModes: exerciseModes,
			IsQuizzable:   isQuizzable,
			Config:        config,
		})
		if err != nil {
			return Topic{}, err
		}
		return topicFrom(t), nil
	}
	t, err := s.q.UpsertChildTopic(ctx, db.UpsertChildTopicParams{
		ParentID:      parentID,
		Slug:          slug,
		Name:          name,
		Position:      int32(position),
		BaseTier:      baseTier,
		QuizKind:      quizKind,
		ExerciseModes: exerciseModes,
		IsQuizzable:   isQuizzable,
		Config:        config,
	})
	if err != nil {
		return Topic{}, err
	}
	return topicFrom(t), nil
}

// GetTopicByID looks up a topic by primary key.
func (s *Store) GetTopicByID(ctx context.Context, id uuid.UUID) (Topic, bool, error) {
	t, err := s.q.GetTopicByID(ctx, id)
	if IsNotFound(err) {
		return Topic{}, false, nil
	}
	if err != nil {
		return Topic{}, false, err
	}
	return topicFrom(t), true, nil
}

// GetTopicByPath resolves a topic by its full slash-joined slug path (e.g.
// "languages/special-characters"), via the recursive topic_paths view — the
// path-walk helper for canonical parent_id+slug storage (architecture §2.1).
func (s *Store) GetTopicByPath(ctx context.Context, path string) (Topic, bool, error) {
	t, err := s.q.GetTopicByPath(ctx, path)
	if IsNotFound(err) {
		return Topic{}, false, nil
	}
	if err != nil {
		return Topic{}, false, err
	}
	return topicFrom(t), true, nil
}

// GetTopicPath returns the slash-joined slug path and depth for one topic id.
func (s *Store) GetTopicPath(ctx context.Context, id uuid.UUID) (path string, depth int, found bool, err error) {
	tp, err := s.q.GetTopicPath(ctx, id)
	if IsNotFound(err) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	return tp.Path, int(tp.Depth), true, nil
}

// ListRootTopics returns every root topic (parent_id IS NULL).
func (s *Store) ListRootTopics(ctx context.Context) ([]Topic, error) {
	rows, err := s.q.ListRootTopics(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Topic, len(rows))
	for i, r := range rows {
		out[i] = topicFrom(r)
	}
	return out, nil
}

// ListChildTopics returns the direct children of a topic.
func (s *Store) ListChildTopics(ctx context.Context, parentID uuid.UUID) ([]Topic, error) {
	rows, err := s.q.ListChildTopics(ctx, &parentID)
	if err != nil {
		return nil, err
	}
	out := make([]Topic, len(rows))
	for i, r := range rows {
		out[i] = topicFrom(r)
	}
	return out, nil
}

// ListAllTopics returns every topic in the tree.
func (s *Store) ListAllTopics(ctx context.Context) ([]Topic, error) {
	rows, err := s.q.ListAllTopics(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Topic, len(rows))
	for i, r := range rows {
		out[i] = topicFrom(r)
	}
	return out, nil
}

// ReparentTopic moves a topic under a new parent (nil = becomes root) with a
// single-row UPDATE (architecture §2.1: "Aurora will rearrange it later").
func (s *Store) ReparentTopic(ctx context.Context, topicID uuid.UUID, newParentID *uuid.UUID) error {
	return s.q.ReparentTopic(ctx, db.ReparentTopicParams{ID: topicID, ParentID: newParentID})
}

// ── user_topics (per-user topic opt-in/out, architecture §2.10) ────────────

// SetUserTopicEnabled toggles a topic for a user.
func (s *Store) SetUserTopicEnabled(ctx context.Context, userID, topicID uuid.UUID, enabled bool) error {
	return s.q.SetUserTopicEnabled(ctx, db.SetUserTopicEnabledParams{UserID: userID, TopicID: topicID, Enabled: enabled})
}

// GetUserTopicEnabled returns a single topic's enabled flag for a user
// (default-on when no user_topics row exists) — the /topics enable/disable
// toggle's current-state read, without listing every topic (ListUserTopics).
func (s *Store) GetUserTopicEnabled(ctx context.Context, userID, topicID uuid.UUID) (bool, error) {
	return s.q.GetUserTopicEnabled(ctx, db.GetUserTopicEnabledParams{UserID: userID, ID: topicID})
}

// SubtreeTopicCounts is the aggregate enabled-vs-total count over every
// QUIZZABLE topic in a subtree (architecture: the /topics group-level
// "Turn group off/on" toggle) — Enabled == Total means every descendant is
// currently on, Enabled == 0 means every descendant is off, anything else
// is mixed.
type SubtreeTopicCounts struct {
	Total   int
	Enabled int
}

// GetSubtreeQuizzableTopicCounts aggregates enabled-vs-total counts across
// every quizzable topic in topicID's subtree (itself + descendants, via the
// topic_paths view) for a user — feeds the group-level toggle's tri-state
// read without an N+1 per-topic walk.
func (s *Store) GetSubtreeQuizzableTopicCounts(ctx context.Context, userID, topicID uuid.UUID) (SubtreeTopicCounts, error) {
	r, err := s.q.GetSubtreeQuizzableTopicCounts(ctx, db.GetSubtreeQuizzableTopicCountsParams{UserID: userID, ID: topicID})
	if err != nil {
		return SubtreeTopicCounts{}, err
	}
	return SubtreeTopicCounts{Total: int(r.Total), Enabled: int(r.Enabled)}, nil
}

// SetSubtreeTopicsEnabled upserts user_topics.enabled for every quizzable
// topic in topicID's subtree (itself + descendants, via topic_paths) in one
// set-based statement — the group-level toggle's write side (architecture:
// turning off/on a whole container without touching every subtopic by
// hand). Idempotent, like SetUserTopicEnabled.
func (s *Store) SetSubtreeTopicsEnabled(ctx context.Context, userID, topicID uuid.UUID, enabled bool) error {
	return s.q.SetSubtreeTopicsEnabled(ctx, db.SetSubtreeTopicsEnabledParams{UserID: userID, ID: topicID, Enabled: enabled})
}

// ListUserTopics returns every topic with the user's enabled flag
// (default-on when no user_topics row exists).
func (s *Store) ListUserTopics(ctx context.Context, userID uuid.UUID) ([]UserTopic, error) {
	rows, err := s.q.ListUserTopics(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]UserTopic, len(rows))
	for i, r := range rows {
		out[i] = UserTopic{
			Topic: Topic{
				ID:            r.ID,
				ParentID:      r.ParentID,
				Slug:          r.Slug,
				Name:          r.Name,
				Position:      int(r.Position),
				BaseTier:      r.BaseTier,
				QuizKind:      r.QuizKind,
				ExerciseModes: r.ExerciseModes,
				IsQuizzable:   r.IsQuizzable,
				Config:        r.Config,
				CreatedAt:     tsTime(r.CreatedAt),
			},
			Enabled: r.Enabled,
		}
	}
	return out, nil
}
