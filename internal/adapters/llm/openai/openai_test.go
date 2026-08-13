package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iamwavecut/ngbot/internal/adapters/llm"
	openaisdk "github.com/sashabaranov/go-openai"
	log "github.com/sirupsen/logrus"
)

func TestChatCompletionSendsOpenAIClassificationRequest(t *testing.T) {
	t.Parallel()

	requestSeen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestSeen = true
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("request path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q", got)
		}
		var request openaisdk.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Model != DefaultModel {
			t.Fatalf("model = %q, want %q", request.Model, DefaultModel)
		}
		if len(request.Messages) != 2 || request.Messages[0].Role != openaisdk.ChatMessageRoleSystem || request.Messages[0].Content != "policy" || request.Messages[1].Role != openaisdk.ChatMessageRoleUser || request.Messages[1].Content != "candidate" {
			t.Fatalf("messages = %#v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(openaisdk.ChatCompletionResponse{
			Choices: []openaisdk.ChatCompletionChoice{{
				Message: openaisdk.ChatCompletionMessage{Role: openaisdk.ChatMessageRoleAssistant, Content: "1"},
			}},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	adapter, err := NewOpenAI("test-key", "", server.URL+"/v1", log.NewEntry(log.New()))
	if err != nil {
		t.Fatalf("NewOpenAI returned error: %v", err)
	}
	response, err := adapter.ChatCompletion(t.Context(), []llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: "policy", Cacheable: true},
		{Role: llm.RoleUser, Content: "candidate"},
	})
	if err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}
	if !requestSeen {
		t.Fatal("local OpenAI request was not observed")
	}
	if len(response.Choices) != 1 || response.Choices[0].Message.Content != "1" {
		t.Fatalf("response = %#v", response)
	}
}

func TestChatCompletionRejectsUnsupportedRoleWithoutRequest(t *testing.T) {
	t.Parallel()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requestCount++
	}))
	t.Cleanup(server.Close)

	adapter, err := NewOpenAI("test-key", "", server.URL+"/v1", log.NewEntry(log.New()))
	if err != nil {
		t.Fatalf("NewOpenAI returned error: %v", err)
	}
	_, err = adapter.ChatCompletion(t.Context(), []llm.ChatCompletionMessage{{Role: "tool", Content: "payload"}})
	if err == nil {
		t.Fatal("expected unsupported role to fail")
	}
	if requestCount != 0 {
		t.Fatalf("unsupported role made %d HTTP requests", requestCount)
	}
}

func TestChatCompletionClassifiesContentPolicyWithoutLeakingProviderMessage(t *testing.T) {
	t.Parallel()
	const providerSecret = "openai-policy-secret"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(openaisdk.ChatCompletionResponse{
			Choices: []openaisdk.ChatCompletionChoice{{FinishReason: openaisdk.FinishReasonContentFilter}},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	adapter, err := NewOpenAI("test-key", "", server.URL+"/v1", log.NewEntry(log.New()))
	if err != nil {
		t.Fatalf("NewOpenAI returned error: %v", err)
	}
	_, err = adapter.ChatCompletion(t.Context(), []llm.ChatCompletionMessage{{Role: llm.RoleUser, Content: providerSecret}})
	if got := llm.FailureKindOf(err); got != llm.FailurePolicyBlocked {
		t.Fatalf("policy failure kind = %q, want %q", got, llm.FailurePolicyBlocked)
	}
	if strings.Contains(err.Error(), providerSecret) {
		t.Fatalf("policy failure leaked request content: %v", err)
	}
}
