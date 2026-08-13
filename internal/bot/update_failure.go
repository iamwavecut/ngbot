package bot

import (
	"context"
	"errors"
	"strings"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/adapters/llm"
)

type UpdateFailureSource string

const (
	UpdateFailureSQLite     UpdateFailureSource = "sqlite"
	UpdateFailureLLM        UpdateFailureSource = "llm"
	UpdateFailureTelegram   UpdateFailureSource = "telegram"
	UpdateFailureCapability UpdateFailureSource = "capability"
	UpdateFailurePayload    UpdateFailureSource = "payload"
	UpdateFailureRuntime    UpdateFailureSource = "runtime"
)

type UpdateFailureDisposition string

const (
	UpdateFailureRetryable UpdateFailureDisposition = "retryable"
	UpdateFailureTerminal  UpdateFailureDisposition = "terminal"
)

type UpdateFailure struct {
	Source      UpdateFailureSource
	Disposition UpdateFailureDisposition
	Reason      string
	Cause       error
}

func NewRetryableUpdateFailure(source UpdateFailureSource, reason string, cause error) error {
	return newUpdateFailure(source, UpdateFailureRetryable, reason, cause)
}

func NewTerminalUpdateFailure(source UpdateFailureSource, reason string, cause error) error {
	return newUpdateFailure(source, UpdateFailureTerminal, reason, cause)
}

func newUpdateFailure(source UpdateFailureSource, disposition UpdateFailureDisposition, reason string, cause error) error {
	if cause == nil {
		return nil
	}
	return &UpdateFailure{Source: source, Disposition: disposition, Reason: reason, Cause: cause}
}

func (f *UpdateFailure) Error() string {
	return string(f.Source) + " update failure: " + f.Reason
}

func (f *UpdateFailure) Unwrap() error {
	return f.Cause
}

func ClassifyUpdateFailure(err error) UpdateFailure {
	var failure *UpdateFailure
	if errors.As(err, &failure) {
		return *failure
	}

	var llmFailure *llm.Failure
	if errors.As(err, &llmFailure) {
		disposition := UpdateFailureRetryable
		if llmFailure.Kind == llm.FailurePolicyBlocked {
			disposition = UpdateFailureTerminal
		}
		return UpdateFailure{
			Source:      UpdateFailureLLM,
			Disposition: disposition,
			Reason:      string(llmFailure.Kind),
			Cause:       err,
		}
	}

	var telegramError api.Error
	if errors.As(err, &telegramError) {
		disposition := UpdateFailureTerminal
		if telegramError.Code == 429 || telegramError.Code >= 500 {
			disposition = UpdateFailureRetryable
		}
		return UpdateFailure{
			Source:      UpdateFailureTelegram,
			Disposition: disposition,
			Reason:      telegramFailureReason(telegramError),
			Cause:       err,
		}
	}

	var sqliteError interface{ Code() int }
	if errors.As(err, &sqliteError) {
		code := sqliteError.Code() & 0xff
		disposition := UpdateFailureTerminal
		switch code {
		case 5, 6, 9, 10, 13, 14, 15:
			disposition = UpdateFailureRetryable
		}
		return UpdateFailure{
			Source:      UpdateFailureSQLite,
			Disposition: disposition,
			Reason:      "sqlite_error",
			Cause:       err,
		}
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return UpdateFailure{Source: UpdateFailureRuntime, Disposition: UpdateFailureRetryable, Reason: "context_interrupted", Cause: err}
	}

	return UpdateFailure{Source: UpdateFailureRuntime, Disposition: UpdateFailureRetryable, Reason: "unclassified_error", Cause: err}
}

func telegramFailureReason(err api.Error) string {
	message := strings.ToUpper(err.Message)
	for _, marker := range []string{"CHAT_ADMIN_REQUIRED", "NOT ENOUGH RIGHTS", "BOT IS NOT AN ADMINISTRATOR", "NEED ADMINISTRATOR RIGHTS"} {
		if strings.Contains(message, marker) {
			return "permission_denied"
		}
	}
	if err.Code == 429 {
		return "rate_limited"
	}
	if err.Code >= 500 {
		return "server_error"
	}
	return "request_rejected"
}
