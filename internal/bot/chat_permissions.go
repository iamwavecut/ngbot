package bot

import api "github.com/OvyFlash/telegram-bot-api"

func UnrestrictedChatPermissions() *api.ChatPermissions {
	return &api.ChatPermissions{
		CanSendMessages:       true,
		CanSendAudios:         true,
		CanSendDocuments:      true,
		CanSendPhotos:         true,
		CanSendVideos:         true,
		CanSendVideoNotes:     true,
		CanSendVoiceNotes:     true,
		CanSendPolls:          true,
		CanReactToMessages:    true,
		CanSendOtherMessages:  true,
		CanAddWebPagePreviews: true,
		CanEditTag:            true,
		CanChangeInfo:         true,
		CanInviteUsers:        true,
		CanPinMessages:        true,
		CanManageTopics:       true,
	}
}
