package handlers

import api "github.com/OvyFlash/telegram-bot-api"

func isManager(member *api.ChatMember) bool {
	if member == nil {
		return false
	}
	if member.IsCreator() {
		return true
	}
	return member.IsAdministrator() && (member.CanManageChat || member.CanPromoteMembers)
}

func isPrivilegedModerator(member *api.ChatMember) bool {
	if member == nil {
		return false
	}
	if isManager(member) {
		return true
	}
	return member.IsAdministrator() && member.CanRestrictMembers
}
