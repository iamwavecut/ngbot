package openai

import (
	"context"

	"github.com/iamwavecut/ngbot/internal/adapters"
	"github.com/iamwavecut/ngbot/internal/adapters/llm"
	"github.com/sashabaranov/go-openai"
	log "github.com/sirupsen/logrus"
)

type API struct {
	client       *openai.Client
	systemPrompt string
	model        string
	parameters   *llm.GenerationParameters
	logger       *log.Entry
}

const DefaultModel = "gpt-4o-mini"

func NewOpenAI(apiKey, model, baseURL string, logger *log.Entry) adapters.LLM {
	config := openai.DefaultConfig(apiKey)
	config.BaseURL = baseURL
	client := openai.NewClientWithConfig(config)
	return (&API{client: client, logger: logger}).WithModel(model).WithParameters(nil)
}

func (o *API) WithModel(modelName string) adapters.LLM {
	if modelName == "" {
		modelName = DefaultModel
	}
	o.model = modelName
	return o
}

func (o *API) WithParameters(parameters *llm.GenerationParameters) adapters.LLM {
	if parameters == nil || parameters == (&llm.GenerationParameters{}) {
		parameters = &llm.GenerationParameters{
			Temperature:     0.9,
			TopP:            0.9,
			TopK:            50,
			MaxOutputTokens: 8192,
		}
	}
	o.parameters = parameters
	return o
}

func (o *API) WithSystemPrompt(prompt string) adapters.LLM {
	o.systemPrompt = prompt
	return o
}

func (o *API) ChatCompletion(ctx context.Context, messages []llm.ChatCompletionMessage) (llm.ChatCompletionResponse, error) {
	var openaiMessages []openai.ChatCompletionMessage
	systemPrompt := o.systemPrompt

	for _, msg := range messages {
		if msg.Role == openai.ChatMessageRoleSystem {
			systemPrompt = msg.Content
			continue
		}
		openaiMessages = append(openaiMessages, openai.ChatCompletionMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	openaiMessages = append([]openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleSystem,
			Content: systemPrompt,
		},
	}, openaiMessages...)

	resp, err := o.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:       o.model,
		Messages:    openaiMessages,
		Temperature: float32(o.parameters.Temperature),
		TopP:        float32(o.parameters.TopP),
		MaxTokens:   int(o.parameters.MaxOutputTokens),
	})
	if err != nil {
		return llm.ChatCompletionResponse{}, err
	}

	if len(resp.Choices) == 0 {
		return llm.ChatCompletionResponse{}, nil
	}

	return llm.ChatCompletionResponse{
		Choices: []llm.ChatCompletionChoice{
			{
				Message: llm.ChatCompletionMessage{
					Role:    resp.Choices[0].Message.Role,
					Content: resp.Choices[0].Message.Content,
				},
			},
		},
	}, nil

}
