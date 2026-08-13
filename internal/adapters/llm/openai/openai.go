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
	logger *log.Entry
}

const (
	DefaultModel           = "gpt-4o-mini"
	defaultTemperature     = 0.0
	defaultTopP            = 1.0
	defaultMaxOutputTokens = 4
)

func NewOpenAI(apiKey, model, baseURL string, logger *log.Entry) (adapters.LLM, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("openai API key is empty")
	}
	if model == "" {
		model = DefaultModel
	}
	if logger == nil {
		logger = log.New().WithField("adapter", "openai")
	}

	config := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		config.BaseURL = baseURL
	}

	return &API{
		client: openai.NewClientWithConfig(config),
		model:  model,
		logger: logger.WithFields(log.Fields{"provider": "openai", "model": model}),
	}, nil
}

func (o *API) ChatCompletion(ctx context.Context, messages []llm.ChatCompletionMessage) (llm.ChatCompletionResponse, error) {
	if len(messages) == 0 {
		return llm.ChatCompletionResponse{}, fmt.Errorf("chat completion requires at least one message")
	}

	openaiMessages := make([]openai.ChatCompletionMessage, 0, len(messages)+1)
	systemPrompt := ""
	seenConversation := false

	for _, msg := range messages {
		switch msg.Role {
		case llm.RoleSystem:
			if seenConversation {
				return llm.ChatCompletionResponse{}, fmt.Errorf("system message must precede conversation contents")
			}
			systemPrompt = msg.Content
		case llm.RoleUser, llm.RoleAssistant, "":
			seenConversation = true
			role := msg.Role
			if role == "" {
				role = llm.RoleUser
			}
			openaiMessages = append(openaiMessages, openai.ChatCompletionMessage{
				Role:    role,
				Content: msg.Content,
			})
		default:
			return llm.ChatCompletionResponse{}, fmt.Errorf("unsupported message role: %s", msg.Role)
		}
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
	o.logger.WithFields(log.Fields{
		"prompt_tokens":     resp.Usage.PromptTokens,
		"completion_tokens": resp.Usage.CompletionTokens,
		"total_tokens":      resp.Usage.TotalTokens,
	}).Debug("OpenAI usage metadata")

	return llm.ChatCompletionResponse{
		Choices: []llm.ChatCompletionChoice{{
			Message: llm.ChatCompletionMessage{
				Role:    resp.Choices[0].Message.Role,
				Content: resp.Choices[0].Message.Content,
			},
		}},
	}, nil
}
