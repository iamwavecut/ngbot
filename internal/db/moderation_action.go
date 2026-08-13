package db

import "time"

const (
	ModerationActionPending        = "pending"
	ModerationActionStarted        = "started"
	ModerationActionBanned         = "banned"
	ModerationActionCompleted      = "completed"
	ModerationActionReconciliation = "reconciliation"
)

type ModerationActionFence struct {
	ActionKey string    `db:"action_key"`
	ChatID    int64     `db:"chat_id"`
	UserID    int64     `db:"user_id"`
	MessageID int       `db:"message_id"`
	Status    string    `db:"status"`
	Owner     string    `db:"owner"`
	BanUntil  time.Time `db:"ban_until"`
	LastError string    `db:"last_error"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}
