package bot

import (
	"context"
	"encoding/json"

	api "github.com/OvyFlash/telegram-bot-api"
)

func Send(ctx context.Context, botAPI *api.BotAPI, chattable api.Chattable) (api.Message, error) {
	response, err := botAPI.RequestWithContext(ctx, chattable)
	if err != nil {
		return api.Message{}, err
	}
	var message api.Message
	if err := json.Unmarshal(response.Result, &message); err != nil {
		return api.Message{}, err
	}
	return message, nil
}

func GetChat(ctx context.Context, botAPI *api.BotAPI, config api.ChatInfoConfig) (api.ChatFullInfo, error) {
	response, err := botAPI.RequestWithContext(ctx, config)
	if err != nil {
		return api.ChatFullInfo{}, err
	}
	var chat api.ChatFullInfo
	if err := json.Unmarshal(response.Result, &chat); err != nil {
		return api.ChatFullInfo{}, err
	}
	return chat, nil
}

func GetChatMember(ctx context.Context, botAPI *api.BotAPI, config api.GetChatMemberConfig) (api.ChatMember, error) {
	response, err := botAPI.RequestWithContext(ctx, config)
	if err != nil {
		return api.ChatMember{}, err
	}
	var member api.ChatMember
	if err := json.Unmarshal(response.Result, &member); err != nil {
		return api.ChatMember{}, err
	}
	return member, nil
}

func RequestBool(ctx context.Context, botAPI *api.BotAPI, chattable api.Chattable) (bool, error) {
	response, err := botAPI.RequestWithContext(ctx, chattable)
	if err != nil {
		return false, err
	}
	var result bool
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return false, err
	}
	return result, nil
}

func GetUserPersonalChatMessages(ctx context.Context, botAPI *api.BotAPI, config api.UserPersonalChatMessagesConfig) ([]api.Message, error) {
	response, err := botAPI.RequestWithContext(ctx, config)
	if err != nil {
		return nil, err
	}
	var messages []api.Message
	if err := json.Unmarshal(response.Result, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}
