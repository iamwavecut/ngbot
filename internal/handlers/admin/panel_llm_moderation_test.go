package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
	"github.com/iamwavecut/ngbot/internal/i18n"
)

func TestApplyPanelCommandSelectsJobsHRProfile(t *testing.T) {
	t.Parallel()

	client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	settings := db.DefaultSettings(42)
	if err := client.SetSettings(t.Context(), settings); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	session, err := client.CreateAdminPanelSession(t.Context(), &db.AdminPanelSession{
		UserID: 7, ChatID: 42, Page: string(panelPageLLMModerationProfile), StateJSON: "{}", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	admin := &Admin{s: testAdminService{db: client}, store: client}
	state := newPanelState(7, 42, "HR chat", settings)
	state.Page = panelPageLLMModerationProfile

	err = admin.applyPanelCommand(t.Context(), session, &state, panelCommand{
		Action: panelActionSetLLMModerationProfile,
		Value:  db.LLMModerationProfileJobsHR,
	})
	if err != nil {
		t.Fatalf("select Jobs & HR profile: %v", err)
	}
	stored, err := client.GetSettings(t.Context(), 42)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if stored.LLMModerationProfile != db.LLMModerationProfileJobsHR || state.LLMModerationProfile != db.LLMModerationProfileJobsHR {
		t.Fatalf("profile was not persisted and synchronized: stored=%q state=%q", stored.LLMModerationProfile, state.LLMModerationProfile)
	}
}

func TestApplyPanelCommandOpensAllowedExamples(t *testing.T) {
	t.Parallel()

	client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	settings := db.DefaultSettings(42)
	if err := client.SetSettings(t.Context(), settings); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	session, err := client.CreateAdminPanelSession(t.Context(), &db.AdminPanelSession{
		UserID: 7, ChatID: 42, Page: string(panelPageLLM), StateJSON: "{}", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	admin := &Admin{s: testAdminService{db: client}, store: client}
	state := newPanelState(7, 42, "HR chat", settings)

	if err := admin.applyPanelCommand(t.Context(), session, &state, panelCommand{Action: panelActionOpenAllowedExamples}); err != nil {
		t.Fatalf("open allowed examples: %v", err)
	}
	if state.Page != panelPageExamplesList || state.exampleClassification() != db.SpamClassificationAllowed {
		t.Fatalf("allowed examples state = page:%q classification:%d", state.Page, state.exampleClassification())
	}
}

func TestRenderExamplesListFiltersSelectedClassification(t *testing.T) {
	i18n.Init()

	client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	settings := db.DefaultSettings(42)
	settings.Language = "en"
	if err := client.SetSettings(t.Context(), settings); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	for _, example := range []*db.ChatSpamExample{
		{ChatID: 42, Text: "Detailed project manager vacancy", Classification: db.SpamClassificationAllowed, CreatedByUserID: 7, CreatedAt: time.Unix(1, 0)},
		{ChatID: 42, Text: "Vague remote income offer", Classification: db.SpamClassificationSpam, CreatedByUserID: 7, CreatedAt: time.Unix(2, 0)},
	} {
		if _, err := client.CreateChatSpamExample(t.Context(), example); err != nil {
			t.Fatalf("create example: %v", err)
		}
	}
	session, err := client.CreateAdminPanelSession(t.Context(), &db.AdminPanelSession{
		UserID: 7, ChatID: 42, Page: string(panelPageExamplesList), StateJSON: "{}", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	admin := &Admin{s: testAdminService{db: client}, store: client}
	state := newPanelState(7, 42, "HR chat", settings)
	state.Page = panelPageExamplesList
	state.ExampleKind = panelExampleKindAllowed

	text, _, err := admin.renderExamplesList(t.Context(), session, &state)
	if err != nil {
		t.Fatalf("render allowed examples: %v", err)
	}
	if !strings.Contains(text, "Detailed project manager vacancy") || strings.Contains(text, "Vague remote income offer") {
		t.Fatalf("allowed examples page did not filter by classification: %q", text)
	}
}
