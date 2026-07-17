package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
)

func TestMessageProbationChecksEveryMessageUntilSafeExit(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	detector := &testSpamDetector{result: boolPtr(false)}
	store := &testReactorStore{}
	service := &testBotService{}
	processedSpam := 0
	reactor := newMessageProbationTestReactor(t, &now, service, store, detector, &processedSpam)
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, FirstName: testFirstNameUser}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}

	for messageID := 1; messageID <= 5; messageID++ {
		message := &api.Message{MessageID: messageID, Chat: *chat, From: user, Text: "safe message"}
		if err := reactor.handleMessage(t.Context(), message, chat, user, settings); err != nil {
			t.Fatalf("handle safe message %d: %v", messageID, err)
		}
	}
	if detector.calls != 5 || service.insertedMember != 0 {
		t.Fatalf("probation before deadline: checks=%d inserts=%d", detector.calls, service.insertedMember)
	}

	now = now.Add(3 * time.Hour)
	detector.result = boolPtr(true)
	spam := &api.Message{MessageID: 6, Chat: *chat, From: user, Text: "spam at deadline"}
	if err := reactor.handleMessage(t.Context(), spam, chat, user, settings); err != nil {
		t.Fatalf("handle spam at deadline: %v", err)
	}
	if detector.calls != 6 || processedSpam != 1 || service.insertedMember != 0 {
		t.Fatalf("deadline spam: checks=%d processed=%d inserts=%d", detector.calls, processedSpam, service.insertedMember)
	}
	probation, err := store.MessageProbation(t.Context(), chat.ID, user.ID)
	if err != nil || probation == nil || probation.GraduatedAt.Valid {
		t.Fatalf("spam graduated probation: probation=%#v err=%v", probation, err)
	}
}

func TestMessageProbationSafeExitRejectsDuplicateAndStaysPerChat(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	detector := &testSpamDetector{result: boolPtr(false)}
	store := &testReactorStore{}
	service := &testBotService{}
	processedSpam := 0
	reactor := newMessageProbationTestReactor(t, &now, service, store, detector, &processedSpam)
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	otherChat := &api.Chat{ID: -101, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, FirstName: testFirstNameUser}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}
	first := &api.Message{MessageID: 1, Chat: *chat, From: user, Text: "safe first message"}

	if err := reactor.handleMessage(t.Context(), first, chat, user, settings); err != nil {
		t.Fatalf("handle first message: %v", err)
	}
	now = now.Add(3 * time.Hour)
	if err := reactor.handleMessage(t.Context(), first, chat, user, settings); err != nil {
		t.Fatalf("handle duplicate after deadline: %v", err)
	}
	if service.insertedMember != 0 {
		t.Fatalf("duplicate update graduated author: %d inserts", service.insertedMember)
	}

	release := &api.Message{MessageID: 2, Chat: *chat, From: user, Text: "new safe release message"}
	if err := reactor.handleMessage(t.Context(), release, chat, user, settings); err != nil {
		t.Fatalf("handle release message: %v", err)
	}
	probation, err := store.MessageProbation(t.Context(), chat.ID, user.ID)
	if err != nil || probation == nil || !probation.GraduatedAt.Valid {
		t.Fatalf("safe message did not graduate probation: probation=%#v err=%v", probation, err)
	}
	if service.insertedMember != 1 {
		t.Fatalf("trusted state writes = %d, want 1", service.insertedMember)
	}

	otherMessage := &api.Message{MessageID: 1, Chat: *otherChat, From: user, Text: "first message elsewhere"}
	if err := reactor.handleMessage(t.Context(), otherMessage, otherChat, user, settings); err != nil {
		t.Fatalf("handle cross-chat message: %v", err)
	}
	otherProbation, err := store.MessageProbation(t.Context(), otherChat.ID, user.ID)
	if err != nil || otherProbation == nil || otherProbation.GraduatedAt.Valid {
		t.Fatalf("trust crossed chat boundary: probation=%#v err=%v", otherProbation, err)
	}
	if detector.calls != 4 {
		t.Fatalf("LLM checks = %d, want 4", detector.calls)
	}
}

func TestMessageProbationNilErrorAndPersistenceFailuresCannotGraduate(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	detector := &testSpamDetector{}
	store := &testReactorStore{}
	service := &testBotService{}
	processedSpam := 0
	reactor := newMessageProbationTestReactor(t, &now, service, store, detector, &processedSpam)
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, FirstName: testFirstNameUser}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}

	first := &api.Message{MessageID: 1, Chat: *chat, From: user, Text: "undecided"}
	if err := reactor.handleMessage(t.Context(), first, chat, user, settings); err != nil {
		t.Fatalf("handle nil decision: %v", err)
	}
	now = now.Add(3 * time.Hour)
	detector.err = errors.New("classifier unavailable")
	second := &api.Message{MessageID: 2, Chat: *chat, From: user, Text: "classification error"}
	if err := reactor.handleMessage(t.Context(), second, chat, user, settings); err == nil {
		t.Fatal("expected classifier error")
	}
	probation, err := store.MessageProbation(t.Context(), chat.ID, user.ID)
	if err != nil || probation == nil || probation.GraduatedAt.Valid {
		t.Fatalf("nil/error decision graduated probation: probation=%#v err=%v", probation, err)
	}

	detector.err = nil
	detector.result = boolPtr(false)
	service.insertMemberErr = errors.New("member persistence failed")
	third := &api.Message{MessageID: 3, Chat: *chat, From: user, Text: "safe but not persisted"}
	if err := reactor.handleMessage(t.Context(), third, chat, user, settings); err == nil {
		t.Fatal("expected member persistence error")
	}
	probation, _ = store.MessageProbation(t.Context(), chat.ID, user.ID)
	if probation.GraduatedAt.Valid {
		t.Fatal("failed trusted-state persistence graduated probation")
	}

	service.insertMemberErr = nil
	store.graduateError = errors.New("graduation persistence failed")
	fourth := &api.Message{MessageID: 4, Chat: *chat, From: user, Text: "safe with graduation failure"}
	if err := reactor.handleMessage(t.Context(), fourth, chat, user, settings); err == nil {
		t.Fatal("expected graduation persistence error")
	}
	service.isMember = true
	store.graduateError = nil
	fifth := &api.Message{MessageID: 5, Chat: *chat, From: user, Text: "safe retry after partial persistence"}
	if err := reactor.handleMessage(t.Context(), fifth, chat, user, settings); err != nil {
		t.Fatalf("retry graduation: %v", err)
	}
	probation, _ = store.MessageProbation(t.Context(), chat.ID, user.ID)
	if probation == nil || !probation.GraduatedAt.Valid {
		t.Fatalf("probation did not recover after persistence retry: %#v", probation)
	}
	if detector.calls != 5 {
		t.Fatalf("partial member state bypassed active probation: checks=%d, want 5", detector.calls)
	}
}

func TestMessageProbationCoversRichCaptionAndAllActiveEdits(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	detector := &testSpamDetector{result: boolPtr(false)}
	store := &testReactorStore{}
	service := &testBotService{}
	processedSpam := 0
	reactor := newMessageProbationTestReactor(t, &now, service, store, detector, &processedSpam)
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, FirstName: testFirstNameUser}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}
	service.settings = settings

	photo := []api.PhotoSize{{FileID: "photo", FileUniqueID: "unique"}}
	emptyRichMessages := []*api.Message{
		{MessageID: 1, Chat: *chat, From: user, Photo: photo},
		{MessageID: 2, Chat: *chat, From: user, Document: &api.Document{FileID: "document", FileUniqueID: "document-unique"}},
		{MessageID: 3, Chat: *chat, From: user, Video: &api.Video{FileID: "video", FileUniqueID: "video-unique"}},
	}
	for _, message := range emptyRichMessages {
		if _, err := reactor.Handle(t.Context(), &api.Update{Message: message}, chat, user); err != nil {
			t.Fatalf("handle empty rich message %d: %v", message.MessageID, err)
		}
	}
	if detector.calls != 0 {
		t.Fatalf("empty rich messages reached LLM: %d calls", detector.calls)
	}

	now = now.Add(3 * time.Hour)
	safeEdit := *emptyRichMessages[0]
	safeEdit.MessageID = 99
	safeEdit.Caption = "safe caption added by edit"
	safeEdit.EditDate = now.Unix()
	if _, err := reactor.Handle(t.Context(), &api.Update{EditedMessage: &safeEdit}, chat, user); err != nil {
		t.Fatalf("handle safe active edit: %v", err)
	}
	probation, _ := store.MessageProbation(t.Context(), chat.ID, user.ID)
	if probation == nil || probation.GraduatedAt.Valid || service.insertedMember != 0 {
		t.Fatalf("edit released probation: probation=%#v inserts=%d", probation, service.insertedMember)
	}

	detector.result = boolPtr(true)
	spamEdit := safeEdit
	spamEdit.MessageID++
	spamEdit.Caption = "spam caption added by edit"
	if _, err := reactor.Handle(t.Context(), &api.Update{EditedMessage: &spamEdit}, chat, user); err != nil {
		t.Fatalf("handle spam active edit: %v", err)
	}
	if detector.calls != 2 || processedSpam != 1 {
		t.Fatalf("rich edit moderation: checks=%d spam=%d", detector.calls, processedSpam)
	}

	detector.result = boolPtr(false)
	release := &api.Message{MessageID: 100, Chat: *chat, From: user, Caption: "safe new caption", Photo: photo}
	if _, err := reactor.Handle(t.Context(), &api.Update{Message: release}, chat, user); err != nil {
		t.Fatalf("handle rich release message: %v", err)
	}
	probation, _ = store.MessageProbation(t.Context(), chat.ID, user.ID)
	if probation == nil || !probation.GraduatedAt.Valid {
		t.Fatalf("new rich message did not release probation: %#v", probation)
	}
}

func TestMessageProbationChecksRichMessagePostsAndEdits(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	detector := &testSpamDetector{result: boolPtr(false)}
	store := &testReactorStore{}
	service := &testBotService{}
	processedSpam := 0
	reactor := newMessageProbationTestReactor(t, &now, service, store, detector, &processedSpam)
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, FirstName: testFirstNameUser}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}
	service.settings = settings

	post := &api.Message{
		MessageID: 1,
		Chat:      *chat,
		From:      user,
		RichMessage: &api.RichMessage{Blocks: []api.RichBlock{
			api.RichBlockParagraph{Type: "paragraph", Text: "safe rich post"},
		}},
	}
	if _, err := reactor.Handle(t.Context(), &api.Update{Message: post}, chat, user); err != nil {
		t.Fatalf("handle rich post: %v", err)
	}

	detector.result = boolPtr(true)
	now = now.Add(time.Minute)
	edit := *post
	edit.EditDate = now.Unix()
	edit.RichMessage = &api.RichMessage{Blocks: []api.RichBlock{
		api.RichBlockParagraph{Type: "paragraph", Text: "spam rich edit"},
	}}
	if _, err := reactor.Handle(t.Context(), &api.Update{EditedMessage: &edit}, chat, user); err != nil {
		t.Fatalf("handle rich edit: %v", err)
	}
	if detector.calls != 2 || processedSpam != 1 {
		t.Fatalf("rich post/edit checks=%d spam=%d, want checks=2 spam=1", detector.calls, processedSpam)
	}

	detector.result = boolPtr(false)
	now = now.Add(3 * time.Hour)
	release := *post
	release.MessageID = 2
	release.RichMessage = &api.RichMessage{Blocks: []api.RichBlock{
		api.RichBlockParagraph{Type: "paragraph", Text: "safe rich release"},
	}}
	if _, err := reactor.Handle(t.Context(), &api.Update{Message: &release}, chat, user); err != nil {
		t.Fatalf("handle rich release: %v", err)
	}
	probation, err := store.MessageProbation(t.Context(), chat.ID, user.ID)
	if err != nil || probation == nil || !probation.GraduatedAt.Valid {
		t.Fatalf("rich release probation=%#v err=%v", probation, err)
	}
}

func TestCommandsAndMentionsStartProbationWithoutClassification(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	detector := &testSpamDetector{result: boolPtr(false)}
	store := &testReactorStore{}
	settings := &db.Settings{LLMFirstMessageEnabled: true, CommunityVotingEnabled: true}
	service := &testBotService{settings: settings}
	processedSpam := 0
	reactor := newMessageProbationTestReactor(t, &now, service, store, detector, &processedSpam)
	reactor.bot.Self = api.User{ID: 999, UserName: "ngbot"}
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}

	commandUser := &api.User{ID: 200, FirstName: "Command"}
	command := &api.Message{
		MessageID: 1,
		Chat:      *chat,
		From:      commandUser,
		Text:      "/noop",
		Entities:  []api.MessageEntity{{Type: "bot_command", Offset: 0, Length: 5}},
	}
	if _, err := reactor.Handle(t.Context(), &api.Update{Message: command}, chat, commandUser); err != nil {
		t.Fatalf("handle command: %v", err)
	}

	mentionUser := &api.User{ID: 201, FirstName: "Mention"}
	mention := &api.Message{
		MessageID: 2,
		Chat:      *chat,
		From:      mentionUser,
		Text:      "@ngbot",
		Entities:  []api.MessageEntity{{Type: "mention", Offset: 0, Length: 6}},
	}
	if _, err := reactor.Handle(t.Context(), &api.Update{Message: mention}, chat, mentionUser); err != nil {
		t.Fatalf("handle mention: %v", err)
	}

	for _, userID := range []int64{commandUser.ID, mentionUser.ID} {
		probation, err := store.MessageProbation(t.Context(), chat.ID, userID)
		if err != nil || probation == nil || probation.GraduatedAt.Valid {
			t.Fatalf("routed message probation for %d: probation=%#v err=%v", userID, probation, err)
		}
	}
	if detector.calls != 0 {
		t.Fatalf("command or mention reached probation LLM: %d calls", detector.calls)
	}
}

func TestDisabledLLMDoesNotCreateOrMatureProbation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	detector := &testSpamDetector{result: boolPtr(false)}
	store := &testReactorStore{}
	service := &testBotService{}
	processedSpam := 0
	reactor := newMessageProbationTestReactor(t, &now, service, store, detector, &processedSpam)
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	user := &api.User{ID: 200, FirstName: testFirstNameUser}
	disabled := &db.Settings{LLMFirstMessageEnabled: false, CommunityVotingEnabled: true}
	message := &api.Message{MessageID: 1, Chat: *chat, From: user, Text: "unchecked"}

	if err := reactor.handleMessage(t.Context(), message, chat, user, disabled); err != nil {
		t.Fatalf("handle disabled LLM message: %v", err)
	}
	probation, err := store.MessageProbation(t.Context(), chat.ID, user.ID)
	if err != nil || probation != nil {
		t.Fatalf("disabled LLM created probation: probation=%#v err=%v", probation, err)
	}
	if detector.calls != 0 {
		t.Fatalf("disabled LLM classified message: %d calls", detector.calls)
	}
}

func newMessageProbationTestReactor(
	t *testing.T,
	now *time.Time,
	service *testBotService,
	store *testReactorStore,
	detector *testSpamDetector,
	processedSpam *int,
) *Reactor {
	t.Helper()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		switch method {
		case testTelegramMethodGetChatMember:
			return testChatMemberResponse(telegramMemberStatus, false, false, false)
		case "sendMessage":
			return map[string]any{
				logFieldMessageID: 900,
				testJSONDate:      now.Unix(),
				logFieldChat: map[string]any{
					"id":         -100,
					testJSONType: testChatTypeSupergroup,
				},
			}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})
	service.botAPI = botAPI
	reactor := &Reactor{
		s:            service,
		bot:          botAPI,
		store:        store,
		spamDetector: detector,
		banService:   &testBanService{},
		config: Config{SpamControl: config.SpamControl{
			MessageProbationDuration: 3 * time.Hour,
		}},
		processSpam: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			*processedSpam++
			return &moderation.ProcessingResult{MessageDeleted: true, UserBanned: true}, nil
		},
		processBanned: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			*processedSpam++
			return &moderation.ProcessingResult{MessageDeleted: true, UserBanned: true}, nil
		},
		lastResults: make(map[messageResultKey]*MessageProcessingResult),
		now:         func() time.Time { return *now },
	}
	return reactor
}
