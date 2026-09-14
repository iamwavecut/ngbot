package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
)

type failingRestrictionContextStore struct{ banStore }

func (failingRestrictionContextStore) AddRestriction(context.Context, *db.UserRestriction) error {
	return errors.New("forced restriction persistence failure")
}

func TestUserRevokeContextIsRemovedBeforeRestrictionPersistenceFailure(t *testing.T) {
	t.Parallel()
	client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "context.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.SetSettings(t.Context(), db.DefaultSettings(-100)); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := client.UpsertMessageContext(t.Context(), &db.MessageContext{ChatID: -100, MessageID: 10, AuthorKind: db.MessageAuthorUser, AuthorID: 200, Text: "earlier contribution", SentAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	botAPI := newModerationTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != "banChatMember" {
			t.Fatalf("unexpected method: %s", method)
		}
		return true
	})
	service := &defaultBanService{bot: botAPI, db: failingRestrictionContextStore{banStore: client}}
	if err := service.BanUserWithMessage(t.Context(), -100, 200, 40); err == nil {
		t.Fatal("expected restriction persistence failure")
	}
	if saved, err := client.MessageContext(t.Context(), -100, 10); err != nil || saved != nil {
		t.Fatalf("confirmed revoke retained history after later persistence failure: saved=%+v err=%v", saved, err)
	}
}

func TestFailedUserRevokeAndSuccessfulChannelBanPreserveOtherHistory(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"spam_failure", "banlist_failure", "channel_success"} {
		t.Run(path, func(t *testing.T) {
			client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "context.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			if err := client.SetSettings(t.Context(), db.DefaultSettings(-100)); err != nil {
				t.Fatal(err)
			}
			author := db.MessageAuthor{Kind: db.MessageAuthorUser, ID: 200}
			channel := path == "channel_success"
			if channel {
				author = db.MessageAuthor{Kind: db.MessageAuthorSenderChat, ID: -200}
			}
			now := time.Now()
			for _, id := range []int{10, 40} {
				if err := client.UpsertMessageContext(t.Context(), &db.MessageContext{ChatID: -100, MessageID: id, AuthorKind: author.Kind, AuthorID: author.ID, Text: "saved conversation", SentAt: now, UpdatedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			botAPI := newModerationRetryTestBotAPI(t, func(method string, _ *http.Request) testAPIResponse {
				if channel {
					if method != "banChatSenderChat" && method != "deleteMessage" {
						t.Fatalf("unexpected channel method: %s", method)
					}
					return testAPIResponse{OK: true, Result: true}
				}
				if method != "banChatMember" {
					t.Fatalf("unexpected failed revoke method: %s", method)
				}
				return testAPIResponse{OK: false, Description: "Bad Gateway"}
			})
			if path == "banlist_failure" {
				service := &defaultBanService{bot: botAPI, db: client}
				err = service.BanUserWithMessage(t.Context(), -100, author.ID, 40)
			} else {
				control := &SpamControl{bot: botAPI, store: client}
				err = control.banSpamCaseAuthor(t.Context(), &db.SpamCase{ChatID: -100, UserID: author.ID, AuthorKind: author.Kind, MessageID: 40})
			}
			if (err == nil) != channel {
				t.Fatalf("unexpected enforcement error: %v", err)
			}
			if saved, err := client.MessageContext(t.Context(), -100, 10); err != nil || saved == nil {
				t.Fatalf("unrevoked history disappeared: saved=%+v err=%v", saved, err)
			}
			if saved, err := client.MessageContext(t.Context(), -100, 40); err != nil || (saved == nil) != channel {
				t.Fatalf("candidate deletion disagrees with Telegram: saved=%+v err=%v", saved, err)
			}
		})
	}
}

func TestNoOpUserBanOnlyDeletesExplicitCandidateContext(t *testing.T) {
	t.Parallel()
	for _, marker := range []string{"USER_NOT_PARTICIPANT", "PARTICIPANT_ID_INVALID", "MEMBER NOT FOUND", "USER IS DEACTIVATED"} {
		t.Run(marker, func(t *testing.T) {
			client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "context.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			if err := client.SetSettings(t.Context(), db.DefaultSettings(-100)); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			for _, id := range []int{10, 40} {
				if err := client.UpsertMessageContext(t.Context(), &db.MessageContext{ChatID: -100, MessageID: id, AuthorKind: db.MessageAuthorUser, AuthorID: 200, Text: "saved conversation", SentAt: now, UpdatedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			deletions := 0
			botAPI := newModerationRetryTestBotAPI(t, func(method string, r *http.Request) testAPIResponse {
				switch method {
				case "banChatMember":
					return testAPIResponse{OK: false, Description: marker}
				case "deleteMessage":
					deletions++
					if err := r.ParseForm(); err != nil {
						t.Fatal(err)
					}
					if r.Form.Get("message_id") != "40" {
						t.Errorf("deleted unexpected message: %v", r.Form)
					}
					return testAPIResponse{OK: true, Result: true}
				default:
					t.Fatalf("unexpected method: %s", method)
					return testAPIResponse{}
				}
			})
			control := &SpamControl{bot: botAPI, store: client}
			if err := control.banSpamCaseAuthor(t.Context(), &db.SpamCase{ChatID: -100, UserID: 200, AuthorKind: db.MessageAuthorUser, MessageID: 40}); err != nil {
				t.Fatal(err)
			}
			if saved, err := client.MessageContext(t.Context(), -100, 10); err != nil || saved == nil {
				t.Fatalf("no-op ban purged undeleted history: saved=%+v err=%v", saved, err)
			}
			if saved, err := client.MessageContext(t.Context(), -100, 40); err != nil || saved != nil || deletions != 1 {
				t.Fatalf("candidate cleanup did not require deletion: saved=%+v calls=%d err=%v", saved, deletions, err)
			}
		})
	}
}

func (*testBanStore) DeleteAuthorMessageContext(context.Context, int64, db.MessageAuthor) error {
	return nil
}

func (*recordingBanStore) DeleteAuthorMessageContext(context.Context, int64, db.MessageAuthor) error {
	return nil
}

func (*testModerationStore) DeleteAuthorMessageContext(context.Context, int64, db.MessageAuthor) error {
	return nil
}

func TestSuccessfulUserRevokeRemovesOtherSavedMessages(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"spam", "banlist"} {
		t.Run(path, func(t *testing.T) {
			client, err := sqlite.NewSQLiteClient(t.Context(), t.TempDir(), "context.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			if err := client.SetSettings(t.Context(), db.DefaultSettings(-100)); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			message := &db.MessageContext{ChatID: -100, MessageID: 10, AuthorKind: db.MessageAuthorUser, AuthorID: 200, Text: "earlier contribution", SentAt: now, UpdatedAt: now}
			if err := client.UpsertMessageContext(t.Context(), message); err != nil {
				t.Fatal(err)
			}
			botAPI := newModerationTestBotAPI(t, func(method string, r *http.Request) any {
				if method != "banChatMember" {
					t.Fatalf("unexpected method: %s", method)
				}
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				if r.Form.Get("revoke_messages") != "true" {
					t.Errorf("messages were not revoked: %v", r.Form)
				}
				return true
			})
			if path == "spam" {
				control := &SpamControl{bot: botAPI, store: client}
				err = control.banSpamCaseAuthor(t.Context(), &db.SpamCase{ChatID: -100, UserID: 200, AuthorKind: db.MessageAuthorUser, MessageID: 40})
			} else {
				service := &defaultBanService{bot: botAPI, db: client}
				err = service.BanUserWithMessage(t.Context(), -100, 200, 40)
			}
			if err != nil {
				t.Fatal(err)
			}
			message.UpdatedAt = now.Add(time.Second)
			if err := client.UpsertMessageContext(t.Context(), message); err != nil {
				t.Fatal(err)
			}
			if saved, err := client.MessageContext(t.Context(), -100, 10); err != nil || saved != nil {
				t.Fatalf("revoked non-candidate message remained or replayed: saved=%+v err=%v", saved, err)
			}
		})
	}
}
