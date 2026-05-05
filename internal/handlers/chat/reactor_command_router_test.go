package handlers

import (
	"context"
	"net/http"
	"testing"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
	moderation "github.com/iamwavecut/ngbot/internal/handlers/moderation"
)

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
			messageText: "/ban",
			entityLen:   len("/ban"),
			botUserName: "ngbot",
			want:        true,
		},
		{
			name:        "named current bot command is accepted",
			messageText: "/ban@ngbot",
			entityLen:   len("/ban@ngbot"),
			botUserName: "ngbot",
			want:        true,
		},
		{
			name:        "named current bot command is case insensitive",
			messageText: "/ban@NgBoT",
			entityLen:   len("/ban@NgBoT"),
			botUserName: "ngbot",
			want:        true,
		},
		{
			name:        "named foreign bot command is ignored",
			messageText: "/ban@otherbot",
			entityLen:   len("/ban@otherbot"),
			botUserName: "ngbot",
			want:        false,
		},
		{
			name:        "named command is ignored when bot username is empty",
			messageText: "/ban@ngbot",
			entityLen:   len("/ban@ngbot"),
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
					Type:   "bot_command",
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

func TestBanCommandRoutesByRestrictPermission(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		member        map[string]any
		wantBanned    int
		wantSpam      int
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
			name:     "admin with manage permission only starts voting",
			member:   testChatMemberResponse("administrator", true, false, false),
			wantSpam: 1,
		},
		{
			name:     "admin with promote permission only starts voting",
			member:   testChatMemberResponse("administrator", false, true, false),
			wantSpam: 1,
		},
		{
			name:     "regular member starts voting",
			member:   testChatMemberResponse("member", false, false, false),
			wantSpam: 1,
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
					if got := r.Form.Get("message_id"); got != "50" {
						t.Fatalf("unexpected deleted message id: %q", got)
					}
					deleteCommandCalls++
					return true
				default:
					t.Fatalf("unexpected bot method: %s", method)
					return nil
				}
			})

			chat := &api.Chat{ID: -100, Type: "supergroup"}
			actor := &api.User{ID: 100, FirstName: "Actor"}
			target := &api.User{ID: 200, FirstName: "Target"}
			reply := &api.Message{MessageID: 40, Chat: *chat, From: target, Text: "spam text"}
			command := &api.Message{MessageID: 50, MessageThreadID: 7, Chat: *chat, From: actor, Text: "/ban", ReplyToMessage: reply}
			settings := &db.Settings{CommunityVotingEnabled: true}

			bannedCalls := 0
			spamCalls := 0
			reactor := &Reactor{
				s: &testBotService{
					botAPI:   botAPI,
					language: "en",
				},
				processBanned: func(_ context.Context, gotMsg *api.Message, gotChat *api.Chat, lang string) (*moderation.ProcessingResult, error) {
					bannedCalls++
					if gotMsg != reply || gotChat != chat || lang != "en" {
						t.Fatalf("unexpected banned target: msg=%p chat=%p lang=%q", gotMsg, gotChat, lang)
					}
					return &moderation.ProcessingResult{MessageDeleted: true, UserBanned: true}, nil
				},
				processSpam: func(_ context.Context, gotMsg *api.Message, gotChat *api.Chat, lang string) (*moderation.ProcessingResult, error) {
					spamCalls++
					if gotMsg != reply || gotChat != chat || lang != "en" {
						t.Fatalf("unexpected voting target: msg=%p chat=%p lang=%q", gotMsg, gotChat, lang)
					}
					return &moderation.ProcessingResult{MessageDeleted: true, UserBanned: true}, nil
				},
			}

			if err := reactor.banCommand(context.Background(), command, chat, actor, settings); err != nil {
				t.Fatalf("banCommand returned error: %v", err)
			}

			if bannedCalls != tt.wantBanned {
				t.Fatalf("processBanned calls = %d, want %d", bannedCalls, tt.wantBanned)
			}
			if spamCalls != tt.wantSpam {
				t.Fatalf("processSpam calls = %d, want %d", spamCalls, tt.wantSpam)
			}
			if gotDelete := deleteCommandCalls > 0; gotDelete != tt.wantDeleteCmd {
				t.Fatalf("command delete called = %v, want %v", gotDelete, tt.wantDeleteCmd)
			}
		})
	}
}

func TestBanCommandCommunityVotingDisabledSkipsProcessing(t *testing.T) {
	t.Parallel()

	sendMessageCalls := 0
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodGetChatMember:
			return testChatMemberResponse("member", false, false, false)
		case testTelegramMethodSendMessage:
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			sendMessageCalls++
			if got := r.Form.Get("reply_parameters"); got == "" {
				t.Fatal("expected disabled voting response to reply to the command")
			}
			return map[string]any{
				"message_id": 70,
				"date":       0,
				"chat": map[string]any{
					"id":   -100,
					"type": "supergroup",
				},
			}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	chat := &api.Chat{ID: -100, Type: "supergroup"}
	actor := &api.User{ID: 100, FirstName: "Actor"}
	target := &api.User{ID: 200, FirstName: "Target"}
	command := &api.Message{
		MessageID:       50,
		MessageThreadID: 7,
		Chat:            *chat,
		From:            actor,
		Text:            "/ban",
		ReplyToMessage:  &api.Message{MessageID: 40, Chat: *chat, From: target, Text: "spam text"},
		IsTopicMessage:  true,
	}
	settings := &db.Settings{CommunityVotingEnabled: false}

	bannedCalls := 0
	spamCalls := 0
	reactor := &Reactor{
		s: &testBotService{
			botAPI:   botAPI,
			language: "en",
		},
		processBanned: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			bannedCalls++
			return nil, nil
		},
		processSpam: func(context.Context, *api.Message, *api.Chat, string) (*moderation.ProcessingResult, error) {
			spamCalls++
			return nil, nil
		},
	}

	if err := reactor.banCommand(context.Background(), command, chat, actor, settings); err != nil {
		t.Fatalf("banCommand returned error: %v", err)
	}

	if bannedCalls != 0 || spamCalls != 0 {
		t.Fatalf("expected no processing calls, got banned=%d spam=%d", bannedCalls, spamCalls)
	}
	if sendMessageCalls != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", sendMessageCalls)
	}
}

func testChatMemberResponse(status string, canManage bool, canPromote bool, canRestrict bool) map[string]any {
	return map[string]any{
		"user": map[string]any{
			"id":         100,
			"is_bot":     false,
			"first_name": "Actor",
		},
		"status":               status,
		"can_manage_chat":      canManage,
		"can_promote_members":  canPromote,
		"can_restrict_members": canRestrict,
	}
}
