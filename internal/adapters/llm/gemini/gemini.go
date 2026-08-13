package gemini

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"iter"
	"net/http"
	"strings"
	"sync"
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
	cacheMutex      sync.Mutex
	cacheEntries    map[string]*cacheEntry
}

type cacheEntry struct {
	cache      *genai.CachedContent
	ready      chan struct{}
	err        error
	retryAfter time.Time
}

type promptSegments struct {
	systemInstruction *genai.Content
	systemMessage     *llm.ChatCompletionMessage
	cacheableSystem   bool
	cacheablePrefix   []llm.ChatCompletionMessage
	cachedContents    []*genai.Content
	liveContents      []*genai.Content
}

const (
	DefaultModel           = "gemini-2.5-flash-lite"
	defaultMaxOutputTokens = int32(16)
	defaultCacheTTL        = 6 * time.Hour
	cacheFailureBackoff    = 30 * time.Second
	cacheDisplayPrefix     = "ngbot-spam-"
	cacheHashLength        = 12
	logFieldCacheName      = "cache_name"
	logFieldCacheDisplay   = "display"
	providerName           = "gemini"
	statusInvalidArgument  = "INVALID_ARGUMENT"
)

type requestCapability uint8

const (
	capabilityExplicitCache requestCapability = 1 << iota
	capabilityPrefilledModelTurns
	capabilitySamplingControls
	capabilityThinking
	classificationCapabilities = capabilityExplicitCache
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

	return newGeminiAPI(model, logger, client), nil
}

func newGeminiAPI(model string, logger *log.Entry, client *genai.Client) *API {
	return &API{
		model:           model,
		logger:          logger.WithFields(log.Fields{"provider": providerName, "model": model}),
		generateContent: client.Models.GenerateContent,
		createCache:     client.Caches.Create,
		listCaches:      client.Caches.All,
	}
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
	cacheFallbackReason := ""
	if classificationCapabilities&capabilityExplicitCache != 0 && (segments.cacheableSystem || len(segments.cachedContents) > 0) {
		fingerprint := cacheFingerprint(g.model, segments.systemMessage, segments.cacheablePrefix)
		cache, cacheErr := g.loadOrCreateCache(ctx, segments)
		if cacheErr != nil {
			fields := cacheUseErrorLogFields(cacheErr)
			fields["cache_outcome"] = "prepare_failed"
			fields["provider"] = providerName
			g.logger.WithFields(fields).Warn("failed to prepare Gemini explicit cache, falling back to uncached request")
		}
		if cache != nil {
			config := classificationConfig()
			config.CachedContent = cache.Name
			resp, err = g.generateContent(ctx, g.model, segments.liveContents, config)
			if err == nil {
				g.logUsageMetadata(resp)
				if hasTextResponse(resp) {
					return toChatCompletionResponse(resp), nil
				}
				fields := emptyResponseLogFields(resp)
				fields[logFieldCacheName] = cache.Name
				fields[logFieldCacheDisplay] = cache.DisplayName
				fields["cache_outcome"] = "empty_response"
				fields["provider"] = providerName
				g.logger.WithFields(fields).Warn("Gemini cached response was empty, retrying without cache")
				g.invalidateLocalCache(fingerprint, cache.Name)
				cacheFallbackReason = "empty_response"
			}
			if err != nil && !isCacheUseError(err) {
				return llm.ChatCompletionResponse{}, llm.NewFailure(llm.FailureKindOf(err), err)
			}
			if err != nil {
				g.invalidateLocalCache(fingerprint, cache.Name)
				fields := cacheUseErrorLogFields(err)
				fields[logFieldCacheName] = cache.Name
				fields[logFieldCacheDisplay] = cache.DisplayName
				fields["cache_outcome"] = "use_failed"
				fields["provider"] = providerName
				g.logger.WithFields(fields).Warn("Gemini explicit cache could not be used, retrying without cache")
				cacheFallbackReason = "use_failed"
			}
		}
	}

	contents := append([]*genai.Content{}, segments.cachedContents...)
	contents = append(contents, segments.liveContents...)
	config := classificationConfig()
	config.SystemInstruction = segments.systemInstruction
	resp, err = g.generateContent(ctx, g.model, contents, config)
	if err != nil {
		return llm.ChatCompletionResponse{}, llm.NewFailure(llm.FailureKindOf(err), err)
	}

	g.logUsageMetadata(resp)
	if !hasTextResponse(resp) {
		fields := emptyResponseLogFields(resp)
		g.logger.WithFields(fields).Warn("Gemini uncached response was empty")
		cause := fmt.Errorf(
			"generate gemini content returned no text: candidates=%v finish_reasons=%v prompt_block_reason=%v",
			fields["candidate_count"],
			fields["finish_reasons"],
			fields["prompt_block_reason"],
		)
		if fields["prompt_block_reason"] != "" {
			return llm.ChatCompletionResponse{}, llm.NewFailure(llm.FailurePolicyBlocked, cause)
		}
		return llm.ChatCompletionResponse{}, llm.NewFailure(llm.FailureMalformedOutput, cause)
	}
	if cacheFallbackReason != "" {
		g.logger.WithFields(log.Fields{
			"cache_outcome":       "fallback_succeeded",
			"cache_failure_stage": cacheFallbackReason,
			"provider":            providerName,
		}).Info("Gemini uncached fallback succeeded")
	}
	return toChatCompletionResponse(resp), nil
}

func classificationConfig() *genai.GenerateContentConfig {
	config := &genai.GenerateContentConfig{
		MaxOutputTokens:  defaultMaxOutputTokens,
		ResponseMIMEType: "text/plain",
		SafetySettings:   defaultSafetySettings(),
	}
	if classificationCapabilities&capabilitySamplingControls != 0 {
		config.Temperature = genai.Ptr(float32(0))
		config.TopK = genai.Ptr(float32(1))
		config.TopP = genai.Ptr(float32(1))
	}
	if classificationCapabilities&capabilityThinking != 0 {
		config.ThinkingConfig = &genai.ThinkingConfig{ThinkingBudget: genai.Ptr(int32(0))}
	}
	return config
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
			systemMessage := message
			segments.systemMessage = &systemMessage
			segments.cacheableSystem = message.Cacheable
		case llm.RoleAssistant:
			if classificationCapabilities&capabilityPrefilledModelTurns == 0 {
				return promptSegments{}, fmt.Errorf("prefilled assistant messages are not supported by the Gemini classification contract")
			}
			fallthrough
		case llm.RoleUser, "":
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
	fingerprint := cacheFingerprint(g.model, segments.systemMessage, segments.cacheablePrefix)
	displayName := cacheDisplayPrefix + fingerprint
	for {
		now := time.Now()
		g.cacheMutex.Lock()
		if g.cacheEntries == nil {
			g.cacheEntries = make(map[string]*cacheEntry)
		}
		entry := g.cacheEntries[fingerprint]
		if entry != nil && cacheIsUsable(entry.cache, now) {
			cache := entry.cache
			g.cacheMutex.Unlock()
			return cache, nil
		}
		if entry != nil && entry.ready != nil {
			ready := entry.ready
			g.cacheMutex.Unlock()
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("wait for Gemini explicit cache: %w", ctx.Err())
			case <-ready:
				continue
			}
		}
		if entry != nil && now.Before(entry.retryAfter) {
			err := entry.err
			g.cacheMutex.Unlock()
			return nil, fmt.Errorf("gemini explicit cache preparation is backing off: %w", err)
		}
		entry = &cacheEntry{ready: make(chan struct{})}
		g.cacheEntries[fingerprint] = entry
		g.cacheMutex.Unlock()

		cache, err := g.findOrCreateCache(ctx, segments, displayName, fingerprint)
		g.cacheMutex.Lock()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			delete(g.cacheEntries, fingerprint)
		} else if err != nil {
			entry.err = err
			entry.retryAfter = time.Now().Add(cacheFailureBackoff)
		} else {
			entry.cache = cache
		}
		close(entry.ready)
		entry.ready = nil
		g.cacheMutex.Unlock()
		return cache, err
	}
}

func (g *API) findOrCreateCache(ctx context.Context, segments promptSegments, displayName, fingerprint string) (*genai.CachedContent, error) {
	cache, err := g.findCacheByDisplayName(ctx, displayName)
	if err != nil {
		return nil, fmt.Errorf("find Gemini explicit cache: %w", err)
	}
	if cache != nil {
		g.logger.WithFields(log.Fields{
			logFieldCacheName:    cache.Name,
			logFieldCacheDisplay: cache.DisplayName,
			"expire_time":        cache.ExpireTime,
			"fingerprint":        fingerprint,
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
	if cache == nil || cache.Name == "" {
		return nil, fmt.Errorf("create Gemini explicit cache returned an invalid handle")
	}
	if cache.DisplayName == "" {
		cache.DisplayName = displayName
	}
	if cache.ExpireTime.IsZero() {
		cache.ExpireTime = time.Now().Add(defaultCacheTTL)
	}

	g.logger.WithFields(log.Fields{
		logFieldCacheName:    cache.Name,
		logFieldCacheDisplay: cache.DisplayName,
		"expire_time":        cache.ExpireTime,
		"fingerprint":        fingerprint,
		"cached_count":       len(segments.cachedContents),
	}).Info("created Gemini explicit cache")

	return cache, nil
}

func (g *API) invalidateLocalCache(fingerprint, cacheName string) {
	g.cacheMutex.Lock()
	defer g.cacheMutex.Unlock()
	entry := g.cacheEntries[fingerprint]
	if entry == nil || entry.ready != nil {
		return
	}
	if entry.cache == nil || cacheName == "" || entry.cache.Name == cacheName {
		delete(g.cacheEntries, fingerprint)
	}
}

func cacheIsUsable(cache *genai.CachedContent, now time.Time) bool {
	return cache != nil && cache.Name != "" && (cache.ExpireTime.IsZero() || cache.ExpireTime.After(now))
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

func cacheFingerprint(model string, systemMessage *llm.ChatCompletionMessage, prefix []llm.ChatCompletionMessage) string {
	hasher := sha256.New()
	writeFingerprintString(hasher, "ngbot-gemini-cache-v2")
	writeFingerprintString(hasher, model)
	writeFingerprintBool(hasher, systemMessage != nil)
	if systemMessage != nil {
		writeFingerprintMessage(hasher, *systemMessage)
	}
	writeFingerprintUint64(hasher, uint64(len(prefix)))
	for _, message := range prefix {
		writeFingerprintMessage(hasher, message)
	}
	return hex.EncodeToString(hasher.Sum(nil))[:cacheHashLength]
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

func writeFingerprintMessage(hasher hash.Hash, message llm.ChatCompletionMessage) {
	writeFingerprintString(hasher, message.Role)
	writeFingerprintBool(hasher, message.Cacheable)
	writeFingerprintString(hasher, message.Content)
}

func writeFingerprintString(hasher hash.Hash, value string) {
	writeFingerprintUint64(hasher, uint64(len(value)))
	_, _ = hasher.Write([]byte(value))
}

func writeFingerprintBool(hasher hash.Hash, value bool) {
	if value {
		_, _ = hasher.Write([]byte{1})
		return
	}
	_, _ = hasher.Write([]byte{0})
}

func writeFingerprintUint64(hasher hash.Hash, value uint64) {
	var framed [8]byte
	binary.BigEndian.PutUint64(framed[:], value)
	_, _ = hasher.Write(framed[:])
}

func isCacheUseError(err error) bool {
	var apiErr genai.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	status := strings.ToUpper(strings.TrimSpace(apiErr.Status))
	return (apiErr.Code == http.StatusBadRequest && status == statusInvalidArgument) ||
		apiErr.Code == http.StatusNotFound || apiErr.Code == http.StatusGone
}

func cacheUseErrorLogFields(err error) log.Fields {
	fields := log.Fields{}
	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		fields["provider_error_code"] = apiErr.Code
		fields["provider_error_status"] = apiErr.Status
		return fields
	}
	fields["provider_error_type"] = fmt.Sprintf("%T", err)
	return fields
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

func hasTextResponse(resp *genai.GenerateContentResponse) bool {
	return resp != nil && strings.TrimSpace(resp.Text()) != ""
}

func emptyResponseLogFields(resp *genai.GenerateContentResponse) log.Fields {
	fields := log.Fields{
		"candidate_count":     0,
		"finish_reasons":      []string(nil),
		"prompt_block_reason": "",
	}
	if resp == nil {
		fields["response_nil"] = true
		return fields
	}
	fields["candidate_count"] = len(resp.Candidates)
	finishReasons := make([]string, 0, len(resp.Candidates))
	for _, candidate := range resp.Candidates {
		if candidate != nil {
			finishReasons = append(finishReasons, string(candidate.FinishReason))
		}
	}
	fields["finish_reasons"] = finishReasons
	if resp.PromptFeedback != nil {
		fields["prompt_block_reason"] = string(resp.PromptFeedback.BlockReason)
	}
	if resp.UsageMetadata != nil {
		fields["candidate_tokens"] = resp.UsageMetadata.CandidatesTokenCount
		fields["thoughts_tokens"] = resp.UsageMetadata.ThoughtsTokenCount
	}
	return fields
}

func (g *API) logUsageMetadata(resp *genai.GenerateContentResponse) {
	if resp == nil || resp.UsageMetadata == nil {
		return
	}
	g.logger.WithFields(log.Fields{
		"cached_content_tokens": resp.UsageMetadata.CachedContentTokenCount,
		"candidate_tokens":      resp.UsageMetadata.CandidatesTokenCount,
		"prompt_tokens":         resp.UsageMetadata.PromptTokenCount,
		"thoughts_tokens":       resp.UsageMetadata.ThoughtsTokenCount,
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
