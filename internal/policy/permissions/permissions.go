package permissions

import api "github.com/OvyFlash/telegram-bot-api"

func IsManager(member *api.ChatMember) bool {
	if member == nil {
		return false
	}
	if member.IsCreator() {
		return true
	}
	return member.IsAdministrator() && (member.CanManageChat || member.CanPromoteMembers)
}

func IsPrivilegedModerator(member *api.ChatMember) bool {
	if member == nil {
		return false
	}
	if IsManager(member) {
		return true
	}
	return member.IsAdministrator() && member.CanRestrictMembers
}
