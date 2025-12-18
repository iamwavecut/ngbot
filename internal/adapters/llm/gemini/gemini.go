package gemini

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"github.com/iamwavecut/ngbot/internal/adapters"
	"github.com/iamwavecut/ngbot/internal/adapters/llm"
	log "github.com/sirupsen/logrus"
	"google.golang.org/api/option"
)

type API struct {
	client *genai.Client
	model  *genai.GenerativeModel
	logger *log.Entry
}

const DefaultModel = "gemini-2.5-flash-lite"

func NewGemini(apiKey, model string, logger *log.Entry) adapters.LLM {
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		logger.Fatalf("Error creating client: %v", err)
	}
	api := &API{
		client: client,
		logger: logger,
		model:  client.GenerativeModel(model),
	}
	api.WithSafetySettings(nil)
	api.WithParameters(nil)
	return api
}

func (g *API) WithModel(modelName string) adapters.LLM {
	if modelName == "" {
		modelName = DefaultModel
	}
	model := g.client.GenerativeModel(modelName)
	g.model = model
	return g
}

func (g *API) WithParameters(parameters *llm.GenerationParameters) adapters.LLM {
	if parameters == nil || (parameters == &llm.GenerationParameters{}) {
		parameters = &llm.GenerationParameters{
			Temperature:      0.9,
			TopK:             40,
			TopP:             0.95,
			MaxOutputTokens:  8192,
			ResponseMIMEType: "text/plain",
		}
	}

	g.model.SetTemperature(parameters.Temperature)
	g.model.SetTopK(parameters.TopK)
	g.model.SetTopP(parameters.TopP)
	g.model.SetMaxOutputTokens(int32(parameters.MaxOutputTokens))
	g.model.ResponseMIMEType = parameters.ResponseMIMEType

	return g
}

func (g *API) WithSafetySettings(safetySettings []*genai.SafetySetting) *API {
	if len(safetySettings) == 0 {
		safetySettings = []*genai.SafetySetting{
			{
				Category:  genai.HarmCategoryDangerous,
				Threshold: genai.HarmBlockNone,
			},
			{
				Category:  genai.HarmCategoryDangerousContent,
				Threshold: genai.HarmBlockNone,
			},
			{
				Category:  genai.HarmCategoryDerogatory,
				Threshold: genai.HarmBlockNone,
			},
			{
				Category:  genai.HarmCategoryHarassment,
				Threshold: genai.HarmBlockNone,
			},
			{
				Category:  genai.HarmCategoryHateSpeech,
				Threshold: genai.HarmBlockNone,
			},
			{
				Category:  genai.HarmCategoryMedical,
				Threshold: genai.HarmBlockNone,
			},
			{
				Category:  genai.HarmCategorySexual,
				Threshold: genai.HarmBlockNone,
			},
			{
				Category:  genai.HarmCategorySexuallyExplicit,
				Threshold: genai.HarmBlockNone,
			},
			{
				Category:  genai.HarmCategoryToxicity,
				Threshold: genai.HarmBlockNone,
			},
			{
				Category:  genai.HarmCategoryViolence,
				Threshold: genai.HarmBlockNone,
			},
			{
				Category:  genai.HarmCategoryUnspecified,
				Threshold: genai.HarmBlockNone,
			},
		}
	}
	g.model.SafetySettings = safetySettings
	return g
}

func (g *API) WithSystemPrompt(prompt string) adapters.LLM {
	g.model.SystemInstruction = &genai.Content{
		Parts: []genai.Part{genai.Text(prompt)},
	}
	return g
}

func (g *API) ChatCompletion(ctx context.Context, messages []llm.ChatCompletionMessage) (llm.ChatCompletionResponse, error) {
	session := g.model.StartChat()
	session.History = []*genai.Content{}

	lastMessage, messages := messages[len(messages)-1], messages[:len(messages)-1]

	backupGlobalInstruction := g.model.SystemInstruction
	for _, message := range messages {
		if message.Role == "system" {
			g.model.SystemInstruction = &genai.Content{
				Parts: []genai.Part{genai.Text(message.Content)},
			}
			continue
		}
		session.History = append(session.History, &genai.Content{
			Parts: []genai.Part{genai.Text(message.Content)},
		})
	}

	resp, err := session.SendMessage(ctx, genai.Text(lastMessage.Content))
	if err != nil {
		return llm.ChatCompletionResponse{}, err
	}
	g.model.SystemInstruction = backupGlobalInstruction

	response := ""
	for _, part := range resp.Candidates[0].Content.Parts {
		response += fmt.Sprintf("%v", part)
	}

	return llm.ChatCompletionResponse{
		Choices: []llm.ChatCompletionChoice{{Message: llm.ChatCompletionMessage{Content: response}}},
	}, nil
}

// Detect implements the LLM interface
func (g *API) Detect(ctx context.Context, message string) (*bool, error) {
	messages := []llm.ChatCompletionMessage{
		{
			Role:    "system",
			Content: "You are a spam detection system. Analyze the following message and respond with true if it's spam, false if it's not. Consider advertising, scams, and inappropriate content as spam.",
		},
		{
			Role:    "user",
			Content: message,
		},
	}

	resp, err := g.ChatCompletion(ctx, messages)
	if err != nil {
		return nil, err
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response choices available")
	}

	result := strings.ToLower(strings.TrimSpace(resp.Choices[0].Message.Content)) == "true"
	return &result, nil
}
