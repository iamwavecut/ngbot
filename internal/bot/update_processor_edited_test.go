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
	}{
		{name: "fresh edit of old message", editDate: time.Now(), wantCall: true},
		{name: "stale edit", editDate: time.Now().Add(-UpdateTimeout - time.Minute)},
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

			if err := processor.Process(t.Context(), update); err != nil {
				t.Fatalf("process edited update: %v", err)
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
