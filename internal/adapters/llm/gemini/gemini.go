package gemini

import (
	"context"
	"fmt"
	"strings"

	"github.com/iamwavecut/ngbot/internal/adapters"
	"github.com/iamwavecut/ngbot/internal/adapters/llm"
	log "github.com/sirupsen/logrus"
	"google.golang.org/genai"
)

type generateContentFunc func(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)

type API struct {
	model           string
	logger          *log.Entry
	generateContent generateContentFunc
}

type promptSegments struct {
	systemInstruction *genai.Content
	contents          []*genai.Content
}

const (
	DefaultModel           = "gemini-2.5-flash-lite"
	defaultMaxOutputTokens = int32(16)
	providerName           = "gemini"
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
	if len(segments.contents) == 0 {
		return llm.ChatCompletionResponse{}, fmt.Errorf("chat completion requires at least one user message")
	}

	config := classificationConfig()
	config.SystemInstruction = segments.systemInstruction
	resp, err := g.generateContent(ctx, g.model, segments.contents, config)
	if err != nil {
		return llm.ChatCompletionResponse{}, llm.NewFailure(llm.FailureKindOf(err), err)
	}

	g.logUsageMetadata(resp)
	if !hasTextResponse(resp) {
		fields := emptyResponseLogFields(resp)
		g.logger.WithFields(fields).Warn("Gemini response was empty")
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

	return toChatCompletionResponse(resp), nil
}

func classificationConfig() *genai.GenerateContentConfig {
	return &genai.GenerateContentConfig{
		MaxOutputTokens:  defaultMaxOutputTokens,
		ResponseMIMEType: "text/plain",
		SafetySettings:   defaultSafetySettings(),
	}
}

func splitPromptSegments(messages []llm.ChatCompletionMessage) (promptSegments, error) {
	var segments promptSegments
	seenConversation := false

	for _, message := range messages {
		switch message.Role {
		case llm.RoleSystem:
			if seenConversation {
				return promptSegments{}, fmt.Errorf("system message must precede conversation contents")
			}
			segments.systemInstruction = genai.NewContentFromText(message.Content, genai.RoleUser)
		case llm.RoleUser, "":
			seenConversation = true
			content, err := toGeminiContent(message)
			if err != nil {
				return promptSegments{}, err
			}
			segments.contents = append(segments.contents, content)
		case llm.RoleAssistant:
			return promptSegments{}, fmt.Errorf("prefilled assistant messages are not supported by the Gemini classification contract")
		default:
			return promptSegments{}, fmt.Errorf("unsupported message role: %s", message.Role)
		}
	}

	return segments, nil
}

func toGeminiContent(message llm.ChatCompletionMessage) (*genai.Content, error) {
	switch message.Role {
	case llm.RoleUser, "":
		return genai.NewContentFromText(message.Content, genai.RoleUser), nil
	default:
		return nil, fmt.Errorf("unsupported message role: %s", message.Role)
	}
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
