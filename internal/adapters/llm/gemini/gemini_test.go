package gemini

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/adapters/llm"
	log "github.com/sirupsen/logrus"
	"google.golang.org/genai"
)

const (
	testSystemPrompt      = "system"
	testStaticUser        = "static-user"
	testStaticAnswer      = "static-answer"
	testCandidate         = "candidate"
	testExistingCacheName = "cachedContents/existing"
)

func TestSplitPromptSegmentsUsesContiguousCacheablePrefix(t *testing.T) {
	t.Parallel()

	segments, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: testStaticUser, Cacheable: true},
		{Role: llm.RoleAssistant, Content: testStaticAnswer, Cacheable: true},
		{Role: llm.RoleUser, Content: "dynamic-example"},
		{Role: llm.RoleAssistant, Content: "dynamic-answer", Cacheable: true},
		{Role: llm.RoleUser, Content: testCandidate},
	})
	if err != nil {
		t.Fatalf("splitPromptSegments returned error: %v", err)
	}

	if len(segments.cachedContents) != 2 {
		t.Fatalf("expected two cached contents, got %d", len(segments.cachedContents))
	}
	if len(segments.liveContents) != 3 {
		t.Fatalf("expected three live contents, got %d", len(segments.liveContents))
	}
	if got := contentText(segments.systemInstruction); got != testSystemPrompt {
		t.Fatalf("unexpected system instruction text: %q", got)
	}
}

func TestCacheFingerprintIgnoresDynamicTail(t *testing.T) {
	t.Parallel()

	first, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: testStaticUser, Cacheable: true},
		{Role: llm.RoleAssistant, Content: testStaticAnswer, Cacheable: true},
		{Role: llm.RoleUser, Content: "candidate-a"},
	})
	if err != nil {
		t.Fatalf("first splitPromptSegments returned error: %v", err)
	}

	second, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: testStaticUser, Cacheable: true},
		{Role: llm.RoleAssistant, Content: testStaticAnswer, Cacheable: true},
		{Role: llm.RoleUser, Content: "candidate-b"},
	})
	if err != nil {
		t.Fatalf("second splitPromptSegments returned error: %v", err)
	}

	firstFingerprint := cacheFingerprint(DefaultModel, first.systemInstruction, first.cacheablePrefix)
	secondFingerprint := cacheFingerprint(DefaultModel, second.systemInstruction, second.cacheablePrefix)
	if firstFingerprint != secondFingerprint {
		t.Fatalf("expected identical fingerprints, got %q and %q", firstFingerprint, secondFingerprint)
	}
}

func TestLoadOrCreateCacheReusesMatchingDisplayName(t *testing.T) {
	t.Parallel()

	segments, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: testStaticUser, Cacheable: true},
		{Role: llm.RoleUser, Content: testCandidate},
	})
	if err != nil {
		t.Fatalf("splitPromptSegments returned error: %v", err)
	}

	fingerprint := cacheFingerprint(DefaultModel, segments.systemInstruction, segments.cacheablePrefix)
	createCalls := 0
	api := &API{
		model:  DefaultModel,
		logger: log.New().WithField("test", "gemini"),
		listCaches: listedCaches(
			&genai.CachedContent{
				Name:        "cachedContents/expired",
				DisplayName: cacheDisplayPrefix + fingerprint,
				Model:       DefaultModel,
				ExpireTime:  time.Now().Add(-time.Minute),
				UpdateTime:  time.Now().Add(-2 * time.Minute),
			},
			&genai.CachedContent{
				Name:        testExistingCacheName,
				DisplayName: cacheDisplayPrefix + fingerprint,
				Model:       DefaultModel,
				ExpireTime:  time.Now().Add(time.Hour),
				UpdateTime:  time.Now(),
			},
		),
		createCache: func(context.Context, string, *genai.CreateCachedContentConfig) (*genai.CachedContent, error) {
			createCalls++
			return nil, fmt.Errorf("unexpected create")
		},
	}

	got, err := api.loadOrCreateCache(context.Background(), segments)
	if err != nil {
		t.Fatalf("loadOrCreateCache returned error: %v", err)
	}
	if got == nil || got.Name != testExistingCacheName {
		t.Fatalf("expected existing cache to be reused, got %#v", got)
	}
	if createCalls != 0 {
		t.Fatalf("expected create cache to be skipped, got %d calls", createCalls)
	}
}

func TestLoadOrCreateCacheCreatesWhenCacheMissing(t *testing.T) {
	t.Parallel()

	segments, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: testStaticUser, Cacheable: true},
		{Role: llm.RoleUser, Content: testCandidate},
	})
	if err != nil {
		t.Fatalf("splitPromptSegments returned error: %v", err)
	}

	createCalls := 0
	api := &API{
		model:      DefaultModel,
		logger:     log.New().WithField("test", "gemini"),
		listCaches: listedCaches(),
		createCache: func(_ context.Context, _ string, config *genai.CreateCachedContentConfig) (*genai.CachedContent, error) {
			createCalls++
			if config.DisplayName == "" {
				t.Fatal("expected display name to be derived from fingerprint")
			}
			return &genai.CachedContent{
				Name:        "cachedContents/created",
				DisplayName: config.DisplayName,
				Model:       DefaultModel,
				ExpireTime:  time.Now().Add(time.Hour),
			}, nil
		},
	}

	got, err := api.loadOrCreateCache(context.Background(), segments)
	if err != nil {
		t.Fatalf("loadOrCreateCache returned error: %v", err)
	}
	if got == nil || got.Name != "cachedContents/created" {
		t.Fatalf("expected created cache, got %#v", got)
	}
	if createCalls != 1 {
		t.Fatalf("expected one cache creation, got %d", createCalls)
	}
}

func TestLoadOrCreateCacheUsesLocalHandleAfterFirstLookup(t *testing.T) {
	t.Parallel()

	segments, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: testStaticUser, Cacheable: true},
		{Role: llm.RoleUser, Content: testCandidate},
	})
	if err != nil {
		t.Fatalf("splitPromptSegments returned error: %v", err)
	}
	fingerprint := cacheFingerprint(DefaultModel, segments.systemInstruction, segments.cacheablePrefix)
	listCalls := 0
	api := &API{
		model:  DefaultModel,
		logger: log.New().WithField("test", "gemini"),
		listCaches: func(context.Context) iter.Seq2[*genai.CachedContent, error] {
			listCalls++
			return listedCaches(&genai.CachedContent{
				Name:        testExistingCacheName,
				DisplayName: cacheDisplayPrefix + fingerprint,
				ExpireTime:  time.Now().Add(time.Hour),
			})(context.Background())
		},
	}

	first, err := api.loadOrCreateCache(context.Background(), segments)
	if err != nil {
		t.Fatalf("first loadOrCreateCache: %v", err)
	}
	second, err := api.loadOrCreateCache(context.Background(), segments)
	if err != nil {
		t.Fatalf("second loadOrCreateCache: %v", err)
	}
	if first != second {
		t.Fatal("expected the process-local cache handle to be reused")
	}
	if listCalls != 1 {
		t.Fatalf("remote cache list calls = %d, want 1", listCalls)
	}

	api.invalidateLocalCache(fingerprint, first.Name)
	if _, err := api.loadOrCreateCache(context.Background(), segments); err != nil {
		t.Fatalf("reload after invalidation: %v", err)
	}
	if listCalls != 2 {
		t.Fatalf("remote cache list calls after invalidation = %d, want 2", listCalls)
	}
}

func TestChatCompletionFallsBackWhenCachedContentCannotBeUsed(t *testing.T) {
	t.Parallel()

	segments, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: testStaticUser, Cacheable: true},
		{Role: llm.RoleAssistant, Content: testStaticAnswer, Cacheable: true},
		{Role: llm.RoleUser, Content: testCandidate},
	})
	if err != nil {
		t.Fatalf("splitPromptSegments returned error: %v", err)
	}
	fingerprint := cacheFingerprint(DefaultModel, segments.systemInstruction, segments.cacheablePrefix)

	callCount := 0
	api := &API{
		model:      DefaultModel,
		logger:     log.New().WithField("test", "gemini"),
		listCaches: listedCaches(&genai.CachedContent{Name: testExistingCacheName, DisplayName: cacheDisplayPrefix + fingerprint, Model: DefaultModel, ExpireTime: time.Now().Add(time.Hour)}),
		generateContent: func(_ context.Context, _ string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
			assertClassificationConfig(t, config)
			callCount++
			if config.CachedContent != "" {
				if len(contents) != 1 || contentText(contents[0]) != testCandidate {
					t.Fatalf("expected cached request to include only candidate content, got %#v", contents)
				}
				return nil, fmt.Errorf("cached content expired")
			}
			if config.SystemInstruction == nil || contentText(config.SystemInstruction) != testSystemPrompt {
				t.Fatalf("expected uncached fallback to restore system instruction, got %#v", config.SystemInstruction)
			}
			if len(contents) != 3 {
				t.Fatalf("expected uncached fallback to send full contents, got %d", len(contents))
			}
			return &genai.GenerateContentResponse{
				Candidates: []*genai.Candidate{{
					Content: genai.NewContentFromText("1", genai.RoleModel),
				}},
			}, nil
		},
	}

	resp, err := api.ChatCompletion(context.Background(), []llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: testStaticUser, Cacheable: true},
		{Role: llm.RoleAssistant, Content: testStaticAnswer, Cacheable: true},
		{Role: llm.RoleUser, Content: testCandidate},
	})
	if err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("expected two GenerateContent calls, got %d", callCount)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "1" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func TestChatCompletionFallsBackWhenCachedResponseIsEmpty(t *testing.T) {
	t.Parallel()

	segments, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: testStaticUser, Cacheable: true},
		{Role: llm.RoleAssistant, Content: testStaticAnswer, Cacheable: true},
		{Role: llm.RoleUser, Content: testCandidate},
	})
	if err != nil {
		t.Fatalf("splitPromptSegments returned error: %v", err)
	}
	fingerprint := cacheFingerprint(DefaultModel, segments.systemInstruction, segments.cacheablePrefix)

	callCount := 0
	api := &API{
		model:      DefaultModel,
		logger:     log.New().WithField("test", "gemini"),
		listCaches: listedCaches(&genai.CachedContent{Name: testExistingCacheName, DisplayName: cacheDisplayPrefix + fingerprint, Model: DefaultModel, ExpireTime: time.Now().Add(time.Hour)}),
		generateContent: func(_ context.Context, _ string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
			callCount++
			if config.CachedContent != "" {
				if len(contents) != 1 || contentText(contents[0]) != testCandidate {
					t.Fatalf("expected cached request to include only candidate content, got %#v", contents)
				}
				return &genai.GenerateContentResponse{}, nil
			}
			if config.SystemInstruction == nil || contentText(config.SystemInstruction) != testSystemPrompt {
				t.Fatalf("expected uncached fallback to restore system instruction, got %#v", config.SystemInstruction)
			}
			if len(contents) != 3 {
				t.Fatalf("expected uncached fallback to send full contents, got %d", len(contents))
			}
			return &genai.GenerateContentResponse{
				Candidates: []*genai.Candidate{{
					Content: genai.NewContentFromText("0", genai.RoleModel),
				}},
			}, nil
		},
	}

	resp, err := api.ChatCompletion(context.Background(), []llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: testStaticUser, Cacheable: true},
		{Role: llm.RoleAssistant, Content: testStaticAnswer, Cacheable: true},
		{Role: llm.RoleUser, Content: testCandidate},
	})
	if err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("expected two GenerateContent calls, got %d", callCount)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "0" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func TestChatCompletionRejectsRepeatedEmptyResponseWithSafeDiagnostics(t *testing.T) {
	t.Parallel()
	const classifiedPayload = "classified-payload-secret"

	segments, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: testStaticUser, Cacheable: true},
		{Role: llm.RoleAssistant, Content: testStaticAnswer, Cacheable: true},
		{Role: llm.RoleUser, Content: classifiedPayload},
	})
	if err != nil {
		t.Fatalf("splitPromptSegments returned error: %v", err)
	}
	fingerprint := cacheFingerprint(DefaultModel, segments.systemInstruction, segments.cacheablePrefix)

	callCount := 0
	api := &API{
		model:      DefaultModel,
		logger:     log.New().WithField("test", "gemini"),
		listCaches: listedCaches(&genai.CachedContent{Name: testExistingCacheName, DisplayName: cacheDisplayPrefix + fingerprint, Model: DefaultModel, ExpireTime: time.Now().Add(time.Hour)}),
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

	_, err = api.ChatCompletion(context.Background(), []llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: testStaticUser, Cacheable: true},
		{Role: llm.RoleAssistant, Content: testStaticAnswer, Cacheable: true},
		{Role: llm.RoleUser, Content: classifiedPayload},
	})
	if err == nil {
		t.Fatal("expected repeated empty Gemini response to fail")
	}
	if callCount != 2 {
		t.Fatalf("GenerateContent calls = %d, want 2", callCount)
	}
	if !strings.Contains(err.Error(), "returned no text") || !strings.Contains(err.Error(), string(genai.FinishReasonMaxTokens)) {
		t.Fatalf("unexpected empty response error: %v", err)
	}
	if strings.Contains(err.Error(), classifiedPayload) {
		t.Fatalf("diagnostic error leaked classified message: %v", err)
	}
}

func assertClassificationConfig(t *testing.T, config *genai.GenerateContentConfig) {
	t.Helper()
	if config.MaxOutputTokens != defaultMaxOutputTokens {
		t.Fatalf("max output tokens = %d, want %d", config.MaxOutputTokens, defaultMaxOutputTokens)
	}
	if config.ThinkingConfig == nil || config.ThinkingConfig.ThinkingBudget == nil {
		t.Fatal("thinking budget is not configured")
	}
	if *config.ThinkingConfig.ThinkingBudget != defaultThinkingBudget {
		t.Fatalf("thinking budget = %d, want %d", *config.ThinkingConfig.ThinkingBudget, defaultThinkingBudget)
	}
}

func listedCaches(caches ...*genai.CachedContent) listCachesFunc {
	return func(context.Context) iter.Seq2[*genai.CachedContent, error] {
		return func(yield func(*genai.CachedContent, error) bool) {
			for _, cache := range caches {
				if !yield(cache, nil) {
					return
				}
			}
		}
	}
}
