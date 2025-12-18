# GenKit Integration Example

This document provides concrete examples of how to integrate GenKit into the NGBot project, replacing the custom LLM adapters with modern GenKit flows.

## Current vs. GenKit Implementation Comparison

### Current Implementation (Custom Adapter)

```go
// Current: internal/adapters/llm/openai/openai.go
type OpenAI struct {
    apiKey  string
    model   string
    baseURL string
    logger  *log.Entry
}

func (o *OpenAI) Detect(ctx context.Context, message string) (*bool, error) {
    prompt := fmt.Sprintf("Is this message spam? Respond with only 'true' or 'false':\n%s", message)

    resp, err := o.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
        Model: o.model,
        Messages: []openai.ChatCompletionMessage{
            {Role: "user", Content: prompt},
        },
    })

    if err != nil {
        return nil, fmt.Errorf("failed to detect spam: %w", err)
    }

    isSpam := strings.ToLower(resp.Choices[0].Message.Content) == "true"
    return &isSpam, nil
}
```

### GenKit Implementation (Structured Flow)

```go
// New: internal/domain/services/spam_detection.go
package services

import (
    "context"
    "fmt"

    "github.com/firebase/genkit/go/ai"
    "github.com/firebase/genkit/go/genkit"
    "github.com/firebase/genkit/go/plugins/googlegenai"
)

type SpamDetectionInput struct {
    Message      string `json:"message" jsonschema:"description=Message to analyze for spam"`
    UserContext  string `json:"user_context,omitempty" jsonschema:"description=Additional context about the user"`
    ChatContext  string `json:"chat_context,omitempty" jsonschema:"description=Information about the chat context"`
}

type SpamAnalysisResult struct {
    IsSpam           bool     `json:"is_spam" jsonschema:"description=Whether the message is spam"`
    Confidence       float32  `json:"confidence" jsonschema:"description=Confidence score (0.0-1.0)"`
    Reasoning        string   `json:"reasoning" jsonschema:"description=Explanation of the analysis"`
    SpamIndicators   []string `json:"spam_indicators" jsonschema:"description=List of detected spam indicators"`
    RecommendedAction string  `json:"recommended_action" jsonschema:"description=Suggested moderation action"`
}

type SpamDetectionService struct {
    genkit *genkit.Genkit
}

func NewSpamDetectionService(g *genkit.Genkit) *SpamDetectionService {
    return &SpamDetectionService{genkit: g}
}

func (s *SpamDetectionService) DefineFlows() {
    // Main spam detection flow
    genkit.DefineFlow(s.genkit, "detectSpam", func(ctx context.Context, input *SpamDetectionInput) (*SpamAnalysisResult, error) {
        return s.detectSpamFlow(ctx, input)
    })

    // Content analysis flow for detailed analysis
    genkit.DefineFlow(s.genkit, "analyzeContent", func(ctx context.Context, input *SpamDetectionInput) (*DetailedContentAnalysis, error) {
        return s.analyzeContentFlow(ctx, input)
    })
}

func (s *SpamDetectionService) detectSpamFlow(ctx context.Context, input *SpamDetectionInput) (*SpamAnalysisResult, error) {
    // Build comprehensive prompt with context
    prompt := s.buildSpamDetectionPrompt(input)

    // Generate structured output using GenKit
    result, _, err := genkit.GenerateData[SpamAnalysisResult](ctx, s.genkit,
        ai.WithPrompt(prompt),
        ai.WithSystem(s.getSpamDetectionSystemPrompt()),
        ai.WithConfig(&ai.GenerateContentConfig{
            Temperature: genkit.Ptr[float32](0.1), // Low temperature for consistent results
            MaxOutputTokens: genkit.Ptr[int32](500),
        }),
    )

    if err != nil {
        return nil, fmt.Errorf("failed to generate spam analysis: %w", err)
    }

    return result, nil
}

func (s *SpamDetectionService) getSpamDetectionSystemPrompt() string {
    return `You are a sophisticated spam detection AI for Telegram groups. Analyze messages for spam content with high accuracy.

Rules:
1. Consider promotional content, scams, and unsolicited advertisements as spam
2. Be mindful of cultural differences and legitimate business communication
3. Provide clear reasoning for your decisions
4. Include specific spam indicators when detected
5. Suggest appropriate moderation actions

Spam indicators to look for:
- Urgent calls to action ("buy now", "limited time")
- Suspicious links and URL shorteners
- Cryptocurrency investment schemes
- Miracle health claims
- Repetitive or unsolicited commercial content`
}

func (s *SpamDetectionService) buildSpamDetectionPrompt(input *SpamDetectionInput) string {
    prompt := fmt.Sprintf("Analyze this message for spam:\n\nMessage: %s", input.Message)

    if input.UserContext != "" {
        prompt += fmt.Sprintf("\nUser Context: %s", input.UserContext)
    }

    if input.ChatContext != "" {
        prompt += fmt.Sprintf("\nChat Context: %s", input.ChatContext)
    }

    prompt += "\n\nProvide a structured analysis with is_spam, confidence, reasoning, spam_indicators, and recommended_action."

    return prompt
}
```

## Tool Integration Example

### Telegram Bot Tools for GenKit

```go
// internal/infrastructure/genkit/tools.go
package genkit

import (
    "context"
    "fmt"

    "github.com/firebase/genkit/go/ai"
    "github.com/firebase/genkit/go/genkit"
    api "github.com/OvyFlash/telegram-bot-api"
    "github.com/iamwavecut/ngbot/internal/domain/entities"
    "github.com/iamwavecut/ngbot/internal/domain/services"
)

type TelegramTools struct {
    botAPI    *api.BotAPI
    modService *services.ModerationService
}

func DefineTelegramTools(g *genkit.Genkit, tools *TelegramTools) {
    // Ban user tool
    banUserTool := genkit.DefineTool(g, "banUser", "Ban a user from a chat group",
        func(ctx *ai.ToolContext, req *BanUserRequest) (*BanUserResponse, error) {
            return tools.banUser(ctx, req)
        },
    )

    // Delete message tool
    deleteMessageTool := genkit.DefineTool(g, "deleteMessage", "Delete a message from a chat",
        func(ctx *ai.ToolContext, req *DeleteMessageRequest) (*DeleteMessageResponse, error) {
            return tools.deleteMessage(ctx, req)
        },
    )

    // Mute user tool
    muteUserTool := genkit.DefineTool(g, "muteUser", "Mute a user in a chat for a specified duration",
        func(ctx *ai.ToolContext, req *MuteUserRequest) (*MuteUserResponse, error) {
            return tools.muteUser(ctx, req)
        },
    )

    // Get user info tool
    getUserInfoTool := genkit.DefineTool(g, "getUserInfo", "Get information about a user",
        func(ctx *ai.ToolContext, req *GetUserInfoRequest) (*GetUserInfoResponse, error) {
            return tools.getUserInfo(ctx, req)
        },
    )
}

type BanUserRequest struct {
    ChatID    int64  `json:"chat_id" jsonschema:"description=ID of the chat"`
    UserID    int64  `json:"user_id" jsonschema:"description=ID of the user to ban"`
    Reason    string `json:"reason" jsonschema:"description=Reason for banning"`
    DeleteMessages bool `json:"delete_messages" jsonschema:"description=Whether to delete user's recent messages"`
}

type BanUserResponse struct {
    Success bool   `json:"success" jsonschema:"description=Whether the ban was successful"`
    Message string `json:"message" jsonschema:"description=Status message"`
}

func (t *TelegramTools) banUser(ctx *ai.ToolContext, req *BanUserRequest) (*BanUserResponse, error) {
    chatConfig := api.ChatConfig{ChatID: req.ChatID}

    // Ban the user
    _, err := t.botAPI.Request(api.BanChatMemberConfig{
        ChatMemberConfig: api.ChatMemberConfig{
            ChatConfig: chatConfig,
            UserID:     req.UserID,
        },
        UntilDate: 0, // Permanent ban
        RevokeMessages: req.DeleteMessages,
    })

    if err != nil {
        return &BanUserResponse{
            Success: false,
            Message: fmt.Sprintf("Failed to ban user: %v", err),
        }, nil
    }

    // Log the action
    t.modService.LogModerationAction(ctx, req.ChatID, req.UserID, "ban", req.Reason)

    return &BanUserResponse{
        Success: true,
        Message: fmt.Sprintf("User %d has been banned from chat %d", req.UserID, req.ChatID),
    }, nil
}
```

## Agentic Moderation Workflow

### Autonomous Moderation Agent

```go
// internal/domain/services/moderation_agent.go
package services

import (
    "context"
    "fmt"

    "github.com/firebase/genkit/go/ai"
    "github.com/firebase/genkit/go/genkit"
)

type ModerationAgent struct {
    genkit *genkit.Genkit
    tools  *TelegramTools
}

type ModerationTask struct {
    ChatID       int64  `json:"chat_id"`
    UserID       int64  `json:"user_id"`
    Message      string `json:"message"`
    MessageID    int    `json:"message_id"`
    UserHistory  string `json:"user_history,omitempty"`
    ChatSettings string `json:"chat_settings,omitempty"`
}

type ModerationDecision struct {
    Action         string `json:"action"` // ban, mute, delete, warn, none
    Reason         string `json:"reason"`
    Duration       int    `json:"duration,omitempty"` // For mute in minutes
    DeleteMessages bool   `json:"delete_messages"`
    NotifyAdmins   bool   `json:"notify_admins"`
}

func (m *ModerationAgent) DefineAgenticFlow() {
    genkit.DefineFlow(m.genkit, "autonomousModeration",
        func(ctx context.Context, task *ModerationTask) (*ModerationDecision, error) {
            return m.performAutonomousModeration(ctx, task)
        },
        genkit.WithTools(
            DefineTelegramTools(m.genkit, m.tools),
        ),
        genkit.WithMaxTurns(5), // Limit complexity
    )
}

func (m *ModerationAgent) performAutonomousModeration(ctx context.Context, task *ModerationTask) (*ModerationDecision, error) {
    systemPrompt := `You are an autonomous moderation agent for Telegram groups. Your job is to analyze messages and take appropriate moderation actions.

Available actions:
- "ban": Permanently ban the user from the chat
- "mute": Temporarily mute the user (specify duration in minutes)
- "delete": Delete the offending message only
- "warn": Issue a warning to the user
- "none": No action needed

Consider these factors:
1. Message content and intent
2. User's history in the chat
3. Chat-specific rules and settings
4. Severity of the violation
5. Whether this is a first offense

Use the available tools to take action when necessary. Be fair but firm in moderation.`

    prompt := fmt.Sprintf(`Moderation Task:
Chat ID: %d
User ID: %d
Message: "%s"
User History: %s
Chat Settings: %s

Analyze this situation and take appropriate moderation action if needed.`,
        task.ChatID, task.UserID, task.Message, task.UserHistory, task.ChatSettings)

    response, err := genkit.Generate(ctx, m.genkit,
        ai.WithPrompt(prompt),
        ai.WithSystem(systemPrompt),
        ai.WithTools(DefineTelegramTools(m.genkit, m.tools)...),
        ai.WithMaxTurns(3),
    )

    if err != nil {
        return nil, fmt.Errorf("moderation agent failed: %w", err)
    }

    // Parse the response and convert to ModerationDecision
    return m.parseModerationResponse(response.Text()), nil
}
```

## Configuration and Initialization

### GenKit Service Setup

```go
// internal/infrastructure/genkit/service.go
package genkit

import (
    "context"
    "fmt"

    "github.com/firebase/genkit/go/ai"
    "github.com/firebase/genkit/go/genkit"
    "github.com/firebase/genkit/go/plugins/googlegenai"
    "github.com/firebase/genkit/go/plugins/server"
)

type Service struct {
    genkit *genkit.Genkit
    config *Config
}

type Config struct {
    GeminiAPIKey string `env:"GEMINI_API_KEY,required"`
    Model        string `env:"GENKIT_MODEL,default=googleai/gemini-2.5-flash-lite"`
    ServerPort   string `env:"GENKIT_PORT,default=3400"`
}

func NewService(ctx context.Context, cfg *Config) (*Service, error) {
    // Initialize Genkit with Google AI plugin
    g, err := genkit.Init(ctx,
        genkit.WithPlugins(&googlegenai.GoogleAI{
            APIKey: cfg.GeminiAPIKey,
        }),
        genkit.WithDefaultModel(cfg.Model),
    )
    if err != nil {
        return nil, fmt.Errorf("failed to initialize genkit: %w", err)
    }

    // Create service instance
    service := &Service{
        genkit: g,
        config: cfg,
    }

    // Initialize flows and tools
    service.initializeFlows(ctx)
    service.initializeTools(ctx)

    return service, nil
}

func (s *Service) initializeFlows(ctx context.Context) {
    spamService := NewSpamDetectionService(s.genkit)
    spamService.DefineFlows()

    moderationAgent := &ModerationAgent{
        genkit: s.genkit,
        tools:  s.tools,
    }
    moderationAgent.DefineAgenticFlow()
}

func (s *Service) StartDevUI(ctx context.Context) error {
    return server.Start(ctx, "127.0.0.1:"+s.config.ServerPort, nil)
}
```

### Integration with Main Application

```go
// cmd/ngbot/main.go (updated)
func initializeGenKitService(ctx context.Context, cfg *config.Config) (*genkit.Service, error) {
    genkitConfig := &genkit.Config{
        GeminiAPIKey: cfg.LLM.APIKey,
        Model:        cfg.LLM.Model,
    }

    genkitService, err := genkit.NewService(ctx, genkitConfig)
    if err != nil {
        return nil, fmt.Errorf("failed to initialize genkit service: %w", err)
    }

    // Start GenKit Dev UI in development mode
    if cfg.Env == "development" {
        go func() {
            if err := genkitService.StartDevUI(ctx); err != nil {
                log.WithError(err).Error("Failed to start GenKit Dev UI")
            }
        }()
    }

    return genkitService, nil
}

func initializeHandlers(service bot.Service, cfg *config.Config, genkitService *genkit.Service) {
    // Create Telegram tools
    telegramTools := &genkit.TelegramTools{
        BotAPI:     service.GetBot(),
        ModService: moderation.NewModerationService(service),
    }

    // Initialize spam detection with GenKit
    spamDetector := services.NewSpamDetectionService(genkitService.GetGenkit())

    bot.RegisterUpdateHandler("reactor", chatHandlers.NewReactor(
        service,
        cfg,
        spamDetector, // Replace custom LLM with GenKit service
        telegramTools, // Add tool capabilities
    ))
}
```

## Testing GenKit Integration

### Unit Tests for GenKit Flows

```go
// internal/domain/services/spam_detection_test.go
package services_test

import (
    "context"
    "testing"

    "github.com/firebase/genkit/go/genkit"
    "github.com/firebase/genkit/go/plugins/googlegenai"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestSpamDetectionFlow(t *testing.T) {
    ctx := context.Background()

    // Initialize Genkit for testing
    g, err := genkit.Init(ctx,
        genkit.WithPlugins(&googlegenai.GoogleAI{
            APIKey: "test-key", // Use test API key or mock
        }),
    )
    require.NoError(t, err)

    // Create spam detection service
    spamService := NewSpamDetectionService(g)
    spamService.DefineFlows()

    testCases := []struct {
        name     string
        input    *SpamDetectionInput
        expected bool
    }{
        {
            name: "clear spam",
            input: &SpamDetectionInput{
                Message: "🚀 LIMITED TIME OFFER! Buy cheap Viagra now! Click here: https://spam.link",
            },
            expected: true,
        },
        {
            name: "normal message",
            input: &SpamDetectionInput{
                Message: "Hello everyone! How is your day going?",
            },
            expected: false,
        },
        {
            name: "borderline case",
            input: &SpamDetectionInput{
                Message: "Check out my new YouTube channel: https://youtube.com/example",
                UserContext: "User is a regular member who occasionally shares content",
            },
            expected: false, // Should not be spam based on context
        },
    }

    for _, tc := range testCases {
        t.Run(tc.name, func(t *testing.T) {
            result, err := genkit.RunFlow[*SpamAnalysisResult](ctx, g, "detectSpam", tc.input)
            require.NoError(t, err)
            assert.NotNil(t, result)
            assert.Equal(t, tc.expected, result.IsSpam)
            assert.Greater(t, result.Confidence, float32(0.0))
            assert.LessOrEqual(t, result.Confidence, float32(1.0))
            assert.NotEmpty(t, result.Reasoning)
        })
    }
}
```

## Benefits of GenKit Integration

### 1. **Structured Output**
- Type-safe responses from AI models
- Automatic validation and error handling
- Consistent data structures across different models

### 2. **Observability**
- Built-in tracing and monitoring
- Developer UI for testing and debugging
- Comprehensive logging of AI interactions

### 3. **Model Agnostic**
- Easy switching between different AI providers
- Consistent API regardless of underlying model
- Support for multimodal inputs and outputs

### 4. **Tool Calling**
- Autonomous agent capabilities
- Integration with external systems
- Complex multi-step workflows

### 5. **Developer Experience**
- Rich debugging capabilities
- Visual flow testing
- Performance monitoring and optimization

## Migration Strategy

### Phase 1: Parallel Implementation
1. Keep existing LLM adapters
2. Add GenKit flows alongside
3. Feature flag to switch between implementations

### Phase 2: Gradual Migration
1. Migrate simple flows first (spam detection)
2. Add complex workflows (autonomous moderation)
3. Remove old LLM adapters

### Phase 3: Full GenKit Integration
1. All AI-powered features use GenKit
2. Comprehensive observability and monitoring
3. Advanced agentic capabilities

This integration example demonstrates how GenKit can significantly enhance the NGBot's AI capabilities while providing better structure, observability, and maintainability.