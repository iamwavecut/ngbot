package db

import (
	"context"
	"errors"
	"strings"
)

const (
	GatekeeperErrorAmbiguousExternalEffect = "ambiguous_external_effect"
	GatekeeperErrorCanceled                = "operation_canceled"
	GatekeeperErrorConversationUnavailable = "conversation_unavailable"
	GatekeeperErrorDeadline                = "deadline_exceeded"
	GatekeeperErrorPermission              = "permission_denied"
	GatekeeperErrorQueryExpired            = "query_expired"
	GatekeeperErrorRetryExhausted          = "retry_exhausted"
	GatekeeperErrorStateConflict           = "state_conflict"
	GatekeeperErrorTelegram                = "telegram_error"
	GatekeeperErrorUnavailable             = "dependency_unavailable"
)

func SafeGatekeeperErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return GatekeeperErrorDeadline
	}
	if errors.Is(err, context.Canceled) {
		return GatekeeperErrorCanceled
	}
	return SafeGatekeeperErrorText(err.Error())
}

func SafeGatekeeperErrorText(value string) string {
	upper := strings.ToUpper(value)
	switch {
	case value == "":
		return ""
	case isSafeGatekeeperErrorCode(value):
		return value
	case strings.Contains(upper, "QUERY_ID_INVALID"), strings.Contains(upper, "QUERY IS TOO OLD"):
		return GatekeeperErrorQueryExpired
	case strings.Contains(upper, "CHAT_ADMIN_REQUIRED"), strings.Contains(upper, "NOT ENOUGH RIGHTS"), strings.Contains(upper, "NO PRIVILEGES"), strings.Contains(upper, "BOT IS NOT AN ADMINISTRATOR"):
		return GatekeeperErrorPermission
	case strings.Contains(upper, "BOT CAN'T INITIATE CONVERSATION"), strings.Contains(upper, "BOT_CANT_INITIATE_CONVERSATION"), strings.Contains(upper, "BOT WAS BLOCKED BY THE USER"), strings.Contains(upper, "USER IS DEACTIVATED"):
		return GatekeeperErrorConversationUnavailable
	case strings.Contains(upper, "DEADLINE EXCEEDED"), strings.Contains(upper, "TIMEOUT"):
		return GatekeeperErrorDeadline
	case strings.Contains(upper, "CANCELED"):
		return GatekeeperErrorCanceled
	case strings.Contains(upper, "FENCE"), strings.Contains(upper, "CHANGED BEFORE"), strings.Contains(upper, "LEASE"):
		return GatekeeperErrorStateConflict
	case strings.Contains(upper, "RETRIES EXHAUSTED"):
		return GatekeeperErrorRetryExhausted
	case strings.Contains(upper, "TELEGRAM"), strings.Contains(upper, "BAD REQUEST"), strings.Contains(upper, "FORBIDDEN"):
		return GatekeeperErrorTelegram
	default:
		return GatekeeperErrorUnavailable
	}
}

func isSafeGatekeeperErrorCode(value string) bool {
	switch value {
	case GatekeeperErrorAmbiguousExternalEffect,
		GatekeeperErrorCanceled,
		GatekeeperErrorConversationUnavailable,
		GatekeeperErrorDeadline,
		GatekeeperErrorPermission,
		GatekeeperErrorQueryExpired,
		GatekeeperErrorRetryExhausted,
		GatekeeperErrorStateConflict,
		GatekeeperErrorTelegram,
		GatekeeperErrorUnavailable:
		return true
	default:
		return false
	}
}
