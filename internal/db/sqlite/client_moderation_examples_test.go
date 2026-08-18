package sqlite

import (
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestCommitSettingsPersistsAndNormalizesLLMModerationProfile(t *testing.T) {
	t.Parallel()

	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	settings := db.DefaultSettings(-100)
	settings.LLMModerationProfile = db.LLMModerationProfileJobsHR
	if err := client.SetSettings(t.Context(), settings); err != nil {
		t.Fatalf("set Jobs & HR profile: %v", err)
	}
	stored, err := client.GetSettings(t.Context(), settings.ID)
	if err != nil {
		t.Fatalf("get Jobs & HR profile: %v", err)
	}
	if stored.LLMModerationProfile != db.LLMModerationProfileJobsHR {
		t.Fatalf("stored profile = %q, want %q", stored.LLMModerationProfile, db.LLMModerationProfileJobsHR)
	}

	settings.LLMModerationProfile = "unknown"
	if err := client.SetSettings(t.Context(), settings); err != nil {
		t.Fatalf("normalize unknown profile: %v", err)
	}
	stored, err = client.GetSettings(t.Context(), settings.ID)
	if err != nil {
		t.Fatalf("get normalized profile: %v", err)
	}
	if stored.LLMModerationProfile != db.LLMModerationProfileGeneral {
		t.Fatalf("normalized profile = %q, want %q", stored.LLMModerationProfile, db.LLMModerationProfileGeneral)
	}
}

func TestChatModerationExamplesAreFilteredByClassification(t *testing.T) {
	t.Parallel()

	client, err := NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	const chatID = int64(-100)
	for _, example := range []*db.ChatSpamExample{
		{ChatID: chatID, Text: "Detailed project manager vacancy", Classification: db.SpamClassificationAllowed, CreatedByUserID: 1, CreatedAt: time.Unix(1, 0)},
		{ChatID: chatID, Text: "Vague remote income offer", Classification: db.SpamClassificationSpam, CreatedByUserID: 1, CreatedAt: time.Unix(2, 0)},
	} {
		if _, err := client.CreateChatSpamExample(t.Context(), example); err != nil {
			t.Fatalf("create classification %d example: %v", example.Classification, err)
		}
	}

	allowed, err := client.ListChatSpamExamples(t.Context(), chatID, db.SpamClassificationAllowed, 20, 0)
	if err != nil {
		t.Fatalf("list allowed examples: %v", err)
	}
	if len(allowed) != 1 || allowed[0].Text != "Detailed project manager vacancy" || allowed[0].Classification != db.SpamClassificationAllowed {
		t.Fatalf("allowed examples = %#v", allowed)
	}
	spam, err := client.ListChatSpamExamples(t.Context(), chatID, db.SpamClassificationSpam, 20, 0)
	if err != nil {
		t.Fatalf("list spam examples: %v", err)
	}
	if len(spam) != 1 || spam[0].Text != "Vague remote income offer" || spam[0].Classification != db.SpamClassificationSpam {
		t.Fatalf("spam examples = %#v", spam)
	}

	allowedCount, err := client.CountChatSpamExamples(t.Context(), chatID, db.SpamClassificationAllowed)
	if err != nil {
		t.Fatalf("count allowed examples: %v", err)
	}
	spamCount, err := client.CountChatSpamExamples(t.Context(), chatID, db.SpamClassificationSpam)
	if err != nil {
		t.Fatalf("count spam examples: %v", err)
	}
	if allowedCount != 1 || spamCount != 1 {
		t.Fatalf("classification counts = allowed:%d spam:%d, want 1 and 1", allowedCount, spamCount)
	}
}
