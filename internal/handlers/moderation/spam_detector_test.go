package handlers

import (
	"bytes"
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

	if len(llmStub.lastMessages) != 2 {
		t.Fatalf("expected one static instruction and one live candidate, got %d messages", len(llmStub.lastMessages))
	}
	if !llmStub.lastMessages[0].Cacheable {
		t.Fatalf("expected system prompt to be cacheable")
	}
	for _, message := range llmStub.lastMessages {
		if message.Role == llm.RoleAssistant {
			t.Fatalf("classification prompt must not contain prefilled assistant turns: %#v", message)
		}
	}
	if !strings.Contains(llmStub.lastMessages[0].Content, extra) {
		t.Fatal("expected custom spam example in the static classification instruction")
	}
	tail := llmStub.lastMessages[len(llmStub.lastMessages)-1]
	if tail.Role != llm.RoleUser || tail.Content != candidate {
		t.Fatalf("expected candidate message at tail, got %#v", tail)
	}
	if tail.Cacheable {
		t.Fatalf("expected candidate message to stay live")
	}
}

func TestSpamDetectorRejectsMalformedOutputWithoutLeakingIt(t *testing.T) {
	t.Parallel()

	const malformed = "classification-secret: result=1"
	var logs bytes.Buffer
	logger := log.New()
	logger.SetOutput(&logs)
	logger.SetLevel(log.DebugLevel)
	detector := NewSpamDetector(&spamDetectorTestLLM{
		response: llm.ChatCompletionResponse{
			Choices: []llm.ChatCompletionChoice{{
				Message: llm.ChatCompletionMessage{Role: llm.RoleAssistant, Content: malformed},
			}},
		},
	}, log.NewEntry(logger), time.Minute)

	result, err := detector.IsSpam(t.Context(), "candidate", nil)
	if err == nil {
		t.Fatal("expected malformed model output to fail closed")
	}
	if result != nil {
		t.Fatalf("malformed model output produced classification: %v", result)
	}
	if strings.Contains(err.Error(), malformed) || strings.Contains(logs.String(), malformed) {
		t.Fatalf("malformed model output leaked into diagnostics: err=%v logs=%q", err, logs.String())
	}
}

func TestSpamDetectorAcceptsTrimmedBinaryOutput(t *testing.T) {
	t.Parallel()

	detector := NewSpamDetector(&spamDetectorTestLLM{
		response: llm.ChatCompletionResponse{
			Choices: []llm.ChatCompletionChoice{{
				Message: llm.ChatCompletionMessage{Role: llm.RoleAssistant, Content: "\n 1 \t"},
			}},
		},
	}, log.New().WithField("test", "spam_detector"), time.Minute)

	result, err := detector.IsSpam(t.Context(), "candidate", nil)
	if err != nil {
		t.Fatalf("IsSpam returned error: %v", err)
	}
	if result == nil || !*result {
		t.Fatalf("expected spam result, got %v", result)
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
