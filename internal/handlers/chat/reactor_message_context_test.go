package handlers

import (
	"strings"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
)

func TestConversationIsolatesThreadsAndPrioritizesReplyAndRoot(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t)
	save := func(id, thread int, text string, age time.Duration) {
		t.Helper()
		err := f.store.UpsertMessageContext(t.Context(), &db.MessageContext{ChatID: f.chat.ID, MessageID: id, ThreadID: thread, AuthorKind: db.MessageAuthorUser, AuthorID: 200, Text: text, SentAt: f.now.Add(-age), UpdatedAt: f.now.Add(-age)})
		if err != nil {
			t.Fatal(err)
		}
	}
	save(1, 1, "original post", time.Hour)
	for id := 2; id <= 8; id++ {
		save(id, 1, strings.Repeat("я", 2001), time.Minute)
	}
	save(9, 20, "foreign thread secret", time.Minute)
	save(10, 1, "old text secret", 25*time.Hour)
	candidate := f.message(11)
	candidate.MessageThreadID = 1
	candidate.ReplyToMessage = &api.Message{MessageID: 8, Chat: *f.chat}
	history, err := f.reactor.messageConversation(t.Context(), candidate, f.chat)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) < 3 || history[0].Role != "direct_reply" || history[1].Role != "original_post" || history[1].Message != "original post" {
		t.Fatalf("wrong priority: %#v", history)
	}
	total := 0
	for _, item := range history {
		total += len([]rune(item.Message))
		if len([]rune(item.Message)) > 2000 || strings.Contains(item.Message, "secret") {
			t.Fatal("context leaked scope or exceeded per-message limit")
		}
	}
	if total != 8000 {
		t.Fatalf("context runes=%d, want capped 8000", total)
	}
	candidate.MessageThreadID = 20
	candidate.ReplyToMessage = nil
	history, err = f.reactor.messageConversation(t.Context(), candidate, f.chat)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Message != "foreign thread secret" {
		t.Fatalf("thread isolation: %#v", history)
	}
	candidate.Chat.ID = -101
	history, err = f.reactor.messageConversation(t.Context(), candidate, &candidate.Chat)
	if err != nil || len(history) != 0 {
		t.Fatalf("chat isolation: %#v %v", history, err)
	}
}

func TestUnknownDiscussionUsesOnlyReplyChainAndPlainGroupUsesRecentFive(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t)
	for id := 1; id < 10; id++ {
		msg := f.message(id)
		if id < 4 && id > 1 {
			msg.ReplyToMessage = &api.Message{MessageID: id - 1, Chat: *f.chat}
		}
		if err := f.reactor.rememberMessageContext(t.Context(), msg, f.chat, f.settings); err != nil {
			t.Fatal(err)
		}
	}
	candidate := f.message(10)
	candidate.Chat.Type = "supergroup"
	candidate.ReplyToMessage = &api.Message{MessageID: 3, Chat: candidate.Chat}
	history, err := f.reactor.messageConversation(t.Context(), candidate, &candidate.Chat)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("unknown linked discussion included unrelated messages: %#v", history)
	}
	candidate.Chat.Type = "group"
	candidate.ReplyToMessage = nil
	history, err = f.reactor.messageConversation(t.Context(), candidate, &candidate.Chat)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 5 {
		t.Fatalf("plain group history=%d, want 5", len(history))
	}
}

func TestTrustedMessagesEditsAndBotDeletionsUpdateConversation(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t)
	for id := 1; id <= 4; id++ {
		f.handle(t, f.message(id), false)
	}
	if f.detector.calls != 3 {
		t.Fatal("trusted message was classified")
	}
	saved, err := f.store.MessageContext(t.Context(), f.chat.ID, 4)
	if err != nil || saved == nil {
		t.Fatalf("trusted message not retained: %v", err)
	}
	edited := f.message(4)
	edited.EditDate = f.now.Add(time.Minute).Unix()
	edited.Text = "changed conversation text"
	f.handle(t, edited, true)
	saved, err = f.store.MessageContext(t.Context(), f.chat.ID, 4)
	if err != nil || saved.Text != edited.Text {
		t.Fatalf("trusted edit context not updated: %#v %v", saved, err)
	}
	if f.detector.calls != 3 {
		t.Fatal("unbound trusted edit classified")
	}
	if err := f.store.DeleteMessageContext(t.Context(), f.chat.ID, 4); err != nil {
		t.Fatal(err)
	}
	f.handle(t, edited, true)
	saved, err = f.store.MessageContext(t.Context(), f.chat.ID, 4)
	if err != nil || saved != nil {
		t.Fatal("deleted context resurrected")
	}
	candidate := f.message(5)
	candidate.ReplyToMessage = edited
	candidate.Quote = &api.TextQuote{Text: edited.Text}
	if err := f.reactor.rememberMessageContext(t.Context(), candidate, f.chat, f.settings); err != nil {
		t.Fatal(err)
	}
	history, err := f.reactor.messageConversation(t.Context(), candidate, f.chat)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range history {
		if item.Message == edited.Text {
			t.Fatal("deleted reply returned in history")
		}
	}
}

func TestConversationQuoteRequiresFreshSameChatReply(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"fresh", "old", "other chat", "unavailable"} {
		t.Run(scope, func(t *testing.T) {
			f := newTrustFixture(t)
			parent := f.message(1)
			if scope == "old" {
				parent.Date = f.now.Add(-25 * time.Hour).Unix()
			}
			if scope == "other chat" {
				parent.Chat.ID = -200
			}
			candidate := f.message(2)
			candidate.Quote = &api.TextQuote{Text: "quoted text"}
			if scope != "unavailable" {
				candidate.ReplyToMessage = parent
			}
			if err := f.reactor.rememberMessageContext(t.Context(), candidate, f.chat, f.settings); err != nil {
				t.Fatal(err)
			}
			history, err := f.reactor.messageConversation(t.Context(), candidate, f.chat)
			if err != nil {
				t.Fatal(err)
			}
			quoted := false
			for _, item := range history {
				quoted = quoted || item.Role == "quote"
			}
			if quoted != (scope == "fresh") {
				t.Fatalf("quote crossed context boundary: %#v", history)
			}
		})
	}
}

func TestReplyEstablishesDiscussionRootAndOldRootIsOmitted(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t)
	root := f.message(10)
	root.IsAutomaticForward = true
	root.SenderChat = &api.Chat{ID: -999, Type: "channel"}
	root.Text = "source post"
	comment := f.message(11)
	comment.ReplyToMessage = root
	if err := f.reactor.rememberMessageContext(t.Context(), comment, f.chat, f.settings); err != nil {
		t.Fatal(err)
	}
	saved, err := f.store.MessageContext(t.Context(), f.chat.ID, 11)
	if err != nil || saved.ThreadID != 10 {
		t.Fatalf("thread not inherited: %#v %v", saved, err)
	}
	next := f.message(12)
	next.ReplyToMessage = comment
	if err := f.reactor.rememberMessageContext(t.Context(), next, f.chat, f.settings); err != nil {
		t.Fatal(err)
	}
	history, err := f.reactor.messageConversation(t.Context(), next, f.chat)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[1].Role != "original_post" || history[1].Message != "source post" {
		t.Fatalf("missing source: %#v", history)
	}
	f.now = f.now.Add(25 * time.Hour)
	history, err = f.reactor.messageConversation(t.Context(), next, f.chat)
	if err != nil || len(history) != 0 {
		t.Fatalf("old context read: %#v %v", history, err)
	}
}

func TestSameSecondEditsReplaceAndClearContextWithoutReplay(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t)
	msg := f.message(1)
	for _, tt := range []struct {
		updateID   int
		edit       bool
		text, want string
	}{
		{100, false, "original", "original"},
		{101, true, "replacement", "replacement"},
		{102, true, "", ""},
		{101, true, "replacement", ""},
		{100, false, "original", ""},
	} {
		msg.Text = tt.text
		update := &api.Update{UpdateID: tt.updateID, Message: msg}
		if tt.edit {
			msg.EditDate = msg.Date
			update.Message = nil
			update.EditedMessage = msg
		} else {
			msg.EditDate = 0
		}
		if _, err := f.reactor.Handle(t.Context(), update, f.chat, f.user); err != nil {
			t.Fatal(err)
		}
		saved, err := f.store.MessageContext(t.Context(), f.chat.ID, msg.MessageID)
		if err != nil || saved.Text != tt.want {
			t.Fatalf("update %d: saved=%#v err=%v", tt.updateID, saved, err)
		}
	}
}

func TestSafeUnboundEditRemainsProtectedAfterAdmission(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t)
	edit := f.message(50)
	edit.EditDate = f.now.Unix()
	f.handle(t, edit, true)
	for id := 51; id <= 53; id++ {
		f.handle(t, f.message(id), false)
	}
	f.detector.result = boolPtr(true)
	edit.Text = "spam edit after trust"
	f.handle(t, edit, true)
	if f.detector.calls != 5 || f.spam != 1 {
		t.Fatalf("checked edit lost protection: calls=%d actions=%d", f.detector.calls, f.spam)
	}
}

func TestConversationRejectsReplyFromAnotherKnownThread(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t)
	parent := f.message(5)
	parent.MessageThreadID = 20
	parent.Text = "foreign thread text"
	if err := f.reactor.rememberMessageContext(t.Context(), parent, f.chat, f.settings); err != nil {
		t.Fatal(err)
	}
	candidate := f.message(10)
	candidate.MessageThreadID = 1
	candidate.ReplyToMessage = parent
	candidate.Quote = &api.TextQuote{Text: parent.Text}
	history, err := f.reactor.messageConversation(t.Context(), candidate, f.chat)
	if err != nil || len(history) != 0 {
		t.Fatalf("foreign thread context: %#v error=%v", history, err)
	}
}
