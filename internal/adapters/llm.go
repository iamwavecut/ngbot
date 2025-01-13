package adapters

import (
	"context"

	"github.com/iamwavecut/ngbot/internal/adapters/llm"
)

// LLM defines the interface for language model operations
type LLM interface {
	// Detect checks if a message is spam
	Detect(ctx context.Context, message string) (*bool, error)
	// WithModel sets the model to use
	WithModel(modelName string) LLM
	// WithParameters sets the generation parameters
	WithParameters(parameters *llm.GenerationParameters) LLM
	// WithSystemPrompt sets the system prompt
	WithSystemPrompt(prompt string) LLM
	// ChatCompletion performs a chat completion request
	ChatCompletion(ctx context.Context, messages []llm.ChatCompletionMessage) (llm.ChatCompletionResponse, error)
}
