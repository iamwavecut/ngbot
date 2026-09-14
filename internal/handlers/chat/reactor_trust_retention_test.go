package handlers

import (
	"context"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
)

func TestCheckedEditsRemainProtectedAcrossRetentionAndTrustRenewal(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{db.MessageAuthorUser, db.MessageAuthorSenderChat} {
		for _, renew := range []bool{false, true} {
			phase := map[bool]string{false: "initial", true: "renewal"}[renew]
			t.Run(kind+"/"+phase, func(t *testing.T) {
				f := newTrustFixture(t)
				start := f.now
				message := func(id int) *api.Message {
					msg := f.message(id)
					if kind == db.MessageAuthorSenderChat {
						msg.SenderChat = &api.Chat{ID: -300, Type: testChatTypeChannel}
					}
					return msg
				}
				for id := 1; id <= 3; id++ {
					f.now = start.Add(time.Duration(id-1) * 24 * time.Hour)
					f.handle(t, message(id), false)
				}
				f.now = start.Add(30*24*time.Hour + time.Hour)
				if renew {
					f.now = start.Add(40 * 24 * time.Hour)
				}
				cleaner := f.store.(interface {
					CleanupRetainedRecords(context.Context, time.Time, int) error
				})
				if err := cleaner.CleanupRetainedRecords(t.Context(), f.now, 500); err != nil {
					t.Fatal(err)
				}
				if renew {
					f.handle(t, message(4), false)
				}
				calls := f.detector.calls
				f.detector.result = boolPtr(true)
				edit := message(1)
				edit.Date = start.Unix()
				edit.EditDate = f.now.Unix()
				edit.Text = "spam inserted into an old checked message"
				f.handle(t, edit, true)
				if f.detector.calls != calls+1 || f.spam != 1 {
					t.Fatalf("retention removed edit protection: calls %d -> %d, spam actions=%d", calls, f.detector.calls, f.spam)
				}
			})
		}
	}
}
