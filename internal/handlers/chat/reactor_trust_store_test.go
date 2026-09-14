package handlers

import (
	"context"
	"database/sql"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

type authorTrustKey struct {
	chatID int64
	author db.MessageAuthor
}

func (s *testReactorStore) MessageTrust(_ context.Context, chatID int64, author db.MessageAuthor) (*db.MessageTrust, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.trustError != nil {
		return nil, s.trustError
	}
	value, found := s.trusts[authorTrustKey{chatID, author}]
	if !found {
		return nil, nil
	}
	return &value, nil
}

func (s *testReactorStore) EnsureMessageTrust(ctx context.Context, chatID int64, author db.MessageAuthor) (*db.MessageTrust, error) {
	value, err := s.MessageTrust(ctx, chatID, author)
	if err != nil || value != nil {
		return value, err
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.trusts == nil {
		s.trusts = make(map[authorTrustKey]db.MessageTrust)
	}
	value = &db.MessageTrust{ChatID: chatID, AuthorKind: author.Kind, AuthorID: author.ID}
	s.trusts[authorTrustKey{chatID, author}] = *value
	return value, nil
}

func (s *testReactorStore) RecordSafeAuthorMessage(ctx context.Context, chatID int64, author db.MessageAuthor, messageID int, now time.Time, required int, duration time.Duration, eligible bool) (*db.MessageTrust, bool, error) {
	value, err := s.EnsureMessageTrust(ctx, chatID, author)
	if err != nil {
		return nil, false, err
	}
	inserted, err := s.RecordChallengedMessage(ctx, chatID, author.ID, messageID)
	if err != nil {
		return nil, false, err
	}
	if inserted && eligible && !value.Suspended && !value.Trusted(now) {
		value.SafeMessages = min(value.SafeMessages+1, required)
		if value.SafeMessages >= required {
			value.TrustedUntil = sql.NullTime{Time: now.Add(duration), Valid: true}
		}
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.trusts[authorTrustKey{chatID, author}] = *value
	return value, inserted, nil
}

func (s *testReactorStore) IsCheckedAuthorMessage(ctx context.Context, chatID int64, author db.MessageAuthor, messageID int) (bool, error) {
	return s.IsChallengedMessage(ctx, chatID, author.ID, messageID)
}

func (s *testReactorStore) ResetMessageTrust(_ context.Context, chatID int64, author db.MessageAuthor) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	delete(s.trusts, authorTrustKey{chatID, author})
	return nil
}

func (*testReactorStore) UpsertMessageContext(context.Context, *db.MessageContext) error { return nil }

func (*testReactorStore) MessageContext(context.Context, int64, int) (*db.MessageContext, error) {
	return nil, nil
}

func (*testReactorStore) RecentMessageContext(context.Context, int64, int, int, time.Time, int) ([]db.MessageContext, error) {
	return nil, nil
}
func (*testReactorStore) DeleteMessageContext(context.Context, int64, int) error { return nil }
