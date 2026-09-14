package handlers

import (
	"errors"
	"net/http"
	"testing"
	"time"

	botservice "github.com/iamwavecut/ngbot/internal/bot"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
	handlersbase "github.com/iamwavecut/ngbot/internal/handlers/base"
)

func TestAuthorTrustChecksThreeOfHundredMessagesAndRenewsAfterThirtyDays(t *testing.T) {
	for _, senderChat := range []bool{false, true} {
		name := db.MessageAuthorUser
		if senderChat {
			name = db.MessageAuthorSenderChat
		}
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
			client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "trust.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			settings := db.DefaultSettings(-100)
			if err := client.SetSettings(t.Context(), settings); err != nil {
				t.Fatal(err)
			}
			memberLookups := 0
			botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
				switch method {
				case testTelegramMethodGetChatMember:
					memberLookups++
					return testChatMemberResponse("left", false, false, false)
				case testTelegramMethodGetChat:
					return map[string]any{"id": -100, testJSONType: testChatTypeSupergroup, testJSONLinkedChatID: -999}
				default:
					t.Fatalf("unexpected method %s", method)
					return nil
				}
			})
			detector := &testSpamDetector{result: boolPtr(false)}
			reactor := &Reactor{
				s: &testBotService{botAPI: botAPI, settings: settings}, bot: botAPI, store: client, stats: client,
				spamDetector: detector, banService: &testBanService{}, now: func() time.Time { return now },
			}
			chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
			user := &api.User{ID: 200, FirstName: "Commenter"}
			message := &api.Message{Chat: *chat, From: user, Text: "A safe contribution to the discussion"}
			if senderChat {
				message.SenderChat = &api.Chat{ID: -200, Type: testChatTypeChannel, Title: "Commenting channel"}
			}
			for id := 1; id <= 100; id++ {
				message.MessageID = id
				message.Date = now.Unix()
				if err := reactor.handleMessage(t.Context(), message, chat, user, settings); err != nil {
					t.Fatalf("message %d: %v", id, err)
				}
			}
			if detector.calls != 3 {
				t.Fatalf("100 safe messages made %d LLM calls, want 3", detector.calls)
			}
			if memberLookups > 4 {
				t.Fatalf("trusted author repeated membership lookups: %d", memberLookups)
			}
			summary, err := handlersbase.LoadStatsSummary(t.Context(), client, chat.ID, now, 1)
			if err != nil || summary.AuthorTrustSkipped != 97 || summary.AuthorCheckInitial != 3 || summary.AuthorTrustGranted != 1 {
				t.Fatalf("trust statistics=%#v error=%v", summary, err)
			}
			now = now.Add(30 * 24 * time.Hour)
			message.MessageID = 101
			message.Date = now.Unix()
			if err := reactor.handleMessage(t.Context(), message, chat, user, settings); err != nil {
				t.Fatal(err)
			}
			message.MessageID++
			if err := reactor.handleMessage(t.Context(), message, chat, user, settings); err != nil {
				t.Fatal(err)
			}
			if detector.calls != 4 {
				t.Fatalf("renewal made %d total LLM calls, want 4", detector.calls)
			}
		})
	}
}

func TestExhaustedSenderChatFailureNeverQuarantinesTechnicalUser(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t)
	message := f.message(1)
	message.SenderChat = &api.Chat{ID: -200, Type: testChatTypeChannel}
	failure := botservice.ClassifyUpdateFailure(botservice.NewRetryableUpdateFailure(botservice.UpdateFailureLLM, "provider", errors.New("unavailable")))
	if err := f.reactor.HandleExhaustedUpdateFailure(t.Context(), &api.Update{Message: message}, f.chat, f.user, failure); err != nil {
		t.Fatal(err)
	}
	service := f.reactor.banService.(*testBanService)
	if service.muteCalls != 0 || len(service.bans) != 0 {
		t.Fatal("technical sender was quarantined")
	}
}

func TestCaptionCommandsCannotGrantTrust(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t)
	for id := 1; id <= 3; id++ {
		message := f.message(id)
		message.Text = ""
		message.Caption = testNoOpCommand
		message.CaptionEntities = []api.MessageEntity{{Type: testEntityBotCommand, Offset: 0, Length: 5}}
		f.handle(t, message, false)
	}
	if trust := f.trust(t); trust.Trusted(f.now) || trust.SafeMessages != 0 {
		t.Fatalf("caption command advanced trust: %#v", trust)
	}
}

func TestRichCommandsAndBotMentionsNeverAdvanceOrRenewTrust(t *testing.T) {
	t.Parallel()
	self := api.User{ID: 999, UserName: "ngbot"}
	for _, tt := range []struct {
		name string
		text api.RichText
	}{
		{"command", api.RichTextBotCommand{Type: testEntityBotCommand, BotCommand: testNoOpCommand}},
		{testEntityMention, api.RichTextMention{Type: testEntityMention, Username: "NgBot"}},
		{"text mention", api.RichTextTextMention{Type: "text_mention", Text: "bot", User: self}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newTrustFixture(t)
			f.reactor.bot.Self = self
			for phase := range 2 {
				for id := 1; id <= 3; id++ {
					message := f.message(id + phase*100)
					message.Text = ""
					message.RichMessage = &api.RichMessage{Blocks: []api.RichBlock{api.RichBlockParagraph{Type: testRichBlockParagraph, Text: tt.text}}}
					f.handle(t, message, false)
				}
				trust := f.trust(t)
				if trust.Trusted(f.now) || (phase == 0 && trust.SafeMessages != 0) {
					t.Fatalf("rich control advanced or renewed trust: %#v", trust)
				}
				if phase == 0 {
					for id := 10; id < 13; id++ {
						f.handle(t, f.message(id), false)
					}
					f.now = f.trust(t).TrustedUntil.Time
				}
			}
		})
	}
}
