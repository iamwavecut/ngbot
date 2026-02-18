package openai

import (
	"context"
	"fmt"

	"github.com/iamwavecut/ngbot/internal/adapters"
	"github.com/iamwavecut/ngbot/internal/adapters/llm"
	"github.com/sashabaranov/go-openai"
	log "github.com/sirupsen/logrus"
)

type API struct {
	client *openai.Client
	model  string
}

const (
	DefaultModel           = "gpt-4o-mini"
	defaultTemperature     = 0.9
	defaultTopP            = 0.9
	defaultMaxOutputTokens = 8192
)

func NewOpenAI(apiKey, model, baseURL string, logger *log.Entry) (adapters.LLM, error) {
	_ = logger
	if apiKey == "" {
		return nil, fmt.Errorf("openai API key is empty")
	}
	if model == "" {
		model = DefaultModel
	}

	config := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		config.BaseURL = baseURL
	}

	return &API{
		client: openai.NewClientWithConfig(config),
		model:  model,
	}, nil
}

func (o *API) ChatCompletion(ctx context.Context, messages []llm.ChatCompletionMessage) (llm.ChatCompletionResponse, error) {
	if len(messages) == 0 {
		return llm.ChatCompletionResponse{}, fmt.Errorf("chat completion requires at least one message")
	}

	openaiMessages := make([]openai.ChatCompletionMessage, 0, len(messages)+1)
	systemPrompt := ""

	for _, msg := range messages {
		if msg.Role == llm.RoleSystem {
			systemPrompt = msg.Content
			continue
		}
		openaiMessages = append(openaiMessages, openai.ChatCompletionMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	if systemPrompt != "" {
		openaiMessages = append([]openai.ChatCompletionMessage{{
			Role:    openai.ChatMessageRoleSystem,
			Content: systemPrompt,
		}}, openaiMessages...)
	}

	resp, err := o.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:       o.model,
		Messages:    openaiMessages,
		Temperature: defaultTemperature,
		TopP:        defaultTopP,
		MaxTokens:   defaultMaxOutputTokens,
	})
	if err != nil {
		return llm.ChatCompletionResponse{}, fmt.Errorf("create openai chat completion: %w", err)
	}

	if len(resp.Choices) == 0 {
		return llm.ChatCompletionResponse{}, nil
	}

	return llm.ChatCompletionResponse{
		Choices: []llm.ChatCompletionChoice{{
			Message: llm.ChatCompletionMessage{
				Role:    resp.Choices[0].Message.Role,
				Content: resp.Choices[0].Message.Content,
			},
		}},
	}, nil
}
