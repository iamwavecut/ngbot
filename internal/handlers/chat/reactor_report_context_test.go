package handlers

import (
	"context"
	"errors"
	"testing"

	api "github.com/OvyFlash/telegram-bot-api"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
)

func TestReportSnapshotCannotOverwriteSameSecondAuthoritativeEdit(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t)
	target := f.message(1)
	target.EditDate = target.Date
	target.Text = "authoritative edit"
	ctx := withMessageContextUpdate(t.Context(), &api.Update{UpdateID: 101})
	if err := f.reactor.rememberMessageContext(ctx, target, f.chat, f.settings); err != nil {
		t.Fatal(err)
	}
	target.Text = "older reply snapshot"
	report := f.message(2)
	report.ReplyToMessage = target
	report.Text = "/voteban"
	stop := errors.New("report processing reached")
	f.reactor.processReported = func(context.Context, *api.Message, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
		return nil, stop
	}
	ctx = withMessageContextUpdate(t.Context(), &api.Update{UpdateID: 110})
	if err := f.reactor.voteBanCommand(ctx, report, f.chat, f.user, f.settings); !errors.Is(err, stop) {
		t.Fatalf("report flow: %v", err)
	}
	saved, err := f.store.MessageContext(t.Context(), f.chat.ID, target.MessageID)
	if err != nil || saved == nil || saved.Text != "authoritative edit" || saved.UpdateID != 101 {
		t.Fatalf("report overwrote authoritative context: %#v error=%v", saved, err)
	}
}
