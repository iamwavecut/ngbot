package gemini

import (
	"context"
	"fmt"

	"github.com/iamwavecut/ngbot/internal/adapters"
	"github.com/iamwavecut/ngbot/internal/adapters/llm"
	log "github.com/sirupsen/logrus"
	"google.golang.org/genai"
)

type API struct {
	client *genai.Client
	model  string
}

const (
	DefaultModel           = "gemini-2.5-flash-lite"
	defaultTemperature     = float32(0.9)
	defaultTopK            = float32(40)
	defaultTopP            = float32(0.95)
	defaultMaxOutputTokens = int32(8192)
)

func NewGemini(apiKey, model string, logger *log.Entry) (adapters.LLM, error) {
	_ = logger
	if apiKey == "" {
		return nil, fmt.Errorf("gemini API key is empty")
	}
	if model == "" {
		model = DefaultModel
	}

	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("create gemini client: %w", err)
	}

	return &API{client: client, model: model}, nil
}

func (g *API) ChatCompletion(ctx context.Context, messages []llm.ChatCompletionMessage) (llm.ChatCompletionResponse, error) {
	if len(messages) == 0 {
		return llm.ChatCompletionResponse{}, fmt.Errorf("chat completion requires at least one message")
	}

	contents := make([]*genai.Content, 0, len(messages))
	var systemInstruction *genai.Content

	for _, message := range messages {
		switch message.Role {
		case llm.RoleSystem:
			systemInstruction = genai.NewContentFromText(message.Content, genai.RoleUser)
		case llm.RoleAssistant:
			contents = append(contents, genai.NewContentFromText(message.Content, genai.RoleModel))
		case llm.RoleUser, "":
			contents = append(contents, genai.NewContentFromText(message.Content, genai.RoleUser))
		default:
			return llm.ChatCompletionResponse{}, fmt.Errorf("unsupported message role: %s", message.Role)
		}
	}

	if len(contents) == 0 {
		return llm.ChatCompletionResponse{}, fmt.Errorf("chat completion requires at least one non-system message")
	}

	config := &genai.GenerateContentConfig{
		SystemInstruction: systemInstruction,
		Temperature:       genai.Ptr(defaultTemperature),
		TopK:              genai.Ptr(defaultTopK),
		TopP:              genai.Ptr(defaultTopP),
		MaxOutputTokens:   defaultMaxOutputTokens,
		ResponseMIMEType:  "text/plain",
		SafetySettings:    defaultSafetySettings(),
	}

	resp, err := g.client.Models.GenerateContent(ctx, g.model, contents, config)
	if err != nil {
		return llm.ChatCompletionResponse{}, fmt.Errorf("generate gemini content: %w", err)
	}

	return llm.ChatCompletionResponse{
		Choices: []llm.ChatCompletionChoice{{
			Message: llm.ChatCompletionMessage{
				Role:    llm.RoleAssistant,
				Content: resp.Text(),
			},
		}},
	}, nil
}

func defaultSafetySettings() []*genai.SafetySetting {
	return []*genai.SafetySetting{
		{Category: genai.HarmCategoryHarassment, Threshold: genai.HarmBlockThresholdBlockNone},
		{Category: genai.HarmCategoryHateSpeech, Threshold: genai.HarmBlockThresholdBlockNone},
		{Category: genai.HarmCategorySexuallyExplicit, Threshold: genai.HarmBlockThresholdBlockNone},
		{Category: genai.HarmCategoryDangerousContent, Threshold: genai.HarmBlockThresholdBlockNone},
	}
}
