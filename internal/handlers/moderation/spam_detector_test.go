package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

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
				{Message: llm.ChatCompletionMessage{Role: llm.RoleAssistant, Content: "0"}},
			},
		},
	}
	detector := NewSpamDetector(llmStub, log.New().WithField("test", "spam_detector"), time.Minute)

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
	if !llmStub.lastMessages[0].Cacheable {
		t.Fatalf("expected system prompt to be cacheable")
	}
	if len(llmStub.lastMessages) < 3 || !llmStub.lastMessages[1].Cacheable || !llmStub.lastMessages[2].Cacheable {
		t.Fatalf("expected built-in few-shot example prefix to be cacheable")
	}
	tail := llmStub.lastMessages[len(llmStub.lastMessages)-3:]
	if tail[0].Role != llm.RoleUser || tail[0].Content != extra {
		t.Fatalf("expected extra example user message, got %#v", tail[0])
	}
	if tail[0].Cacheable {
		t.Fatalf("expected extra example user message to stay live")
	}
	if tail[1].Role != llm.RoleAssistant || tail[1].Content != "1" {
		t.Fatalf("expected extra example assistant response \"1\", got %#v", tail[1])
	}
	if tail[1].Cacheable {
		t.Fatalf("expected extra example assistant response to stay live")
	}
	if tail[2].Role != llm.RoleUser || tail[2].Content != candidate {
		t.Fatalf("expected candidate message at tail, got %#v", tail[2])
	}
	if tail[2].Cacheable {
		t.Fatalf("expected candidate message to stay live")
	}
}

func TestSpamDetectorUsesReportedPromptForReportedSpam(t *testing.T) {
	t.Parallel()

	llmStub := &spamDetectorTestLLM{
		response: llm.ChatCompletionResponse{
			Choices: []llm.ChatCompletionChoice{
				{Message: llm.ChatCompletionMessage{Role: llm.RoleAssistant, Content: "1"}},
			},
		},
	}
	detector := NewSpamDetector(llmStub, log.New().WithField("test", "spam_detector"), time.Minute)

	candidate := "reported message"
	result, err := detector.IsReportedSpam(context.Background(), candidate, nil)
	if err != nil {
		t.Fatalf("IsReportedSpam returned error: %v", err)
	}
	if result == nil || !*result {
		t.Fatalf("expected reported spam result, got %v", result)
	}
	if len(llmStub.lastMessages) == 0 {
		t.Fatal("expected reported spam check to call LLM")
	}
	systemPrompt := llmStub.lastMessages[0].Content
	if systemPrompt == spamDetectionPrompt {
		t.Fatal("expected reported spam check to use a report-specific prompt")
	}
	if !strings.Contains(strings.ToLower(systemPrompt), "reported") && !strings.Contains(strings.ToLower(systemPrompt), "зарепорч") {
		t.Fatalf("expected report-specific prompt to mention reported spam, got %q", systemPrompt)
	}
	if got := llmStub.lastMessages[len(llmStub.lastMessages)-1].Content; got != candidate {
		t.Fatalf("expected reported candidate at tail, got %q", got)
	}
}
