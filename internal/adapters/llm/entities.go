package llm

import (
	"context"
	"errors"
)

const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

type ChatCompletionMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Cacheable bool   `json:"cacheable,omitempty"`
}

type ChatCompletionResponse struct {
	Choices []ChatCompletionChoice `json:"choices"`
}

type ChatCompletionChoice struct {
	Message ChatCompletionMessage `json:"message"`
}

type FailureKind string

const (
	FailureTimeout         FailureKind = "timeout"
	FailureMalformedOutput FailureKind = "malformed_output"
	FailurePolicyBlocked   FailureKind = "policy_blocked"
	FailureProvider        FailureKind = "provider_error"
)

type Failure struct {
	Kind  FailureKind
	cause error
}

func NewFailure(kind FailureKind, cause error) error {
	if cause == nil {
		return nil
	}
	return &Failure{Kind: kind, cause: cause}
}

func (f *Failure) Error() string {
	return "LLM classification failed: " + string(f.Kind)
}

func (f *Failure) Unwrap() error {
	return f.cause
}

func FailureKindOf(err error) FailureKind {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Kind
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return FailureTimeout
	}
	return FailureProvider
}
