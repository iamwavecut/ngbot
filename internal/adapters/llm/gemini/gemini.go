package gemini

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/iamwavecut/ngbot/internal/adapters"
	"github.com/iamwavecut/ngbot/internal/adapters/llm"
	log "github.com/sirupsen/logrus"
	"google.golang.org/genai"
)

type (
	generateContentFunc func(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
	createCacheFunc     func(context.Context, string, *genai.CreateCachedContentConfig) (*genai.CachedContent, error)
	listCachesFunc      func(context.Context) iter.Seq2[*genai.CachedContent, error]
)

type API struct {
	model           string
	logger          *log.Entry
	generateContent generateContentFunc
	createCache     createCacheFunc
	listCaches      listCachesFunc
}

type promptSegments struct {
	systemInstruction *genai.Content
	cacheablePrefix   []llm.ChatCompletionMessage
	cachedContents    []*genai.Content
	liveContents      []*genai.Content
}

const (
	DefaultModel           = "gemini-2.5-flash-lite"
	defaultTemperature     = float32(0)
	defaultTopK            = float32(1)
	defaultTopP            = float32(1)
	defaultMaxOutputTokens = int32(4)
	defaultCacheTTL        = 6 * time.Hour
	cacheDisplayPrefix     = "ngbot-spam-"
	cacheHashLength        = 12
)

func NewGemini(apiKey, model string, logger *log.Entry) (adapters.LLM, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("gemini API key is empty")
	}
	if model == "" {
		model = DefaultModel
	}
	if logger == nil {
		logger = log.New().WithField("adapter", "gemini")
	}

	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("create gemini client: %w", err)
	}

	return &API{
		model:           model,
		logger:          logger,
		generateContent: client.Models.GenerateContent,
		createCache:     client.Caches.Create,
		listCaches:      client.Caches.All,
	}, nil
}

func (g *API) ChatCompletion(ctx context.Context, messages []llm.ChatCompletionMessage) (llm.ChatCompletionResponse, error) {
	if len(messages) == 0 {
		return llm.ChatCompletionResponse{}, fmt.Errorf("chat completion requires at least one message")
	}

	segments, err := splitPromptSegments(messages)
	if err != nil {
		return llm.ChatCompletionResponse{}, err
	}
	if len(segments.liveContents) == 0 {
		return llm.ChatCompletionResponse{}, fmt.Errorf("chat completion requires at least one live message")
	}

	var resp *genai.GenerateContentResponse
	if len(segments.cachedContents) > 0 {
		cache, cacheErr := g.loadOrCreateCache(ctx, segments)
		if cacheErr != nil {
			g.logger.WithField("error", cacheErr.Error()).Warn("failed to prepare Gemini explicit cache, falling back to uncached request")
		}
		if cache != nil {
			resp, err = g.generateContent(ctx, g.model, segments.liveContents, &genai.GenerateContentConfig{
				CachedContent:    cache.Name,
				Temperature:      genai.Ptr(defaultTemperature),
				TopK:             genai.Ptr(defaultTopK),
				TopP:             genai.Ptr(defaultTopP),
				MaxOutputTokens:  defaultMaxOutputTokens,
				ResponseMIMEType: "text/plain",
				SafetySettings:   defaultSafetySettings(),
			})
			if err == nil {
				g.logUsageMetadata(resp)
				return toChatCompletionResponse(resp), nil
			}
			if !isCacheUseError(err) {
				return llm.ChatCompletionResponse{}, fmt.Errorf("generate gemini content with cache: %w", err)
			}
			g.logger.WithFields(log.Fields{
				"cache_name": cache.Name,
				"display":    cache.DisplayName,
				"error":      err.Error(),
			}).Warn("Gemini explicit cache could not be used, retrying without cache")
		}
	}

	contents := append([]*genai.Content{}, segments.cachedContents...)
	contents = append(contents, segments.liveContents...)
	resp, err = g.generateContent(ctx, g.model, contents, &genai.GenerateContentConfig{
		SystemInstruction: segments.systemInstruction,
		Temperature:       genai.Ptr(defaultTemperature),
		TopK:              genai.Ptr(defaultTopK),
		TopP:              genai.Ptr(defaultTopP),
		MaxOutputTokens:   defaultMaxOutputTokens,
		ResponseMIMEType:  "text/plain",
		SafetySettings:    defaultSafetySettings(),
	})
	if err != nil {
		return llm.ChatCompletionResponse{}, fmt.Errorf("generate gemini content: %w", err)
	}

	g.logUsageMetadata(resp)
	return toChatCompletionResponse(resp), nil
}

func splitPromptSegments(messages []llm.ChatCompletionMessage) (promptSegments, error) {
	var segments promptSegments
	seenConversation := false
	prefixClosed := false

	for _, message := range messages {
		switch message.Role {
		case llm.RoleSystem:
			if seenConversation {
				return promptSegments{}, fmt.Errorf("system message must precede conversation contents")
			}
			segments.systemInstruction = genai.NewContentFromText(message.Content, genai.RoleUser)
		case llm.RoleAssistant, llm.RoleUser, "":
			seenConversation = true
			content, err := toGeminiContent(message)
			if err != nil {
				return promptSegments{}, err
			}
			if !prefixClosed && message.Cacheable {
				segments.cacheablePrefix = append(segments.cacheablePrefix, message)
				segments.cachedContents = append(segments.cachedContents, content)
				continue
			}
			prefixClosed = true
			segments.liveContents = append(segments.liveContents, content)
		default:
			return promptSegments{}, fmt.Errorf("unsupported message role: %s", message.Role)
		}
	}

	return segments, nil
}

func toGeminiContent(message llm.ChatCompletionMessage) (*genai.Content, error) {
	switch message.Role {
	case llm.RoleAssistant:
		return genai.NewContentFromText(message.Content, genai.RoleModel), nil
	case llm.RoleUser, "":
		return genai.NewContentFromText(message.Content, genai.RoleUser), nil
	default:
		return nil, fmt.Errorf("unsupported message role: %s", message.Role)
	}
}

func (g *API) loadOrCreateCache(ctx context.Context, segments promptSegments) (*genai.CachedContent, error) {
	fingerprint := cacheFingerprint(g.model, segments.systemInstruction, segments.cacheablePrefix)
	displayName := cacheDisplayPrefix + fingerprint

	cache, err := g.findCacheByDisplayName(ctx, displayName)
	if err != nil {
		return nil, fmt.Errorf("find Gemini explicit cache: %w", err)
	}
	if cache != nil {
		g.logger.WithFields(log.Fields{
			"cache_name":  cache.Name,
			"display":     cache.DisplayName,
			"expire_time": cache.ExpireTime,
			"fingerprint": fingerprint,
		}).Debug("reusing Gemini explicit cache")
		return cache, nil
	}

	cache, err = g.createCache(ctx, g.model, &genai.CreateCachedContentConfig{
		DisplayName:       displayName,
		TTL:               defaultCacheTTL,
		Contents:          segments.cachedContents,
		SystemInstruction: segments.systemInstruction,
	})
	if err != nil {
		return nil, fmt.Errorf("create Gemini explicit cache: %w", err)
	}
	if cache.DisplayName == "" {
		cache.DisplayName = displayName
	}
	if cache.ExpireTime.IsZero() {
		cache.ExpireTime = time.Now().Add(defaultCacheTTL)
	}

	g.logger.WithFields(log.Fields{
		"cache_name":   cache.Name,
		"display":      cache.DisplayName,
		"expire_time":  cache.ExpireTime,
		"fingerprint":  fingerprint,
		"cached_count": len(segments.cachedContents),
	}).Info("created Gemini explicit cache")

	return cache, nil
}

func (g *API) findCacheByDisplayName(ctx context.Context, displayName string) (*genai.CachedContent, error) {
	if g.listCaches == nil {
		return nil, nil
	}

	now := time.Now()
	var selected *genai.CachedContent
	for cache, err := range g.listCaches(ctx) {
		if err != nil {
			return nil, err
		}
		if cache == nil || cache.Name == "" || cache.DisplayName != displayName {
			continue
		}
		if !cache.ExpireTime.IsZero() && !cache.ExpireTime.After(now) {
			continue
		}
		if selected == nil || cacheSortTime(cache).After(cacheSortTime(selected)) {
			selected = cache
		}
	}

	return selected, nil
}

func cacheFingerprint(model string, systemInstruction *genai.Content, prefix []llm.ChatCompletionMessage) string {
	hash := sha256.New()
	writeNormalized(hash, model)
	writeNormalized(hash, contentText(systemInstruction))
	for _, message := range prefix {
		writeNormalized(hash, message.Role)
		writeNormalized(hash, message.Content)
	}
	return hex.EncodeToString(hash.Sum(nil))[:cacheHashLength]
}

func cacheSortTime(cache *genai.CachedContent) time.Time {
	if cache == nil {
		return time.Time{}
	}
	if !cache.UpdateTime.IsZero() {
		return cache.UpdateTime
	}
	if !cache.CreateTime.IsZero() {
		return cache.CreateTime
	}
	return cache.ExpireTime
}

func contentText(content *genai.Content) string {
	if content == nil {
		return ""
	}
	parts := make([]string, 0, len(content.Parts))
	for _, part := range content.Parts {
		if part == nil || part.Text == "" {
			continue
		}
		parts = append(parts, part.Text)
	}
	return strings.Join(parts, "\n")
}

func writeNormalized(hasher interface{ Write([]byte) (int, error) }, value string) {
	_, _ = hasher.Write([]byte(strings.Join(strings.Fields(strings.TrimSpace(value)), " ")))
	_, _ = hasher.Write([]byte{'\n'})
}

func isCacheUseError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "cached content") ||
		strings.Contains(msg, "cachedcontent") ||
		strings.Contains(msg, "cache entry") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "expired")
}

func toChatCompletionResponse(resp *genai.GenerateContentResponse) llm.ChatCompletionResponse {
	if resp == nil {
		return llm.ChatCompletionResponse{}
	}
	return llm.ChatCompletionResponse{
		Choices: []llm.ChatCompletionChoice{{
			Message: llm.ChatCompletionMessage{
				Role:    llm.RoleAssistant,
				Content: resp.Text(),
			},
		}},
	}
}

func (g *API) logUsageMetadata(resp *genai.GenerateContentResponse) {
	if resp == nil || resp.UsageMetadata == nil {
		return
	}
	g.logger.WithFields(log.Fields{
		"cached_content_tokens": resp.UsageMetadata.CachedContentTokenCount,
		"prompt_tokens":         resp.UsageMetadata.PromptTokenCount,
		"total_tokens":          resp.UsageMetadata.TotalTokenCount,
	}).Debug("Gemini usage metadata")
}

func defaultSafetySettings() []*genai.SafetySetting {
	return []*genai.SafetySetting{
		{Category: genai.HarmCategoryHarassment, Threshold: genai.HarmBlockThresholdBlockNone},
		{Category: genai.HarmCategoryHateSpeech, Threshold: genai.HarmBlockThresholdBlockNone},
		{Category: genai.HarmCategorySexuallyExplicit, Threshold: genai.HarmBlockThresholdBlockNone},
		{Category: genai.HarmCategoryDangerousContent, Threshold: genai.HarmBlockThresholdBlockNone},
	}
}
