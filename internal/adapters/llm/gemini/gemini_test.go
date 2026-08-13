package gemini

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync/atomic"
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

func TestSplitPromptSegmentsUsesCacheableSystemInstruction(t *testing.T) {
	t.Parallel()

	segments, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: testCandidate},
	})
	if err != nil {
		t.Fatalf("splitPromptSegments returned error: %v", err)
	}

	if len(segments.cachedContents) != 0 {
		t.Fatalf("expected no cached conversation turns, got %d", len(segments.cachedContents))
	}
	if len(segments.liveContents) != 1 {
		t.Fatalf("expected one live candidate, got %d", len(segments.liveContents))
	}
	if got := contentText(segments.systemInstruction); got != testSystemPrompt {
		t.Fatalf("unexpected system instruction text: %q", got)
	}
}

func TestSplitPromptSegmentsRejectsPrefilledModelTurns(t *testing.T) {
	t.Parallel()

	_, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleAssistant, Content: testStaticAnswer, Cacheable: true},
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

func TestCacheFingerprintIgnoresDynamicTail(t *testing.T) {
	t.Parallel()

	first, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: "candidate-a"},
	})
	if err != nil {
		t.Fatalf("first splitPromptSegments returned error: %v", err)
	}

	second, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
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
			if len(config.Contents) != 0 || contentText(config.SystemInstruction) != testSystemPrompt {
				t.Fatalf("production cache shape = contents:%#v system:%#v", config.Contents, config.SystemInstruction)
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

func TestLoadOrCreateCacheDoesNotSerializeDifferentFingerprints(t *testing.T) {
	t.Parallel()

	firstSegments := mustPromptSegments(t, "first-system")
	secondSegments := mustPromptSegments(t, "second-system")
	entered := make(chan string, 2)
	release := make(chan struct{})
	results := make(chan error, 2)
	api := &API{
		model:      DefaultModel,
		logger:     log.New().WithField("test", "gemini"),
		listCaches: listedCaches(),
		createCache: func(_ context.Context, _ string, config *genai.CreateCachedContentConfig) (*genai.CachedContent, error) {
			entered <- config.DisplayName
			<-release
			return &genai.CachedContent{Name: "cachedContents/" + config.DisplayName, DisplayName: config.DisplayName}, nil
		},
	}

	go func() {
		_, err := api.loadOrCreateCache(t.Context(), firstSegments)
		results <- err
	}()
	firstDisplay := <-entered
	go func() {
		_, err := api.loadOrCreateCache(t.Context(), secondSegments)
		results <- err
	}()

	var secondDisplay string
	select {
	case secondDisplay = <-entered:
	case <-time.After(time.Second):
		t.Error("unrelated cache fingerprint was blocked by another cache network call")
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Errorf("loadOrCreateCache returned error: %v", err)
		}
	}
	if secondDisplay != "" && firstDisplay == secondDisplay {
		t.Fatalf("different system instructions produced the same display name %q", firstDisplay)
	}
}

func TestLoadOrCreateCacheWaiterHonorsCancellation(t *testing.T) {
	t.Parallel()

	segments := mustPromptSegments(t, testSystemPrompt)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	firstResult := make(chan error, 1)
	api := &API{
		model:      DefaultModel,
		logger:     log.New().WithField("test", "gemini"),
		listCaches: listedCaches(),
		createCache: func(_ context.Context, _ string, config *genai.CreateCachedContentConfig) (*genai.CachedContent, error) {
			entered <- struct{}{}
			<-release
			return &genai.CachedContent{Name: "cachedContents/created", DisplayName: config.DisplayName}, nil
		},
	}
	go func() {
		_, err := api.loadOrCreateCache(t.Context(), segments)
		firstResult <- err
	}()
	<-entered

	waitCtx, cancel := context.WithCancel(t.Context())
	cancel()
	waiterResult := make(chan error, 1)
	go func() {
		_, err := api.loadOrCreateCache(waitCtx, segments)
		waiterResult <- err
	}()

	select {
	case err := <-waiterResult:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("waiter error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Error("canceled waiter remained blocked on cache preparation")
	}
	close(release)
	if err := <-firstResult; err != nil {
		t.Fatalf("first cache preparation returned error: %v", err)
	}
}

func TestLoadOrCreateCacheBacksOffAfterPreparationFailure(t *testing.T) {
	t.Parallel()

	var createCalls atomic.Int32
	api := &API{
		model:      DefaultModel,
		logger:     log.New().WithField("test", "gemini"),
		listCaches: listedCaches(),
		createCache: func(context.Context, string, *genai.CreateCachedContentConfig) (*genai.CachedContent, error) {
			createCalls.Add(1)
			return nil, fmt.Errorf("provider unavailable")
		},
	}
	segments := mustPromptSegments(t, testSystemPrompt)

	if _, err := api.loadOrCreateCache(t.Context(), segments); err == nil {
		t.Fatal("expected first cache preparation to fail")
	}
	if _, err := api.loadOrCreateCache(t.Context(), segments); err == nil {
		t.Fatal("expected backoff lookup to preserve the failure")
	}
	if got := createCalls.Load(); got != 1 {
		t.Fatalf("cache create calls = %d, want 1 during negative backoff", got)
	}
}

func TestLoadOrCreateCacheRejectsEmptyHandle(t *testing.T) {
	t.Parallel()

	api := &API{
		model:      DefaultModel,
		logger:     log.New().WithField("test", "gemini"),
		listCaches: listedCaches(),
		createCache: func(context.Context, string, *genai.CreateCachedContentConfig) (*genai.CachedContent, error) {
			return &genai.CachedContent{}, nil
		},
	}
	if _, err := api.loadOrCreateCache(t.Context(), mustPromptSegments(t, testSystemPrompt)); err == nil {
		t.Fatal("expected empty cache handle to be rejected")
	}
}

func TestLoadOrCreateCacheDoesNotBackOffAfterCancellation(t *testing.T) {
	t.Parallel()

	var createCalls atomic.Int32
	api := &API{
		model:      DefaultModel,
		logger:     log.New().WithField("test", "gemini"),
		listCaches: listedCaches(),
		createCache: func(ctx context.Context, _ string, config *genai.CreateCachedContentConfig) (*genai.CachedContent, error) {
			if createCalls.Add(1) == 1 {
				return nil, ctx.Err()
			}
			return &genai.CachedContent{Name: "cachedContents/created", DisplayName: config.DisplayName}, nil
		},
	}
	segments := mustPromptSegments(t, testSystemPrompt)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := api.loadOrCreateCache(canceled, segments); !errors.Is(err, context.Canceled) {
		t.Fatalf("first cache preparation error = %v, want cancellation", err)
	}
	if _, err := api.loadOrCreateCache(t.Context(), segments); err != nil {
		t.Fatalf("cache preparation after cancellation returned error: %v", err)
	}
	if got := createCalls.Load(); got != 2 {
		t.Fatalf("cache create calls = %d, want 2", got)
	}
}

func TestChatCompletionFallsBackWhenCachedContentCannotBeUsed(t *testing.T) {
	t.Parallel()

	segments, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
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
				return nil, genai.APIError{Code: 404, Status: "NOT_FOUND", Message: "cache expired"}
			}
			if config.SystemInstruction == nil || contentText(config.SystemInstruction) != testSystemPrompt {
				t.Fatalf("expected uncached fallback to restore system instruction, got %#v", config.SystemInstruction)
			}
			if len(contents) != 1 {
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

func TestChatCompletionRetriesGenericInvalidArgumentOnlyAfterCachedRequest(t *testing.T) {
	t.Parallel()

	segments, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
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
				if config.SystemInstruction != nil {
					t.Fatalf("cached request repeated system instruction: %#v", config.SystemInstruction)
				}
				if len(contents) != 1 || contentText(contents[0]) != testCandidate {
					t.Fatalf("cached request contents = %#v", contents)
				}
				return nil, genai.APIError{Code: 400, Status: "INVALID_ARGUMENT", Message: "invalid argument"}
			}
			if contentText(config.SystemInstruction) != testSystemPrompt {
				t.Fatalf("uncached retry system instruction = %#v", config.SystemInstruction)
			}
			return &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
				Content: genai.NewContentFromText("1", genai.RoleModel),
			}}}, nil
		},
	}

	resp, err := api.ChatCompletion(t.Context(), []llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
		{Role: llm.RoleUser, Content: testCandidate},
	})
	if err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("GenerateContent calls = %d, want one cached and one uncached", callCount)
	}
	if got := resp.Choices[0].Message.Content; got != "1" {
		t.Fatalf("classification = %q, want 1", got)
	}
}

func TestChatCompletionDoesNotRetryUncachedInvalidArgument(t *testing.T) {
	t.Parallel()

	callCount := 0
	api := &API{
		model:  DefaultModel,
		logger: log.New().WithField("test", "gemini"),
		generateContent: func(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
			callCount++
			return nil, genai.APIError{Code: 400, Status: "INVALID_ARGUMENT", Message: "invalid argument"}
		},
	}

	_, err := api.ChatCompletion(t.Context(), []llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt},
		{Role: llm.RoleUser, Content: testCandidate},
	})
	if err == nil {
		t.Fatal("expected uncached invalid argument to fail")
	}
	if callCount != 1 {
		t.Fatalf("uncached GenerateContent calls = %d, want 1", callCount)
	}
}

func TestChatCompletionFallsBackWhenCachedResponseIsEmpty(t *testing.T) {
	t.Parallel()

	segments, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: testSystemPrompt, Cacheable: true},
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
			if len(contents) != 1 {
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
	if config.Temperature != nil || config.TopP != nil || config.TopK != nil {
		t.Fatalf("sampling controls must be omitted: temperature=%v top_p=%v top_k=%v", config.Temperature, config.TopP, config.TopK)
	}
	if config.ThinkingConfig != nil {
		t.Fatalf("thinking config must be omitted: %#v", config.ThinkingConfig)
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

func mustPromptSegments(t *testing.T, systemInstruction string) promptSegments {
	t.Helper()
	segments, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: systemInstruction, Cacheable: true},
		{Role: llm.RoleUser, Content: testCandidate},
	})
	if err != nil {
		t.Fatalf("splitPromptSegments returned error: %v", err)
	}
	return segments
}
