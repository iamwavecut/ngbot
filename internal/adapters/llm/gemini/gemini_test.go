package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/iamwavecut/ngbot/internal/adapters/llm"
	log "github.com/sirupsen/logrus"
	"google.golang.org/genai"
)

const (
	testSystemPrompt = "system"
	testCandidate    = "candidate"
)

func TestSplitPromptSegmentsKeepsCacheableMessagesInRequest(t *testing.T) {
	t.Parallel()

	segments, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: "static-user", Cacheable: true},
		{Role: llm.RoleUser, Content: testCandidate},
	})
	if err != nil {
		t.Fatalf("splitPromptSegments returned error: %v", err)
	}

	if got := contentText(segments.systemInstruction); got != testSystemPrompt {
		t.Fatalf("system instruction = %q, want %q", got, testSystemPrompt)
	}
	if len(segments.contents) != 2 || contentText(segments.contents[0]) != "static-user" || contentText(segments.contents[1]) != testCandidate {
		t.Fatalf("request contents = %#v", segments.contents)
	}
}

func TestSplitPromptSegmentsRejectsPrefilledModelTurns(t *testing.T) {
	t.Parallel()

	_, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleAssistant, Content: "1", Cacheable: true},
		{Role: llm.RoleUser, Content: testCandidate},
	})
	if err == nil {
		t.Fatal("expected prefilled model turn to be rejected")
	}
}

func TestClassificationConfigUsesProductionCompatibleFields(t *testing.T) {
	t.Parallel()

	assertClassificationConfig(t, classificationConfig())
}

func TestNewGeminiUsesStableDefaultAndStructuredProviderLog(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := log.New()
	logger.SetOutput(&logs)
	logger.SetLevel(log.DebugLevel)
	adapter, err := NewGemini("test-key", "", log.NewEntry(logger))
	if err != nil {
		t.Fatalf("NewGemini returned error: %v", err)
	}
	api := adapter.(*API)
	requestedModel := ""
	api.generateContent = func(_ context.Context, model string, _ []*genai.Content, _ *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
		requestedModel = model
		return &genai.GenerateContentResponse{
			Candidates:    []*genai.Candidate{{Content: genai.NewContentFromText("0", genai.RoleModel)}},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{TotalTokenCount: 2},
		}, nil
	}
	if _, err := api.ChatCompletion(t.Context(), []llm.ChatCompletionMessage{{Role: llm.RoleUser, Content: testCandidate}}); err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}
	if requestedModel != DefaultModel || strings.Contains(requestedModel, "latest") {
		t.Fatalf("requested model = %q, want pinned %q", requestedModel, DefaultModel)
	}
	if !strings.Contains(logs.String(), "provider=gemini") || !strings.Contains(logs.String(), "model="+DefaultModel) {
		t.Fatalf("usage log lacks provider/model fields: %q", logs.String())
	}
}

func TestGeminiProductionWireUsesImplicitCachingWithoutExplicitCacheRequests(t *testing.T) {
	t.Parallel()
	const wireCandidate = "wire-candidate-secret"

	type wireRequest struct {
		path string
		body map[string]any
	}
	requests := make([]wireRequest, 0, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-goog-api-key"); got != "wire-test-key" {
			t.Errorf("x-goog-api-key = %q", got)
		}
		if got := r.Header.Get("x-goog-api-client"); got == "" {
			t.Error("x-goog-api-client header is empty")
		}
		var body map[string]any
		if r.ContentLength != 0 {
			if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
				t.Errorf("Content-Type = %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode wire request: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
		}
		requests = append(requests, wireRequest{path: r.URL.Path, body: body})
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1beta/models/"+DefaultModel+":generateContent" {
			http.Error(w, "explicit cache endpoint is forbidden", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"1"}]}}]}`))
	}))
	t.Cleanup(server.Close)

	client, err := genai.NewClient(t.Context(), &genai.ClientConfig{
		APIKey:     "wire-test-key",
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: server.Client(),
		HTTPOptions: genai.HTTPOptions{
			BaseURL:    server.URL,
			APIVersion: "v1beta",
		},
	})
	if err != nil {
		t.Fatalf("create real Gemini client: %v", err)
	}
	var logs bytes.Buffer
	logger := log.New()
	logger.SetOutput(&logs)
	logger.SetLevel(log.DebugLevel)
	api := newGeminiAPI(DefaultModel, log.NewEntry(logger), client)
	response, err := api.ChatCompletion(t.Context(), []llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: wireCandidate},
	})
	if err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}
	if got := response.Choices[0].Message.Content; got != "1" {
		t.Fatalf("classification = %q, want 1", got)
	}
	if len(requests) != 1 {
		t.Fatalf("wire requests = %d, want one generate request", len(requests))
	}
	request := requests[0]
	if request.path != "/v1beta/models/"+DefaultModel+":generateContent" {
		t.Fatalf("generate path = %q", request.path)
	}
	assertWireFieldOmitted(t, request.body, "cachedContent", "temperature", "topP", "topK", "thinkingConfig")
	if request.body["systemInstruction"] == nil {
		t.Fatalf("generate request omitted system instruction: %#v", request.body)
	}
	assertWireClassificationRequest(t, request.body, wireCandidate)
	if got := wireContentText(t, request.body["systemInstruction"]); got != testSystemPrompt {
		t.Fatalf("system instruction = %q", got)
	}
	if strings.Contains(strings.ToLower(logs.String()), "explicit cache") || strings.Contains(logs.String(), wireCandidate) {
		t.Fatalf("unsafe cache diagnostics were logged: %q", logs.String())
	}
}

func TestChatCompletionDoesNotRetryInvalidArgument(t *testing.T) {
	t.Parallel()

	callCount := 0
	api := &API{
		model:  DefaultModel,
		logger: log.New().WithField("test", "gemini"),
		generateContent: func(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
			callCount++
			return nil, genai.APIError{Code: http.StatusBadRequest, Status: "INVALID_ARGUMENT", Message: "invalid argument"}
		},
	}

	_, err := api.ChatCompletion(t.Context(), []llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt},
		{Role: llm.RoleUser, Content: testCandidate},
	})
	if err == nil {
		t.Fatal("expected invalid argument to fail")
	}
	if callCount != 1 {
		t.Fatalf("GenerateContent calls = %d, want 1", callCount)
	}
}

func TestChatCompletionRejectsEmptyResponseWithSafeDiagnostics(t *testing.T) {
	t.Parallel()
	const classifiedPayload = "classified-payload-secret"

	callCount := 0
	api := &API{
		model:  DefaultModel,
		logger: log.New().WithField("test", "gemini"),
		generateContent: func(_ context.Context, _ string, _ []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
			assertClassificationConfig(t, config)
			callCount++
			return &genai.GenerateContentResponse{
				Candidates: []*genai.Candidate{{FinishReason: genai.FinishReasonMaxTokens}},
				UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
					ThoughtsTokenCount: defaultMaxOutputTokens,
				},
			}, nil
		},
	}

	_, err := api.ChatCompletion(t.Context(), []llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: classifiedPayload},
	})
	if err == nil {
		t.Fatal("expected empty Gemini response to fail")
	}
	if callCount != 1 {
		t.Fatalf("GenerateContent calls = %d, want 1", callCount)
	}
	if got := llm.FailureKindOf(err); got != llm.FailureMalformedOutput {
		t.Fatalf("empty response failure kind = %q, want %q", got, llm.FailureMalformedOutput)
	}
	if strings.Contains(err.Error(), classifiedPayload) {
		t.Fatalf("diagnostic error leaked classified message: %v", err)
	}
}

func TestChatCompletionClassifiesPolicyBlockedResponseWithoutContent(t *testing.T) {
	t.Parallel()
	const providerSecret = "provider-policy-secret"

	api := &API{
		model:  DefaultModel,
		logger: log.New().WithField("test", "gemini"),
		generateContent: func(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
			return &genai.GenerateContentResponse{PromptFeedback: &genai.GenerateContentResponsePromptFeedback{
				BlockReason:        genai.BlockedReasonSafety,
				BlockReasonMessage: providerSecret,
			}}, nil
		},
	}
	_, err := api.ChatCompletion(t.Context(), []llm.ChatCompletionMessage{{Role: llm.RoleUser, Content: testCandidate}})
	if got := llm.FailureKindOf(err); got != llm.FailurePolicyBlocked {
		t.Fatalf("policy failure kind = %q, want %q", got, llm.FailurePolicyBlocked)
	}
	if strings.Contains(err.Error(), providerSecret) {
		t.Fatalf("policy failure leaked provider content: %v", err)
	}
}

func assertClassificationConfig(t *testing.T, config *genai.GenerateContentConfig) {
	t.Helper()
	if config.MaxOutputTokens != defaultMaxOutputTokens {
		t.Fatalf("max output tokens = %d, want %d", config.MaxOutputTokens, defaultMaxOutputTokens)
	}
	if config.Temperature != nil || config.TopP != nil || config.TopK != nil {
		t.Fatalf("sampling controls must be omitted: temperature=%v top_p=%v top_k=%v", config.Temperature, config.TopP, config.TopK)
	}
	if config.ThinkingConfig != nil {
		t.Fatalf("thinking config must be omitted: %#v", config.ThinkingConfig)
	}
}

func assertWireClassificationRequest(t *testing.T, body map[string]any, candidate string) {
	t.Helper()
	contents, ok := body["contents"].([]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("wire contents = %#v", body["contents"])
	}
	if got := wireContentText(t, contents[0]); got != candidate {
		t.Fatalf("wire candidate = %q", got)
	}
	generationConfig, ok := body["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generation config = %#v", body["generationConfig"])
	}
	if generationConfig["maxOutputTokens"] != float64(defaultMaxOutputTokens) || generationConfig["responseMimeType"] != "text/plain" {
		t.Fatalf("generation config = %#v", generationConfig)
	}
	safetySettings, ok := body["safetySettings"].([]any)
	if !ok || len(safetySettings) != len(defaultSafetySettings()) {
		t.Fatalf("safety settings = %#v", body["safetySettings"])
	}
}

func wireContentText(t *testing.T, value any) string {
	t.Helper()
	content, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("wire content = %#v", value)
	}
	parts, ok := content["parts"].([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("wire content parts = %#v", content["parts"])
	}
	part, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("wire content part = %#v", parts[0])
	}
	text, _ := part["text"].(string)
	return text
}

func assertWireFieldOmitted(t *testing.T, value any, forbidden ...string) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if slices.Contains(forbidden, key) {
				t.Fatalf("production wire unexpectedly included %q in %#v", key, value)
			}
			assertWireFieldOmitted(t, child, forbidden...)
		}
	case []any:
		for _, child := range value {
			assertWireFieldOmitted(t, child, forbidden...)
		}
	}
}

func contentText(content *genai.Content) string {
	if content == nil {
		return ""
	}
	parts := make([]string, 0, len(content.Parts))
	for _, part := range content.Parts {
		if part != nil && part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}
