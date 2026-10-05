package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	router "github.com/iamwavecut/gopenrouter"
	"github.com/iamwavecut/gopenrouter/shared"
	"github.com/iamwavecut/ngbot/internal/adapters"
	"github.com/iamwavecut/ngbot/internal/adapters/llm"
	log "github.com/sirupsen/logrus"
)

const (
	DefaultModel      = "deepseek/deepseek-v4.1-flash"
	officialProvider  = "deepseek"
	maxOutputTokens   = 2048
	statisticsTimeout = 10 * time.Minute
	finishStop        = "stop"
	generationIDField = "generation_id"
)

type API struct {
	client           *router.Client
	model            string
	logger           *log.Entry
	mu               sync.Mutex
	totals           usageTotals
	statsCtx         context.Context
	statsCancel      context.CancelFunc
	statsWorkers     sync.WaitGroup
	statsSlots       chan struct{}
	statsClosed      bool
	statsRetryDelays []time.Duration
}

type usageTotals struct {
	Requests                  int
	UsageRequests             int
	CacheObservedRequests     int
	CacheHitRequests          int
	PromptTokens              int
	CacheObservedPromptTokens int
	CachedTokens              int
	CompletionTokens          int
	ReasoningTokens           int
	CostObservedRequests      int
	CostUSD                   float64
}

type responseMetadata struct {
	Usage      map[string]any
	Generation map[string]any
	Provider   string
}

type metadataKey struct{}

type metadataTransport struct{ base http.RoundTripper }

func (t metadataTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	metadata, ok := request.Context().Value(metadataKey{}).(*responseMetadata)
	if !ok || response.StatusCode != http.StatusOK {
		return response, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	closeErr := response.Body.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if len(body) == 16<<20 {
		return nil, fmt.Errorf("OpenRouter response exceeds metadata limit")
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&document) == nil {
		if strings.HasSuffix(request.URL.Path, "/chat/completions") {
			metadata.Usage, _ = document["usage"].(map[string]any)
			metadata.Provider, _ = document["provider"].(string)
		} else if strings.HasSuffix(request.URL.Path, "/generation") {
			data, _ := document["data"].(map[string]any)
			metadata.Generation = generationMetadata(data)
		}
	}
	return response, nil
}

func NewOpenRouter(apiKey, model string, logger *log.Entry) (adapters.LLM, error) {
	return newOpenRouter(apiKey, model, "", logger)
}

func newOpenRouter(apiKey, model, baseURL string, logger *log.Entry) (*API, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("openrouter API key is empty")
	}
	if model == "" {
		model = DefaultModel
	}
	if model != DefaultModel {
		return nil, fmt.Errorf("openrouter moderation requires model %s", DefaultModel)
	}
	if logger == nil {
		logger = log.New().WithField("adapter", "openrouter")
	}
	cfg := router.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}
	cfg.SiteName = "ngbot"
	cfg.HTTPClient = &http.Client{Transport: metadataTransport{base: http.DefaultTransport}}
	statsCtx, statsCancel := context.WithCancel(context.Background())
	return &API{
		statsCtx: statsCtx, statsCancel: statsCancel, statsSlots: make(chan struct{}, 32),
		statsRetryDelays: []time.Duration{0, 5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute},
		client:           router.NewClientWithConfig(cfg), model: model,
		logger: logger.WithFields(log.Fields{"provider": "openrouter", "model": model, "provider_pin": officialProvider}),
	}, nil
}

func (a *API) ChatCompletion(ctx context.Context, messages []llm.ChatCompletionMessage) (llm.ChatCompletionResponse, error) {
	if len(messages) == 0 {
		return llm.ChatCompletionResponse{}, fmt.Errorf("chat completion requires at least one message")
	}
	converted := make([]router.ChatCompletionMessage, 0, len(messages))
	seenConversation := false
	for _, message := range messages {
		role := message.Role
		switch role {
		case llm.RoleSystem:
			if seenConversation {
				return llm.ChatCompletionResponse{}, fmt.Errorf("system message must precede conversation contents")
			}
		case llm.RoleUser, llm.RoleAssistant, "":
			seenConversation = true
			if role == "" {
				role = llm.RoleUser
			}
		default:
			return llm.ChatCompletionResponse{}, fmt.Errorf("unsupported message role: %s", role)
		}
		converted = append(converted, router.ChatCompletionMessage{Role: router.ChatCompletionMessageRole(role), Content: message.Content})
	}
	metadata := &responseMetadata{}
	ctx = context.WithValue(ctx, metadataKey{}, metadata)
	started := time.Now()
	response, err := a.client.CreateChatCompletion(ctx, router.ChatCompletionRequest{
		Model: a.model, Messages: converted, MaxTokens: maxOutputTokens, TopP: 1,
		Reasoning: &router.ReasoningParams{Effort: router.ReasoningEffortLow, Exclude: true},
		Provider:  &shared.ProviderPreferences{Only: []string{officialProvider}, AllowFallbacks: new(false), RequireParameters: new(true)},
		ExtraBody: map[string]any{"temperature": 0},
	})
	elapsed := time.Since(started)
	if err != nil {
		a.logger.WithFields(log.Fields{"duration_ms": elapsed.Milliseconds(), "failure_kind": llm.FailureKindOf(err)}).Warn("OpenRouter request failed")
		return llm.ChatCompletionResponse{}, llm.NewFailure(llm.FailureKindOf(err), err)
	}
	a.collectStatistics(response, metadata, elapsed)
	a.scheduleGenerationStatistics(response.ID)
	if metadata.Provider != "" && !strings.EqualFold(metadata.Provider, officialProvider) {
		return llm.ChatCompletionResponse{}, llm.NewFailure(llm.FailureProvider, fmt.Errorf("OpenRouter returned an unexpected provider"))
	}
	if len(response.Choices) != 1 {
		return llm.ChatCompletionResponse{}, llm.NewFailure(llm.FailureMalformedOutput, fmt.Errorf("OpenRouter returned no unique classification"))
	}
	choice := response.Choices[0]
	if choice.FinishReason == "content_filter" || choice.Message.Refusal != "" {
		return llm.ChatCompletionResponse{}, llm.NewFailure(llm.FailurePolicyBlocked, fmt.Errorf("OpenRouter classification was blocked"))
	}
	if choice.FinishReason != finishStop || (strings.TrimSpace(choice.Message.Content) != "0" && strings.TrimSpace(choice.Message.Content) != "1") {
		return llm.ChatCompletionResponse{}, llm.NewFailure(llm.FailureMalformedOutput, fmt.Errorf("OpenRouter classification was incomplete or malformed"))
	}
	return llm.ChatCompletionResponse{Choices: []llm.ChatCompletionChoice{{Message: llm.ChatCompletionMessage{Role: llm.RoleAssistant, Content: choice.Message.Content}}}}, nil
}

func (a *API) collectStatistics(response *router.ChatCompletionResponse, metadata *responseMetadata, elapsed time.Duration) {
	fields := log.Fields{
		generationIDField: response.ID, "response_model": response.Model,
		"served_provider": metadata.Provider, "duration_ms": elapsed.Milliseconds(), "usage": numericMetadata(metadata.Usage),
	}
	if len(response.Choices) > 0 {
		fields["finish_reason"] = response.Choices[0].FinishReason
		fields["native_finish_reason"] = response.Choices[0].NativeFinishReason
	}
	a.mu.Lock()
	a.totals.Requests++
	if metadata.Usage != nil {
		a.totals.UsageRequests++
		prompt := int(number(metadata.Usage["prompt_tokens"]))
		a.totals.PromptTokens += prompt
		a.totals.CompletionTokens += int(number(metadata.Usage["completion_tokens"]))
		if cost, ok := metadata.Usage["cost"].(json.Number); ok {
			a.totals.CostObservedRequests++
			a.totals.CostUSD += number(cost)
		}
		if details, ok := metadata.Usage["prompt_tokens_details"].(map[string]any); ok && details["cached_tokens"] != nil {
			cached := int(number(details["cached_tokens"]))
			a.totals.CacheObservedRequests++
			a.totals.CacheObservedPromptTokens += prompt
			a.totals.CachedTokens += cached
			if cached > 0 {
				a.totals.CacheHitRequests++
			}
			fields["uncached_prompt_tokens"] = max(0, prompt-cached)
			if prompt > 0 {
				fields["cached_prompt_fraction"] = float64(cached) / float64(prompt)
			}
		}
		if details, ok := metadata.Usage["completion_tokens_details"].(map[string]any); ok {
			a.totals.ReasoningTokens += int(number(details["reasoning_tokens"]))
		}
	}
	totals := a.totals
	a.mu.Unlock()
	fields["totals"] = totals
	if totals.CacheObservedRequests > 0 {
		fields["cache_hit_request_rate"] = float64(totals.CacheHitRequests) / float64(totals.CacheObservedRequests)
	}
	if totals.CacheObservedPromptTokens > 0 {
		fields["cached_input_token_rate"] = float64(totals.CachedTokens) / float64(totals.CacheObservedPromptTokens)
	}
	a.logger.WithFields(fields).Info("OpenRouter usage metadata")
}

func number(value any) float64 {
	if value, ok := value.(json.Number); ok {
		number, _ := value.Float64()
		return number
	}
	return 0
}

func numericMetadata(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		switch value := value.(type) {
		case json.Number, bool, nil:
			output[key] = value
		case map[string]any:
			output[key] = numericMetadata(value)
		}
	}
	return output
}

func generationMetadata(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := numericMetadata(input)
	for _, key := range []string{"external_user", "user_agent", "origin", "http_referer", "workspace_id", "app_id", "preset_id", "session_id"} {
		delete(output, key)
	}
	for _, key := range []string{"id", "upstream_id", "request_id", "created_at", "model", "provider_name", "finish_reason", "native_finish_reason", "api_type", "router", "service_tier", "data_region"} {
		if value, ok := input[key].(string); ok {
			output[key] = value
		}
	}
	if responses, ok := input["provider_responses"].([]any); ok {
		items := make([]map[string]any, 0, len(responses))
		for _, response := range responses {
			if response, ok := response.(map[string]any); ok {
				items = append(items, generationMetadata(response))
			}
		}
		output["provider_responses"] = items
	}
	return output
}

func (a *API) scheduleGenerationStatistics(id string) {
	if id == "" {
		return
	}
	a.mu.Lock()
	if a.statsClosed {
		a.mu.Unlock()
		return
	}
	select {
	case a.statsSlots <- struct{}{}:
		a.statsWorkers.Add(1)
	default:
		a.mu.Unlock()
		a.logger.WithField(generationIDField, id).Warn("OpenRouter generation statistics queue full")
		return
	}
	baseCtx := a.statsCtx
	retryDelays := a.statsRetryDelays
	a.mu.Unlock()
	go func() {
		defer a.statsWorkers.Done()
		defer func() { <-a.statsSlots }()
		metadata := &responseMetadata{}
		ctx, cancel := context.WithTimeout(context.WithValue(baseCtx, metadataKey{}, metadata), statisticsTimeout)
		defer cancel()
		var lastErr error
		for _, delay := range retryDelays {
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			attemptCtx, attemptCancel := context.WithTimeout(ctx, 10*time.Second)
			_, lastErr = a.client.GetGeneration(attemptCtx, id)
			attemptCancel()
			if lastErr == nil {
				if provider, ok := metadata.Generation["provider_name"].(string); ok && !strings.EqualFold(provider, officialProvider) {
					a.logger.WithField(generationIDField, id).Error("OpenRouter generation used an unexpected provider")
				}
				a.logger.WithFields(log.Fields{generationIDField: id, "generation": metadata.Generation}).Info("OpenRouter generation metadata")
				return
			}
			if ctx.Err() != nil {
				return
			}
		}
		a.logger.WithFields(log.Fields{generationIDField: id, "failure_kind": llm.FailureKindOf(lastErr)}).Warn("OpenRouter generation statistics unavailable")
	}()
}

func (a *API) Close() error {
	a.mu.Lock()
	if !a.statsClosed {
		a.statsClosed = true
		a.statsCancel()
	}
	a.mu.Unlock()
	a.statsWorkers.Wait()
	return nil
}

func (a *API) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.statsClosed {
		return fmt.Errorf("OpenRouter adapter is closed")
	}
	a.statsCancel()
	a.statsCtx, a.statsCancel = context.WithCancel(ctx)
	return nil
}

func (a *API) Stop(_ context.Context) error { return a.Close() }
