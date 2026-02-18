package adapters

import (
	"context"

	"github.com/iamwavecut/ngbot/internal/adapters/llm"
)

// LLM defines the interface for language model operations
type LLM interface {
	// ChatCompletion performs a chat completion request
	ChatCompletion(ctx context.Context, messages []llm.ChatCompletionMessage) (llm.ChatCompletionResponse, error)
}
