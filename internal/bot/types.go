package bot

import api "github.com/go-telegram-bot-api/telegram-bot-api/v5"

type ChatExtended struct {
	api.Chat
	Permissions Permissions
}

type Permissions struct {
	CanSendMessages       *bool
	CanSendMediaMessages  *bool
	CanSendPolls          *bool
	CanSendOtherMessages  *bool
	CanAddWebPagePreviews *bool
	CanChangeInfo         *bool
	CanInviteUsers        *bool
	CanPinMessages        *bool
}
