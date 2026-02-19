package handlers

import (
	"context"
	"testing"

	"github.com/iamwavecut/ngbot/internal/adapters/llm"
	log "github.com/sirupsen/logrus"
)

type spamDetectorTestLLM struct {
	lastMessages []llm.ChatCompletionMessage
	response     llm.ChatCompletionResponse
}

func (s *spamDetectorTestLLM) ChatCompletion(_ context.Context, messages []llm.ChatCompletionMessage) (llm.ChatCompletionResponse, error) {
	s.lastMessages = append([]llm.ChatCompletionMessage{}, messages...)
	return s.response, nil
}

func TestSpamDetectorIncludesExtraExamplesInPrompt(t *testing.T) {
	t.Parallel()

	llmStub := &spamDetectorTestLLM{
		response: llm.ChatCompletionResponse{
			Choices: []llm.ChatCompletionChoice{
				{Message: llm.ChatCompletionMessage{Role: "assistant", Content: "0"}},
			},
		},
	}
	detector := NewSpamDetector(llmStub, log.New().WithField("test", "spam_detector"))

	candidate := "candidate message"
	extra := "custom spam example"
	result, err := detector.IsSpam(context.Background(), candidate, []string{extra, " ", ""})
	if err != nil {
		t.Fatalf("IsSpam returned error: %v", err)
	}
	if result == nil || *result {
		t.Fatalf("expected non-spam result, got %v", result)
	}

	if len(llmStub.lastMessages) < 3 {
		t.Fatalf("expected prompt to contain extra examples and candidate message, got %d messages", len(llmStub.lastMessages))
	}
	tail := llmStub.lastMessages[len(llmStub.lastMessages)-3:]
	if tail[0].Role != "user" || tail[0].Content != extra {
		t.Fatalf("expected extra example user message, got %#v", tail[0])
	}
	if tail[1].Role != "assistant" || tail[1].Content != "1" {
		t.Fatalf("expected extra example assistant response \"1\", got %#v", tail[1])
	}
	if tail[2].Role != "user" || tail[2].Content != candidate {
		t.Fatalf("expected candidate message at tail, got %#v", tail[2])
	}
}
