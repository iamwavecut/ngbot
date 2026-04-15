package gemini

import (
	"context"
	"fmt"
	"iter"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/adapters/llm"
	log "github.com/sirupsen/logrus"
	"google.golang.org/genai"
)

func TestSplitPromptSegmentsUsesContiguousCacheablePrefix(t *testing.T) {
	t.Parallel()

	segments, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: "system", Cacheable: true},
		{Role: llm.RoleUser, Content: "static-user", Cacheable: true},
		{Role: llm.RoleAssistant, Content: "static-answer", Cacheable: true},
		{Role: llm.RoleUser, Content: "dynamic-example"},
		{Role: llm.RoleAssistant, Content: "dynamic-answer", Cacheable: true},
		{Role: llm.RoleUser, Content: "candidate"},
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
	if got := contentText(segments.systemInstruction); got != "system" {
		t.Fatalf("unexpected system instruction text: %q", got)
	}
}

func TestCacheFingerprintIgnoresDynamicTail(t *testing.T) {
	t.Parallel()

	first, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: "system", Cacheable: true},
		{Role: llm.RoleUser, Content: "static-user", Cacheable: true},
		{Role: llm.RoleAssistant, Content: "static-answer", Cacheable: true},
		{Role: llm.RoleUser, Content: "candidate-a"},
	})
	if err != nil {
		t.Fatalf("first splitPromptSegments returned error: %v", err)
	}

	second, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: "system", Cacheable: true},
		{Role: llm.RoleUser, Content: "static-user", Cacheable: true},
		{Role: llm.RoleAssistant, Content: "static-answer", Cacheable: true},
		{Role: llm.RoleUser, Content: "candidate-b"},
	})
	if err != nil {
		t.Fatalf("second splitPromptSegments returned error: %v", err)
	}

	firstFingerprint := cacheFingerprint("gemini-2.5-flash-lite", first.systemInstruction, first.cacheablePrefix)
	secondFingerprint := cacheFingerprint("gemini-2.5-flash-lite", second.systemInstruction, second.cacheablePrefix)
	if firstFingerprint != secondFingerprint {
		t.Fatalf("expected identical fingerprints, got %q and %q", firstFingerprint, secondFingerprint)
	}
}

func TestLoadOrCreateCacheReusesMatchingDisplayName(t *testing.T) {
	t.Parallel()

	segments, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: "system", Cacheable: true},
		{Role: llm.RoleUser, Content: "static-user", Cacheable: true},
		{Role: llm.RoleUser, Content: "candidate"},
	})
	if err != nil {
		t.Fatalf("splitPromptSegments returned error: %v", err)
	}

	fingerprint := cacheFingerprint("gemini-2.5-flash-lite", segments.systemInstruction, segments.cacheablePrefix)
	createCalls := 0
	api := &API{
		model:  "gemini-2.5-flash-lite",
		logger: log.New().WithField("test", "gemini"),
		listCaches: listedCaches(
			&genai.CachedContent{
				Name:        "cachedContents/expired",
				DisplayName: cacheDisplayPrefix + fingerprint,
				Model:       "gemini-2.5-flash-lite",
				ExpireTime:  time.Now().Add(-time.Minute),
				UpdateTime:  time.Now().Add(-2 * time.Minute),
			},
			&genai.CachedContent{
				Name:        "cachedContents/existing",
				DisplayName: cacheDisplayPrefix + fingerprint,
				Model:       "gemini-2.5-flash-lite",
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
	if got == nil || got.Name != "cachedContents/existing" {
		t.Fatalf("expected existing cache to be reused, got %#v", got)
	}
	if createCalls != 0 {
		t.Fatalf("expected create cache to be skipped, got %d calls", createCalls)
	}
}

func TestLoadOrCreateCacheCreatesWhenCacheMissing(t *testing.T) {
	t.Parallel()

	segments, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: "system", Cacheable: true},
		{Role: llm.RoleUser, Content: "static-user", Cacheable: true},
		{Role: llm.RoleUser, Content: "candidate"},
	})
	if err != nil {
		t.Fatalf("splitPromptSegments returned error: %v", err)
	}

	createCalls := 0
	api := &API{
		model:      "gemini-2.5-flash-lite",
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
				Model:       "gemini-2.5-flash-lite",
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

func TestChatCompletionFallsBackWhenCachedContentCannotBeUsed(t *testing.T) {
	t.Parallel()

	segments, err := splitPromptSegments([]llm.ChatCompletionMessage{
		{Role: llm.RoleSystem, Content: "system", Cacheable: true},
		{Role: llm.RoleUser, Content: "static-user", Cacheable: true},
		{Role: llm.RoleAssistant, Content: "static-answer", Cacheable: true},
		{Role: llm.RoleUser, Content: "candidate"},
	})
	if err != nil {
		t.Fatalf("splitPromptSegments returned error: %v", err)
	}
	fingerprint := cacheFingerprint("gemini-2.5-flash-lite", segments.systemInstruction, segments.cacheablePrefix)

	callCount := 0
	api := &API{
		model:      "gemini-2.5-flash-lite",
		logger:     log.New().WithField("test", "gemini"),
		listCaches: listedCaches(&genai.CachedContent{Name: "cachedContents/existing", DisplayName: cacheDisplayPrefix + fingerprint, Model: "gemini-2.5-flash-lite", ExpireTime: time.Now().Add(time.Hour)}),
		generateContent: func(_ context.Context, _ string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
			callCount++
			if config.CachedContent != "" {
				if len(contents) != 1 || contentText(contents[0]) != "candidate" {
					t.Fatalf("expected cached request to include only candidate content, got %#v", contents)
				}
				return nil, fmt.Errorf("cached content expired")
			}
			if config.SystemInstruction == nil || contentText(config.SystemInstruction) != "system" {
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
		{Role: llm.RoleSystem, Content: "system", Cacheable: true},
		{Role: llm.RoleUser, Content: "static-user", Cacheable: true},
		{Role: llm.RoleAssistant, Content: "static-answer", Cacheable: true},
		{Role: llm.RoleUser, Content: "candidate"},
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
