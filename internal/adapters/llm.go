package adapters

import (
	"context"

	"github.com/iamwavecut/ngbot/internal/adapters/llm"
)

type LLM interface {
	ChatCompletion(ctx context.Context, messages []llm.ChatCompletionMessage) (llm.ChatCompletionResponse, error)
	WithModel(modelName string) LLM
	WithParameters(parameters *llm.GenerationParameters) LLM
	WithSystemPrompt(prompt string) LLM
}
