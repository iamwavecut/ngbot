package handlers

import (
	"context"
	"github.com/pkg/errors"
	"github.com/sashabaranov/go-openai"
)

type openAISpamDetector struct {
	client *openai.Client
	model  string
}

func (d *openAISpamDetector) IsSpam(ctx context.Context, message string) (bool, error) {
	resp, err := d.client.CreateChatCompletion(
		ctx,
		openai.ChatCompletionRequest{
			Model:       d.model,
			Temperature: 0.02,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleSystem,
					Content: spamDetectionPrompt,
				},
				{
					Role:    openai.ChatMessageRoleUser,
					Content: message,
				},
			},
		},
	)

	if err != nil {
		return false, errors.Wrap(err, "failed to check spam with OpenAI")
	}

	return len(resp.Choices) > 0 && resp.Choices[0].Message.Content == "SPAM", nil
}

const spamDetectionPrompt = `Ты ассистент для обнаружения спама, анализирующий сообщения на различных языках...`
