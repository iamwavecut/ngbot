package handlers

import (
	"context"
	"net/http"
	"testing"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
)

func TestDiagnosticCommandAuthorization(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		if method != testTelegramMethodGetChatMember {
			t.Fatalf("unexpected bot method: %s", method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.Form.Get("user_id") == "100" {
			return testChatMemberResponse("administrator", false, false, false)
		}
		return testChatMemberResponse(telegramMemberStatus, false, false, false)
	})
	reactor := &Reactor{
		bot:    botAPI,
		config: Config{SpamControl: config.SpamControl{DebugUserID: 42}},
	}
	privateChat := &api.Chat{ID: 42, Type: telegramChatTypePrivate}
	groupChat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}

	if !reactor.diagnosticCommandAllowed(t.Context(), privateChat, &api.User{ID: 42}) {
		t.Fatal("configured debug user should be authorized in private chat")
	}
	if reactor.diagnosticCommandAllowed(t.Context(), privateChat, &api.User{ID: 99}) {
		t.Fatal("non-debug user should be denied in private chat")
	}
	if !reactor.diagnosticCommandAllowed(t.Context(), groupChat, &api.User{ID: 100}) {
		t.Fatal("source-chat administrator should be authorized")
	}
	if reactor.diagnosticCommandAllowed(t.Context(), groupChat, &api.User{ID: 101}) {
		t.Fatal("regular source-chat member should be denied")
	}
}

func TestCommandTargetsCurrentBot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		messageText string
		entityLen   int
		botUserName string
		want        bool
	}{
		{
			name:        "unnamed command is accepted",
			messageText: testVoteBanCommand,
			entityLen:   len(testVoteBanCommand),
			botUserName: "ngbot",
			want:        true,
		},
		{
			name:        "named current bot command is accepted",
			messageText: "/voteban@ngbot",
			entityLen:   len("/voteban@ngbot"),
			botUserName: "ngbot",
			want:        true,
		},
		{
			name:        "named current bot command is case insensitive",
			messageText: "/voteban@NgBoT",
			entityLen:   len("/voteban@NgBoT"),
			botUserName: "ngbot",
			want:        true,
		},
		{
			name:        "named foreign bot command is ignored",
			messageText: "/voteban@otherbot",
			entityLen:   len("/voteban@otherbot"),
			botUserName: "ngbot",
			want:        false,
		},
		{
			name:        "named command is ignored when bot username is empty",
			messageText: "/voteban@ngbot",
			entityLen:   len("/voteban@ngbot"),
			botUserName: "",
			want:        false,
		},
		{
			name:        "non command is ignored",
			messageText: "ban",
			entityLen:   0,
			botUserName: "ngbot",
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			msg := &api.Message{Text: tt.messageText}
			if tt.entityLen > 0 {
				msg.Entities = []api.MessageEntity{{
					Type:   testEntityBotCommand,
					Offset: 0,
					Length: tt.entityLen,
				}}
			}

			got := commandTargetsCurrentBot(msg, tt.botUserName)
			if got != tt.want {
				t.Fatalf("commandTargetsCurrentBot() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVoteBanCommandRoutesByRestrictPermissionAfterReportCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		member        map[string]any
		wantBanned    int
		wantReported  int
		wantDeleteCmd bool
	}{
		{
			name:          "creator bans immediately",
			member:        testChatMemberResponse("creator", false, false, false),
			wantBanned:    1,
			wantDeleteCmd: true,
		},
		{
			name:          "admin with restrict permission bans immediately",
			member:        testChatMemberResponse("administrator", false, false, true),
			wantBanned:    1,
			wantDeleteCmd: true,
		},
		{
			name:         "admin with manage permission only starts voting",
			member:       testChatMemberResponse("administrator", true, false, false),
			wantReported: 1,
		},
		{
			name:         "admin with promote permission only starts voting",
			member:       testChatMemberResponse("administrator", false, true, false),
			wantReported: 1,
		},
		{
			name:         "regular member starts voting",
			member:       testChatMemberResponse(telegramMemberStatus, false, false, false),
			wantReported: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			deleteCommandCalls := 0
			botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
				switch method {
				case testTelegramMethodGetChatMember:
					return tt.member
				case testTelegramMethodDeleteMessage:
					if err := r.ParseForm(); err != nil {
						t.Fatalf("parse form: %v", err)
					}
					if got := r.Form.Get(logFieldMessageID); got != "50" {
						t.Fatalf("unexpected deleted message id: %q", got)
					}
					deleteCommandCalls++
					return true
				default:
					t.Fatalf("unexpected bot method: %s", method)
					return nil
				}
			})

			chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
			actor := &api.User{ID: 100, FirstName: testFirstNameActor}
			target := &api.User{ID: 200, FirstName: testFirstNameTarget}
			reply := &api.Message{MessageID: 40, Chat: *chat, From: target, Text: "spam text"}
			command := &api.Message{MessageID: 50, MessageThreadID: 7, Chat: *chat, From: actor, Text: testVoteBanCommand, ReplyToMessage: reply}
			settings := &db.Settings{CommunityVotingEnabled: true}

			bannedCalls := 0
			reportedCalls := 0
			detector := &testSpamDetector{reportedResult: boolPtr(false)}
			reactor := &Reactor{
				s: &testBotService{
					botAPI:   botAPI,
					language: "en",
				},
				bot:          botAPI,
				spamDetector: detector,
				processBanned: func(_ context.Context, gotMsg *api.Message, gotChat *api.Chat, lang string) (*moderation.ProcessingResult, error) {
					bannedCalls++
					if gotMsg != reply || gotChat != chat || lang != "en" {
						t.Fatalf("unexpected banned target: msg=%p chat=%p lang=%q", gotMsg, gotChat, lang)
					}
					return &moderation.ProcessingResult{MessageDeleted: true, UserBanned: true}, nil
				},
				processReported: func(_ context.Context, gotMsg *api.Message, gotReport *api.Message, gotChat *api.Chat, lang string) (*moderation.ProcessingResult, error) {
					reportedCalls++
					if gotMsg != reply || gotChat != chat || lang != "en" {
						t.Fatalf("unexpected voting target: msg=%p chat=%p lang=%q", gotMsg, gotChat, lang)
					}
					if gotReport != command {
						t.Fatalf("unexpected report message: %p", gotReport)
					}
					return &moderation.ProcessingResult{}, nil
				},
			}

			if err := reactor.voteBanCommand(context.Background(), command, chat, actor, settings); err != nil {
				t.Fatalf("voteBanCommand returned error: %v", err)
			}

			if detector.reportedCalls != 1 {
				t.Fatalf("reported spam checks = %d, want 1", detector.reportedCalls)
			}
			if bannedCalls != tt.wantBanned {
				t.Fatalf("processBanned calls = %d, want %d", bannedCalls, tt.wantBanned)
			}
			if reportedCalls != tt.wantReported {
				t.Fatalf("processReported calls = %d, want %d", reportedCalls, tt.wantReported)
			}
			if gotDelete := deleteCommandCalls > 0; gotDelete != tt.wantDeleteCmd {
				t.Fatalf("command delete called = %v, want %v", gotDelete, tt.wantDeleteCmd)
			}
		})
	}
}

func TestVoteBanCommandCommunityVotingDisabledSkipsProcessing(t *testing.T) {
	t.Parallel()

	sendMessageCalls := 0
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodGetChatMember:
			return testChatMemberResponse(telegramMemberStatus, false, false, false)
		case testTelegramMethodSendMessage:
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			sendMessageCalls++
			if got := r.Form.Get("reply_parameters"); got == "" {
				t.Fatal("expected disabled voting response to reply to the command")
			}
			return map[string]any{
				logFieldMessageID: 70,
				testJSONDate:      0,
				logFieldChat: map[string]any{
					"id":   -100,
					"type": testChatTypeSupergroup,
				},
			}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	actor := &api.User{ID: 100, FirstName: testFirstNameActor}
	target := &api.User{ID: 200, FirstName: testFirstNameTarget}
	command := &api.Message{
		MessageID:       50,
		MessageThreadID: 7,
		Chat:            *chat,
		From:            actor,
		Text:            testVoteBanCommand,
		ReplyToMessage:  &api.Message{MessageID: 40, Chat: *chat, From: target, Text: "spam text"},
		IsTopicMessage:  true,
	}
	settings := &db.Settings{CommunityVotingEnabled: false}

	bannedCalls := 0
	reportedCalls := 0
	reactor := &Reactor{
		s: &testBotService{
			botAPI:   botAPI,
			language: "en",
		},
		bot:          botAPI,
		spamDetector: &testSpamDetector{reportedResult: boolPtr(false)},
		processBanned: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			bannedCalls++
			return nil, nil
		},
		processReported: func(context.Context, *api.Message, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			reportedCalls++
			return nil, nil
		},
	}

	if err := reactor.voteBanCommand(context.Background(), command, chat, actor, settings); err != nil {
		t.Fatalf("voteBanCommand returned error: %v", err)
	}

	if bannedCalls != 0 || reportedCalls != 0 {
		t.Fatalf("expected no processing calls, got banned=%d reported=%d", bannedCalls, reportedCalls)
	}
	if sendMessageCalls != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", sendMessageCalls)
	}
}

func TestBanCommandIsIgnored(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		t.Fatalf("unexpected bot method: %s", method)
		return nil
	})
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	actor := &api.User{ID: 100, FirstName: testFirstNameActor}
	msg := &api.Message{
		MessageID: 50,
		Chat:      *chat,
		From:      actor,
		Text:      "/ban",
		Entities: []api.MessageEntity{{
			Type:   testEntityBotCommand,
			Offset: 0,
			Length: len("/ban"),
		}},
	}
	reactor := &Reactor{
		s: &testBotService{
			botAPI: botAPI,
		},
		bot: botAPI,
	}

	if err := reactor.handleCommand(context.Background(), msg, chat, actor, &db.Settings{CommunityVotingEnabled: true}); err != nil {
		t.Fatalf("handleCommand returned error: %v", err)
	}
}

func TestVoteBanCommandWithoutReplySendsUsageHelp(t *testing.T) {
	t.Parallel()

	sendMessageCalls := 0
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodSendMessage:
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			sendMessageCalls++
			if got := r.Form.Get("reply_parameters"); got == "" {
				t.Fatal("expected usage help to reply to the command")
			}
			if got := r.Form.Get("text"); got == "" {
				t.Fatal("expected usage help text")
			}
			return map[string]any{
				logFieldMessageID: 70,
				testJSONDate:      0,
				logFieldChat: map[string]any{
					"id":   -100,
					"type": testChatTypeSupergroup,
				},
			}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	actor := &api.User{ID: 100, FirstName: testFirstNameActor}
	command := &api.Message{MessageID: 50, Chat: *chat, From: actor, Text: testVoteBanCommand}
	reactor := &Reactor{
		s: &testBotService{
			botAPI:   botAPI,
			language: "en",
		},
		bot: botAPI,
	}

	if err := reactor.voteBanCommand(context.Background(), command, chat, actor, &db.Settings{CommunityVotingEnabled: true}); err != nil {
		t.Fatalf("voteBanCommand returned error: %v", err)
	}
	if sendMessageCalls != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", sendMessageCalls)
	}
}

func TestVoteBanCommandLLMSpamBansImmediatelyAndDeletesReportMessage(t *testing.T) {
	t.Parallel()

	sendMessageCalls := 0
	deleteMessageCalls := 0
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodSendMessage:
			sendMessageCalls++
			return map[string]any{
				logFieldMessageID: 70,
				testJSONDate:      0,
				logFieldChat: map[string]any{
					"id":   -100,
					"type": testChatTypeSupergroup,
				},
			}
		case testTelegramMethodDeleteMessage:
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			deleteMessageCalls++
			if got := r.Form.Get(logFieldMessageID); got != "50" {
				t.Fatalf("expected report message delete, got message_id=%q", got)
			}
			return true
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	actor := &api.User{ID: 100, FirstName: testFirstNameActor}
	target := &api.User{ID: 200, FirstName: testFirstNameTarget}
	reply := &api.Message{MessageID: 40, Chat: *chat, From: target, Text: "reported spam text"}
	command := &api.Message{MessageID: 50, Chat: *chat, From: actor, Text: testVoteBanCommand, ReplyToMessage: reply}
	bannedCalls := 0
	reactor := &Reactor{
		s: &testBotService{
			botAPI:   botAPI,
			language: "en",
		},
		bot:          botAPI,
		spamDetector: &testSpamDetector{reportedResult: boolPtr(true)},
		processBanned: func(_ context.Context, gotMsg *api.Message, gotChat *api.Chat, lang string) (*moderation.ProcessingResult, error) {
			bannedCalls++
			if gotMsg != reply || gotChat != chat || lang != "en" {
				t.Fatalf("unexpected banned target: msg=%p chat=%p lang=%q", gotMsg, gotChat, lang)
			}
			return &moderation.ProcessingResult{MessageDeleted: true, UserBanned: true}, nil
		},
	}

	if err := reactor.voteBanCommand(context.Background(), command, chat, actor, &db.Settings{CommunityVotingEnabled: true}); err != nil {
		t.Fatalf("voteBanCommand returned error: %v", err)
	}
	if bannedCalls != 1 {
		t.Fatalf("processBanned calls = %d, want 1", bannedCalls)
	}
	if sendMessageCalls != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", sendMessageCalls)
	}
	if deleteMessageCalls != 1 {
		t.Fatalf("deleteMessage calls = %d, want 1", deleteMessageCalls)
	}
}

func TestMessageMentionCurrentBotTriggersReportFlow(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodGetChatMember:
			return testChatMemberResponse(telegramMemberStatus, false, false, false)
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})
	chat := &api.Chat{ID: -100, Type: testChatTypeSupergroup}
	actor := &api.User{ID: 100, FirstName: testFirstNameActor}
	target := &api.User{ID: 200, FirstName: testFirstNameTarget}
	reply := &api.Message{MessageID: 40, Chat: *chat, From: target, Text: "reported text"}
	report := &api.Message{
		MessageID:       50,
		MessageThreadID: 7,
		Chat:            *chat,
		From:            actor,
		Text:            "@testbot",
		Entities: []api.MessageEntity{{
			Type:   "mention",
			Offset: 0,
			Length: len("@testbot"),
		}},
		ReplyToMessage: reply,
	}
	reportedCalls := 0
	reactor := &Reactor{
		s: &testBotService{
			botAPI:   botAPI,
			language: "en",
			settings: &db.Settings{CommunityVotingEnabled: true},
		},
		bot:          botAPI,
		store:        &testReactorStore{},
		spamDetector: &testSpamDetector{reportedResult: boolPtr(false)},
		processReported: func(_ context.Context, gotMsg *api.Message, gotReport *api.Message, gotChat *api.Chat, lang string) (*moderation.ProcessingResult, error) {
			reportedCalls++
			if gotMsg != reply || gotReport != report || gotChat != chat || lang != "en" {
				t.Fatalf("unexpected report flow args: msg=%p report=%p chat=%p lang=%q", gotMsg, gotReport, gotChat, lang)
			}
			return &moderation.ProcessingResult{}, nil
		},
	}

	proceed, err := reactor.Handle(context.Background(), &api.Update{Message: report}, chat, actor)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !proceed {
		t.Fatal("expected reactor to proceed")
	}
	if reportedCalls != 1 {
		t.Fatalf("processReported calls = %d, want 1", reportedCalls)
	}
}

func TestMessageMentionsCurrentBot(t *testing.T) {
	t.Parallel()

	msg := &api.Message{
		Text: "hi @testbot",
		Entities: []api.MessageEntity{{
			Type:   "mention",
			Offset: 3,
			Length: len("@testbot"),
		}},
	}
	if !messageMentionsCurrentBot(msg, api.User{ID: 1, UserName: "testbot"}) {
		t.Fatal("expected current bot mention to match")
	}
	if messageMentionsCurrentBot(msg, api.User{ID: 2, UserName: "otherbot"}) {
		t.Fatal("expected foreign bot mention to be ignored")
	}
	textMention := &api.Message{
		Text: "bot",
		Entities: []api.MessageEntity{{
			Type:   "text_mention",
			Offset: 0,
			Length: len("bot"),
			User:   &api.User{ID: 1, UserName: "testbot", IsBot: true},
		}},
	}
	if !messageMentionsCurrentBot(textMention, api.User{ID: 1, UserName: "testbot"}) {
		t.Fatal("expected current bot text mention to match")
	}
}

func testChatMemberResponse(status string, canManage bool, canPromote bool, canRestrict bool) map[string]any {
	return map[string]any{
		"user": map[string]any{
			"id":              100,
			testJSONIsBot:     false,
			testJSONFirstName: testFirstNameActor,
		},
		"status":               status,
		"can_manage_chat":      canManage,
		"can_promote_members":  canPromote,
		"can_restrict_members": canRestrict,
	}
}
