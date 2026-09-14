package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
)

type trustFixture struct {
	reactor  *Reactor
	store    reactorStore
	service  *testBotService
	detector *testSpamDetector
	chat     *api.Chat
	user     *api.User
	settings *db.Settings
	now      time.Time
	spam     int
}

func newTrustFixture(t *testing.T) *trustFixture {
	t.Helper()
	f := &trustFixture{now: time.Now().UTC().Truncate(time.Second), chat: &api.Chat{ID: -100, Type: testChatTypeGroup}, user: &api.User{ID: 200, FirstName: "User"}, detector: &testSpamDetector{result: boolPtr(false)}}
	client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "trust.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	f.store = client
	f.settings = db.DefaultSettings(f.chat.ID)
	if err := client.SetSettings(t.Context(), f.settings); err != nil {
		t.Fatal(err)
	}
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		switch method {
		case testTelegramMethodGetChatMember:
			return testChatMemberResponse(telegramMemberStatus, false, false, false)
		case testTelegramMethodGetChat:
			return map[string]any{"id": -100, testJSONType: testChatTypeSupergroup, testJSONLinkedChatID: -999}
		case testTelegramMethodSendMessage:
			return map[string]any{"message_id": 900, "date": f.now.Unix(), "chat": map[string]any{"id": -100, testJSONType: testChatTypeGroup}}
		default:
			t.Fatalf("unexpected method %s", method)
			return nil
		}
	})
	f.service = &testBotService{botAPI: botAPI, settings: f.settings}
	f.reactor = &Reactor{s: f.service, bot: botAPI, store: client, spamDetector: f.detector, banService: &testBanService{}, now: func() time.Time { return f.now }}
	process := func(ctx context.Context, msg *api.Message, chat *api.Chat, _ string) (*moderation.ProcessingResult, error) {
		f.spam++
		author := messageAuthorForTest(msg)
		if err := client.ResetMessageTrust(ctx, chat.ID, author); err != nil {
			return nil, err
		}
		return &moderation.ProcessingResult{MessageDeleted: true, UserBanned: true}, nil
	}
	f.reactor.processSpam, f.reactor.processBanned = process, process
	return f
}

func messageAuthorForTest(msg *api.Message) db.MessageAuthor {
	if msg.SenderChat != nil {
		return db.MessageAuthor{Kind: db.MessageAuthorSenderChat, ID: msg.SenderChat.ID}
	}
	return db.MessageAuthor{Kind: db.MessageAuthorUser, ID: msg.From.ID}
}

func (f *trustFixture) message(id int) *api.Message {
	return &api.Message{MessageID: id, Date: f.now.Unix(), Chat: *f.chat, From: f.user, Text: "safe contribution"}
}

func (f *trustFixture) handle(t *testing.T, msg *api.Message, edited bool) {
	t.Helper()
	update := &api.Update{Message: msg}
	if edited {
		update = &api.Update{EditedMessage: msg}
	}
	if _, err := f.reactor.Handle(t.Context(), update, &msg.Chat, msg.From); err != nil {
		t.Fatal(err)
	}
}

func (f *trustFixture) trust(t *testing.T) *db.MessageTrust {
	t.Helper()
	value, err := f.store.MessageTrust(t.Context(), f.chat.ID, db.MessageAuthor{Kind: db.MessageAuthorUser, ID: f.user.ID})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestAuthorAdmissionRejectsDuplicateAndStaysPerChat(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t)
	first := f.message(1)
	f.handle(t, first, false)
	f.handle(t, first, false)
	if value := f.trust(t); value.SafeMessages != 1 || value.Trusted(f.now) {
		t.Fatalf("duplicate advanced trust: %#v", value)
	}
	f.handle(t, f.message(2), false)
	f.handle(t, f.message(3), false)
	if !f.trust(t).Trusted(f.now) || f.detector.calls != 3 {
		t.Fatal("three distinct messages did not grant trust")
	}
	other := f.message(1)
	other.Chat.ID = -101
	if err := f.store.(interface {
		SetSettings(context.Context, *db.Settings) error
	}).SetSettings(t.Context(), db.DefaultSettings(-101)); err != nil {
		t.Fatal(err)
	}
	f.handle(t, other, false)
	if f.detector.calls != 4 {
		t.Fatal("trust escaped chat scope")
	}
}

func TestSpamAmongInitialMessagesResetsAdmission(t *testing.T) {
	t.Parallel()
	for _, id := range []int{1, 2, 3} {
		t.Run(string(rune('0'+id)), func(t *testing.T) {
			f := newTrustFixture(t)
			for i := 1; i < id; i++ {
				f.handle(t, f.message(i), false)
			}
			f.detector.result = boolPtr(true)
			f.handle(t, f.message(id), false)
			value := f.trust(t)
			if f.spam != 1 || value.SafeMessages != 0 || value.TrustedUntil.Valid {
				t.Fatalf("spam admission: %#v, actions=%d", value, f.spam)
			}
		})
	}
}

type failingTrustStore struct {
	reactorStore
	recordErr error
}

func (s failingTrustStore) RecordSafeAuthorMessage(ctx context.Context, chatID int64, author db.MessageAuthor, id int, now time.Time, required int, duration time.Duration, eligible bool) (*db.MessageTrust, bool, error) {
	if s.recordErr != nil {
		return nil, false, s.recordErr
	}
	return s.reactorStore.RecordSafeAuthorMessage(ctx, chatID, author, id, now, required, duration, eligible)
}

func TestAdmissionErrorsDoNotGrantTrustAndMembershipFailureDoesNotRevokeIt(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t)
	for _, failure := range []struct {
		result *bool
		err    error
	}{{nil, nil}, {nil, errors.New("provider unavailable")}} {
		f.detector.result, f.detector.err = failure.result, failure.err
		if err := f.reactor.handleMessage(t.Context(), f.message(1), f.chat, f.user, f.settings); err == nil {
			t.Fatal("missing classification must retry")
		}
	}
	if f.trust(t).SafeMessages != 0 {
		t.Fatal("LLM failure advanced admission")
	}
	f.detector.result, f.detector.err = boolPtr(false), nil
	f.handle(t, f.message(1), false)
	f.handle(t, f.message(2), false)
	f.reactor.store = failingTrustStore{reactorStore: f.store, recordErr: errors.New("sqlite failure")}
	if err := f.reactor.handleMessage(t.Context(), f.message(3), f.chat, f.user, f.settings); err == nil {
		t.Fatal("SQLite failure ignored")
	}
	if f.trust(t).Trusted(f.now) {
		t.Fatal("SQLite failure granted trust")
	}
	f.reactor.store = f.store
	f.service.insertMemberErr = errors.New("membership failure")
	f.handle(t, f.message(3), false)
	if !f.trust(t).Trusted(f.now) {
		t.Fatal("membership bookkeeping revoked successful trust")
	}
	f.handle(t, f.message(4), false)
	if f.detector.calls != 6 {
		t.Fatalf("LLM calls=%d", f.detector.calls)
	}
}

func TestAdmissionCommandsMentionsAndEmptyMediaNeverAdvanceOrRenew(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t)
	f.reactor.bot.Self = api.User{ID: 999, UserName: "ngbot"}
	excluded := []*api.Message{f.message(10), f.message(11), f.message(12)}
	excluded[0].Text = testNoOpCommand
	excluded[0].Entities = []api.MessageEntity{{Type: testEntityBotCommand, Length: 5}}
	excluded[1].Text = "@ngbot"
	excluded[1].Entities = []api.MessageEntity{{Type: testEntityMention, Length: 6}}
	excluded[2].Text = ""
	excluded[2].Photo = []api.PhotoSize{{FileID: "photo"}}
	for _, msg := range excluded {
		f.handle(t, msg, false)
	}
	if f.trust(t).SafeMessages != 0 {
		t.Fatal("routed or empty content advanced trust")
	}
	for id := 20; id < 23; id++ {
		f.handle(t, f.message(id), false)
	}
	deadline := f.trust(t).TrustedUntil.Time
	f.now = deadline
	for _, msg := range excluded {
		msg.MessageID += 100
		msg.Date = f.now.Unix()
		f.handle(t, msg, false)
	}
	if f.trust(t).TrustedUntil.Time != deadline {
		t.Fatal("excluded content renewed trust")
	}
	f.handle(t, f.message(200), false)
	if !f.trust(t).Trusted(f.now) {
		t.Fatal("safe new message failed to renew")
	}
}

func TestAdmissionEditsRemainProtectedWithoutAdvancing(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"caption", "rich"} {
		t.Run(kind, func(t *testing.T) {
			f := newTrustFixture(t)
			empty := f.message(1)
			empty.Text = ""
			empty.Photo = []api.PhotoSize{{FileID: "photo"}}
			f.handle(t, empty, false)
			edit := f.message(99)
			edit.EditDate = f.now.Unix()
			edit.Text = ""
			if kind == "caption" {
				edit.Caption = "safe caption"
			} else {
				edit.RichMessage = &api.RichMessage{Blocks: []api.RichBlock{api.RichBlockParagraph{Type: testRichBlockParagraph, Text: "safe rich text"}}}
			}
			f.handle(t, edit, true)
			if f.trust(t).SafeMessages != 0 {
				t.Fatal("unbound edit advanced admission")
			}
			for id := 2; id < 5; id++ {
				f.handle(t, f.message(id), false)
			}
			deadline := f.trust(t).TrustedUntil.Time
			unbound := f.message(100)
			f.handle(t, unbound, true)
			if f.detector.calls != 4 {
				t.Fatal("trusted unbound edit reached LLM")
			}
			bound := f.message(2)
			bound.EditDate = f.now.Add(time.Minute).Unix()
			f.handle(t, bound, true)
			if f.detector.calls != 5 || f.trust(t).TrustedUntil.Time != deadline {
				t.Fatal("bound edit not protected or renewed trust")
			}
			f.now = deadline
			f.handle(t, unbound, true)
			if f.detector.calls != 6 || f.trust(t).Trusted(f.now) {
				t.Fatal("expired trust edit bypassed check or renewed")
			}
		})
	}
}

func TestDisabledLLMDoesNotCreateAdmission(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t)
	f.settings.LLMFirstMessageEnabled = false
	f.handle(t, f.message(1), false)
	if f.trust(t) != nil || f.detector.calls != 0 {
		t.Fatal("disabled moderation created admission")
	}
}
