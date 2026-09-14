package db

import (
	"database/sql"
	"errors"
	"time"
)

const (
	MessageAuthorUser       = "user"
	MessageAuthorSenderChat = "sender_chat"
)

type (
	MessageAuthor struct {
		Kind string
		ID   int64
	}

	MessageTrust struct {
		ChatID       int64        `db:"chat_id"`
		AuthorKind   string       `db:"author_kind"`
		AuthorID     int64        `db:"author_id"`
		SafeMessages int          `db:"safe_messages"`
		TrustedUntil sql.NullTime `db:"trusted_until"`
		Suspended    bool         `db:"suspended"`
	}

	MessageContext struct {
		ChatID           int64     `db:"chat_id"`
		MessageID        int       `db:"message_id"`
		ThreadID         int       `db:"thread_id"`
		ReplyToMessageID int       `db:"reply_to_message_id"`
		AuthorKind       string    `db:"author_kind"`
		AuthorID         int64     `db:"author_id"`
		Text             string    `db:"text"`
		SentAt           time.Time `db:"sent_at"`
		UpdatedAt        time.Time `db:"updated_at"`
		UpdateID         int       `db:"update_id"`
	}
)

func (a MessageAuthor) Validate() error {
	if (a.Kind == MessageAuthorUser && a.ID > 0) || (a.Kind == MessageAuthorSenderChat && a.ID < 0) {
		return nil
	}
	return errors.New("invalid message author")
}

func (t *MessageTrust) Trusted(now time.Time) bool {
	return t != nil && !t.Suspended && t.TrustedUntil.Valid && t.TrustedUntil.Time.After(now)
}
