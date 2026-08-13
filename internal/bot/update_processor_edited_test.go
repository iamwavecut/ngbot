package bot

import (
	"context"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
)

type updateHandlerFunc func(context.Context, *api.Update, *api.Chat, *api.User) (bool, error)

func (f updateHandlerFunc) Handle(ctx context.Context, update *api.Update, chat *api.Chat, user *api.User) (bool, error) {
	return f(ctx, update, chat, user)
}

func TestUpdateProcessorUsesEditDateForFreshness(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		editDate time.Time
		wantCall bool
		wantErr  bool
	}{
		{name: "fresh edit of old message", editDate: time.Now(), wantCall: true},
		{name: "stale edit", editDate: time.Now().Add(-UpdateTimeout - time.Minute), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			calls := 0
			processor := NewUpdateProcessor(nil, updateHandlerFunc(func(context.Context, *api.Update, *api.Chat, *api.User) (bool, error) {
				calls++
				return true, nil
			}))
			chat := api.Chat{ID: -100, Type: "supergroup"}
			user := &api.User{ID: 200}
			update := &api.Update{
				UpdateID: 300,
				EditedMessage: &api.Message{
					MessageID: 400,
					Chat:      chat,
					From:      user,
					Date:      time.Now().Add(-time.Hour).Unix(),
					EditDate:  test.editDate.Unix(),
					Text:      "edited text",
				},
			}

			err := processor.Process(t.Context(), update)
			if got := err != nil; got != test.wantErr {
				t.Fatalf("process edited update error = %v, want error=%t", err, test.wantErr)
			}
			if err != nil {
				failure := ClassifyUpdateFailure(err)
				if failure.Disposition != UpdateFailureTerminal || failure.Reason != "stale_security_update" {
					t.Fatalf("stale edit failure = %#v", failure)
				}
			}
			if got := calls == 1; got != test.wantCall {
				t.Fatalf("handler called = %t, want %t", got, test.wantCall)
			}
		})
	}
}

func TestUpdateProcessorUsesChannelPostEditDateForFreshness(t *testing.T) {
	t.Parallel()

	calls := 0
	processor := NewUpdateProcessor(nil, updateHandlerFunc(func(context.Context, *api.Update, *api.Chat, *api.User) (bool, error) {
		calls++
		return true, nil
	}))
	update := &api.Update{
		UpdateID: 301,
		EditedChannelPost: &api.Message{
			MessageID: 401,
			Chat:      api.Chat{ID: -101, Type: "channel"},
			Date:      time.Now().Add(-24 * time.Hour).Unix(),
			EditDate:  time.Now().Unix(),
			Caption:   "freshly edited channel post",
		},
	}

	if err := processor.Process(t.Context(), update); err != nil {
		t.Fatalf("process edited channel post: %v", err)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
}

func TestUpdateProcessorDeadLettersStaleTimestampedSecurityUpdates(t *testing.T) {
	t.Parallel()

	stale := time.Now().Add(-UpdateTimeout - time.Minute).Unix()
	chat := api.Chat{ID: -100, Type: "supergroup"}
	user := api.User{ID: 200}
	tests := []struct {
		name   string
		update *api.Update
	}{
		{name: "my chat member", update: &api.Update{MyChatMember: &api.ChatMemberUpdated{Chat: chat, From: user, Date: stale}}},
		{name: "chat member", update: &api.Update{ChatMember: &api.ChatMemberUpdated{Chat: chat, From: user, Date: stale}}},
		{name: "chat join request", update: &api.Update{ChatJoinRequest: &api.ChatJoinRequest{Chat: chat, From: user, Date: stale}}},
		{name: "message reaction", update: &api.Update{MessageReaction: &api.MessageReactionUpdated{Chat: chat, User: &user, Date: stale}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			processor := NewUpdateProcessor(nil, updateHandlerFunc(func(context.Context, *api.Update, *api.Chat, *api.User) (bool, error) {
				calls++
				return true, nil
			}))
			test.update.UpdateID = 302
			err := processor.Process(t.Context(), test.update)
			failure := ClassifyUpdateFailure(err)
			if failure.Disposition != UpdateFailureTerminal || failure.Reason != "stale_security_update" {
				t.Fatalf("failure = %#v, want stale security terminal", failure)
			}
			if calls != 0 {
				t.Fatalf("handler calls = %d, want 0", calls)
			}
		})
	}
}

func TestUpdateProcessorDoesNotUseCallbackMessageDateAsEventTime(t *testing.T) {
	t.Parallel()

	calls := 0
	processor := NewUpdateProcessor(nil, updateHandlerFunc(func(context.Context, *api.Update, *api.Chat, *api.User) (bool, error) {
		calls++
		return true, nil
	}))
	update := &api.Update{UpdateID: 303, CallbackQuery: &api.CallbackQuery{
		ID: "callback", From: &api.User{ID: 200},
		Message: &api.Message{MessageID: 10, Date: time.Now().Add(-24 * time.Hour).Unix(), Chat: api.Chat{ID: -100, Type: "supergroup"}},
	}}
	if err := processor.Process(t.Context(), update); err != nil {
		t.Fatalf("process callback: %v", err)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
}
