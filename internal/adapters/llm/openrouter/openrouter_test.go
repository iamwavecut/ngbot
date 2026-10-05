package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/adapters/llm"
	log "github.com/sirupsen/logrus"
)

const (
	testProvider   = "DeepSeek"
	generationPath = "/generation"
	testCandidate  = "candidate"
)

func TestPinnedRequestAndCompletePrivateUsageMetadata(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := log.New()
	logger.SetOutput(&logs)
	logger.SetFormatter(&log.JSONFormatter{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing authentication")
		}
		switch r.URL.Path {
		case "/chat/completions":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			provider := request["provider"].(map[string]any)
			if provider["allow_fallbacks"] != false || provider["require_parameters"] != true || provider["only"].([]any)[0] != officialProvider {
				t.Errorf("routing = %#v", provider)
			}
			if request["model"] != DefaultModel || request["temperature"] != float64(0) || request["max_tokens"] != float64(maxOutputTokens) {
				t.Errorf("request configuration = %#v", request)
			}
			reasoning := request["reasoning"].(map[string]any)
			if reasoning["effort"] != "low" || reasoning["exclude"] != true {
				t.Errorf("reasoning = %#v", reasoning)
			}
			if strings.Contains(string(mustJSON(t, request)), "cache_control") {
				t.Error("unexpected explicit cache management")
			}
			_, _ = w.Write([]byte(`{"id":"gen-test","model":"deepseek/deepseek-v4.1-flash","provider":"DeepSeek","choices":[{"finish_reason":"stop","native_finish_reason":"stop","message":{"role":"assistant","content":"0","reasoning":"private-reasoning"}}],"usage":{"prompt_tokens":1000,"completion_tokens":81,"total_tokens":1081,"cost":0.001,"prompt_tokens_details":{"cached_tokens":800,"cache_write_tokens":0},"completion_tokens_details":{"reasoning_tokens":80},"cost_details":{"upstream_inference_prompt_cost":0.0004,"upstream_inference_completions_cost":0.0006},"unexpected_string":"private-usage"}}`))
		case generationPath:
			if r.URL.Query().Get("id") != "gen-test" {
				t.Error("missing generation ID")
			}
			_, _ = w.Write([]byte(`{"data":{"id":"gen-test","provider_name":"DeepSeek","native_tokens_prompt":1000,"native_tokens_cached":800,"native_tokens_reasoning":80,"cache_discount":0.0002,"total_cost":0.001,"latency":300,"generation_time":400,"request_id":"req-test","external_user":"private-user","user_agent":"private-agent","provider_responses":[{"provider_name":"DeepSeek","status":200,"latency":300}]}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	adapter, err := newOpenRouter("test-key", "", server.URL, log.NewEntry(logger))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	for range 2 {
		response, err := adapter.ChatCompletion(t.Context(), []llm.ChatCompletionMessage{{Role: llm.RoleSystem, Content: "policy", Cacheable: true}, {Role: llm.RoleUser, Content: "private-candidate"}})
		if err != nil || len(response.Choices) != 1 || response.Choices[0].Message.Content != "0" {
			t.Fatalf("response=%#v error=%v", response, err)
		}
	}
	adapter.statsWorkers.Wait()
	output := logs.String()
	for _, secret := range []string{"private-candidate", "private-reasoning", "private-user", "private-agent", "private-usage", "test-key"} {
		if strings.Contains(output, secret) {
			t.Errorf("metadata leaked %s", secret)
		}
	}
	for _, field := range []string{"cached_tokens", "cache_write_tokens", "upstream_inference_prompt_cost", "native_tokens_reasoning", "request_id", "provider_responses", "cache_hit_request_rate", "cached_input_token_rate"} {
		if !strings.Contains(output, field) {
			t.Errorf("missing %s", field)
		}
	}
	if adapter.totals.Requests != 2 || adapter.totals.CachedTokens != 1600 || adapter.totals.CacheHitRequests != 2 || adapter.totals.ReasoningTokens != 160 {
		t.Fatalf("totals=%#v", adapter.totals)
	}
}

func TestClassificationFailuresAndStatisticsFailureIsolation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, finish, content, provider string
		want                            llm.FailureKind
	}{
		{"truncation", "length", "1", testProvider, llm.FailureMalformedOutput},
		{"reasoning only", finishStop, "", testProvider, llm.FailureMalformedOutput},
		{"unexpected format", finishStop, "yes", testProvider, llm.FailureMalformedOutput},
		{"content blocked", "content_filter", "", testProvider, llm.FailurePolicyBlocked},
		{"wrong provider", finishStop, "0", "Other", llm.FailureProvider},
		{"metadata temporarily unavailable", finishStop, "0", testProvider, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == generationPath {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "gen-test", "provider": tt.provider, "choices": []any{map[string]any{"finish_reason": tt.finish, "message": map[string]any{"role": "assistant", "content": tt.content}}}})
			}))
			t.Cleanup(server.Close)
			adapter, err := newOpenRouter("test-key", "", server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = adapter.Close() })
			_, err = adapter.ChatCompletion(t.Context(), []llm.ChatCompletionMessage{{Role: llm.RoleUser, Content: testCandidate}})
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || llm.FailureKindOf(err) != tt.want {
				t.Fatalf("error=%v want=%s", err, tt.want)
			}
			if adapter.totals.CacheObservedRequests != 0 {
				t.Fatal("missing cache stats interpreted as zero hit")
			}
		})
	}
}

func TestInvalidConfigurationAndMessagesNeverSendRequests(t *testing.T) {
	t.Parallel()
	if _, err := NewOpenRouter("", "", nil); err == nil {
		t.Fatal("accepted empty key")
	}
	if _, err := NewOpenRouter("key", "other/model", nil); err == nil {
		t.Fatal("accepted unpinned model")
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	t.Cleanup(server.Close)
	adapter, err := newOpenRouter("key", "", server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, messages := range [][]llm.ChatCompletionMessage{nil, {{Role: "tool"}}, {{Role: llm.RoleUser}, {Role: llm.RoleSystem}}} {
		if _, err := adapter.ChatCompletion(t.Context(), messages); err == nil {
			t.Fatal("accepted invalid messages")
		}
	}
	if requests != 0 {
		t.Fatalf("invalid input sent %d requests", requests)
	}
}

func TestRequestTimeoutIsTyped(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	t.Cleanup(server.Close)
	adapter, err := newOpenRouter("key", "", server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = adapter.ChatCompletion(ctx, []llm.ChatCompletionMessage{{Role: llm.RoleUser, Content: testCandidate}})
	if err == nil || llm.FailureKindOf(err) != llm.FailureTimeout {
		t.Fatalf("timeout error=%v", err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestShutdownCancelsPendingGenerationLookups(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == generationPath {
			close(entered)
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"gen-shutdown","provider":"DeepSeek","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"0"}}]}`))
	}))
	t.Cleanup(server.Close)
	adapter, err := newOpenRouter("key", "", server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, err = adapter.ChatCompletion(t.Context(), []llm.ChatCompletionMessage{{Role: llm.RoleUser, Content: testCandidate}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no background lookup")
	}
	if err := adapter.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(adapter.statsSlots) != 0 {
		t.Fatal("pending lookup survived shutdown")
	}
	if err := adapter.Start(t.Context()); err == nil {
		t.Fatal("closed adapter restarted")
	}
}

func TestLiveOpenRouterAccounting(t *testing.T) {
	if os.Getenv("NGBOT_RUN_LIVE_OPENROUTER_ACCOUNTING") != "1" {
		t.Skip("live accounting check is disabled")
	}
	logger := log.New()
	logger.SetFormatter(&log.JSONFormatter{})
	adapter, err := newOpenRouter(os.Getenv("NG_LLM_OPENROUTER_API_KEY"), "", "", log.NewEntry(logger))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Close() }()
	messages := []llm.ChatCompletionMessage{{Role: llm.RoleSystem, Content: strings.Repeat("Classify ordinary conversation as 0 and advertising as 1. ", 500) + "Reply exactly 0 or 1."}, {Role: llm.RoleUser, Content: "Thank you, that is clear."}}
	for range 3 {
		response, err := adapter.ChatCompletion(t.Context(), messages)
		if err != nil || len(response.Choices) != 1 || strings.TrimSpace(response.Choices[0].Message.Content) != "0" {
			t.Fatalf("live classification failed: %v", err)
		}
	}
	adapter.statsWorkers.Wait()
}

func TestDelayedGenerationStatisticsDoNotDelayClassification(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := log.New()
	logger.SetOutput(&logs)
	logger.SetFormatter(&log.JSONFormatter{})
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == generationPath {
			attempts++
			if attempts == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"data":{"id":"gen-delayed","provider_name":"DeepSeek","total_cost":0.001,"native_tokens_cached":800}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"gen-delayed","provider":"DeepSeek","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"0"}}]}`))
	}))
	t.Cleanup(server.Close)
	adapter, err := newOpenRouter("key", "", server.URL, log.NewEntry(logger))
	if err != nil {
		t.Fatal(err)
	}
	adapter.statsRetryDelays = []time.Duration{0, 150 * time.Millisecond}
	t.Cleanup(func() { _ = adapter.Close() })
	started := time.Now()
	_, err = adapter.ChatCompletion(t.Context(), []llm.ChatCompletionMessage{{Role: llm.RoleUser, Content: testCandidate}})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) >= 150*time.Millisecond {
		t.Fatal("metadata retry delayed classification")
	}
	adapter.statsWorkers.Wait()
	if attempts != 2 || !strings.Contains(logs.String(), "OpenRouter generation metadata") {
		t.Fatal("delayed metadata was not collected")
	}
}

func TestLiveOpenRouterGenerationMetadata(t *testing.T) {
	ids := strings.FieldsFunc(os.Getenv("NGBOT_LIVE_GENERATION_IDS"), func(r rune) bool { return r == ',' })
	if len(ids) == 0 {
		t.Skip("provide existing generation IDs for the live metadata check")
	}
	var logs bytes.Buffer
	logger := log.New()
	logger.SetFormatter(&log.JSONFormatter{})
	logger.SetOutput(&logs)
	adapter, err := newOpenRouter(os.Getenv("NG_LLM_OPENROUTER_API_KEY"), "", "", log.NewEntry(logger))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Close() }()
	for index, id := range ids {
		adapter.scheduleGenerationStatistics(id)
		if (index+1)%32 == 0 {
			adapter.statsWorkers.Wait()
		}
	}
	adapter.statsWorkers.Wait()
	if strings.Count(logs.String(), "OpenRouter generation metadata") != len(ids) {
		t.Fatal("not all generation records were retrieved")
	}
	fmt.Print(logs.String())
}
