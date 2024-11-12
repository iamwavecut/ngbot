package db

import "time"

type SpamReport struct {
	UserID     int64     `db:"user_id"`
	ChatID     int64     `db:"chat_id"`
	MessageID  int       `db:"message_id"`
	SpamScore  float64   `db:"spam_score"`
	ReportedAt time.Time `db:"reported_at"`
}

type UserRestriction struct {
	UserID      int64     `db:"user_id"`
	ChatID      int64     `db:"chat_id"`
	RestrictedAt time.Time `db:"restricted_at"`
	ExpiresAt   time.Time `db:"expires_at"`
	Reason      string    `db:"reason"`
}
