package db

import (
	"database/sql"
	"testing"
	"time"
)

func TestMessageAuthorRejectsAmbiguousIdentity(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		author MessageAuthor
		valid  bool
	}{
		{author: MessageAuthor{Kind: MessageAuthorUser, ID: 12}, valid: true},
		{author: MessageAuthor{Kind: MessageAuthorSenderChat, ID: -12}, valid: true},
		{author: MessageAuthor{Kind: MessageAuthorUser, ID: -12}},
		{author: MessageAuthor{Kind: MessageAuthorSenderChat, ID: 12}},
		{author: MessageAuthor{Kind: MessageAuthorUser}},
		{author: MessageAuthor{Kind: MessageAuthorSenderChat}},
		{author: MessageAuthor{Kind: "legacy", ID: 12}},
		{author: MessageAuthor{ID: 12}},
	} {
		if err := test.author.Validate(); (err == nil) != test.valid {
			t.Errorf("author %+v validation = %v, want valid=%t", test.author, err, test.valid)
		}
	}
}

func TestMessageTrustRequiresFutureUnsuspendedExpiry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name  string
		trust *MessageTrust
		want  bool
	}{
		{name: "missing"},
		{name: "unverified", trust: &MessageTrust{SafeMessages: 3}},
		{name: "future", trust: &MessageTrust{TrustedUntil: sql.NullTime{Valid: true, Time: now.Add(time.Second)}}, want: true},
		{name: "cutoff", trust: &MessageTrust{TrustedUntil: sql.NullTime{Valid: true, Time: now}}},
		{name: "expired", trust: &MessageTrust{TrustedUntil: sql.NullTime{Valid: true, Time: now.Add(-time.Second)}}},
		{name: "suspended", trust: &MessageTrust{Suspended: true, TrustedUntil: sql.NullTime{Valid: true, Time: now.Add(time.Second)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.trust.Trusted(now); got != test.want {
				t.Fatalf("Trusted() = %t, want %t", got, test.want)
			}
		})
	}
}
