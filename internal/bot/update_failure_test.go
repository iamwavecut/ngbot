package bot

import (
	"errors"
	"fmt"
	"testing"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/adapters/llm"
)

type codedSQLiteError struct {
	code int
}

func (e codedSQLiteError) Error() string { return "sqlite failure" }
func (e codedSQLiteError) Code() int     { return e.code }

func TestClassifyUpdateFailureUsesTypedDependencyPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         error
		source      UpdateFailureSource
		disposition UpdateFailureDisposition
	}{
		{name: "sqlite busy", err: codedSQLiteError{code: 5}, source: UpdateFailureSQLite, disposition: UpdateFailureRetryable},
		{name: "sqlite constraint", err: codedSQLiteError{code: 19}, source: UpdateFailureSQLite, disposition: UpdateFailureTerminal},
		{name: "telegram rate limit", err: api.Error{Code: 429, Message: "Too Many Requests"}, source: UpdateFailureTelegram, disposition: UpdateFailureRetryable},
		{name: "telegram server", err: api.Error{Code: 502, Message: "Bad Gateway"}, source: UpdateFailureTelegram, disposition: UpdateFailureRetryable},
		{name: "telegram bad request", err: api.Error{Code: 400, Message: "Bad Request"}, source: UpdateFailureTelegram, disposition: UpdateFailureTerminal},
		{name: "telegram wrapped pointer rate limit", err: fmt.Errorf("send: %w", &api.Error{Code: 429, Message: "Too Many Requests"}), source: UpdateFailureTelegram, disposition: UpdateFailureRetryable},
		{name: "telegram wrapped pointer server", err: fmt.Errorf("send: %w", &api.Error{Code: 503, Message: "Unavailable"}), source: UpdateFailureTelegram, disposition: UpdateFailureRetryable},
		{name: "telegram wrapped pointer bad request", err: fmt.Errorf("send: %w", &api.Error{Code: 400, Message: "Bad Request"}), source: UpdateFailureTelegram, disposition: UpdateFailureTerminal},
		{name: "llm timeout", err: llm.NewFailure(llm.FailureTimeout, errors.New("timeout")), source: UpdateFailureLLM, disposition: UpdateFailureRetryable},
		{name: "llm malformed", err: llm.NewFailure(llm.FailureMalformedOutput, errors.New("empty")), source: UpdateFailureLLM, disposition: UpdateFailureRetryable},
		{name: "llm policy", err: llm.NewFailure(llm.FailurePolicyBlocked, errors.New("blocked")), source: UpdateFailureLLM, disposition: UpdateFailureTerminal},
		{name: "capability unknown", err: NewRetryableUpdateFailure(UpdateFailureCapability, "capability_unknown", errors.New("lookup failed")), source: UpdateFailureCapability, disposition: UpdateFailureRetryable},
		{name: "explicit terminal", err: NewTerminalUpdateFailure(UpdateFailurePayload, "malformed_update", errors.New("empty")), source: UpdateFailurePayload, disposition: UpdateFailureTerminal},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			failure := ClassifyUpdateFailure(test.err)
			if failure.Source != test.source || failure.Disposition != test.disposition {
				t.Fatalf("classification = %#v, want source=%q disposition=%q", failure, test.source, test.disposition)
			}
		})
	}
}
