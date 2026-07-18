package study

import (
	"testing"

	"github.com/google/uuid"

	"github.com/supercakecrumb/geodrill/internal/storage"
)

func TestEnabledQuizzableTopicIDs(t *testing.T) {
	enabledQuizzable := storage.UserTopic{Topic: storage.Topic{ID: uuid.New(), IsQuizzable: true}, Enabled: true}
	disabledQuizzable := storage.UserTopic{Topic: storage.Topic{ID: uuid.New(), IsQuizzable: true}, Enabled: false}
	enabledContainer := storage.UserTopic{Topic: storage.Topic{ID: uuid.New(), IsQuizzable: false}, Enabled: true}

	got := enabledQuizzableTopicIDs([]storage.UserTopic{enabledQuizzable, disabledQuizzable, enabledContainer})
	if len(got) != 1 || got[0] != enabledQuizzable.ID {
		t.Fatalf("expected only the enabled quizzable topic's id, got %v", got)
	}

	if got := enabledQuizzableTopicIDs(nil); got != nil {
		t.Fatalf("expected nil for no topics, got %v", got)
	}
	if got := enabledQuizzableTopicIDs([]storage.UserTopic{disabledQuizzable}); len(got) != 0 {
		t.Fatalf("expected no ids when nothing is enabled+quizzable, got %v", got)
	}
}
