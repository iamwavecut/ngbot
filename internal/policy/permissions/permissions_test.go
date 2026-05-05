package permissions

import (
	"testing"

	api "github.com/OvyFlash/telegram-bot-api"
)

func TestCanRestrictMembers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		member *api.ChatMember
		want   bool
	}{
		{
			name: "nil member",
			want: false,
		},
		{
			name:   "regular member",
			member: &api.ChatMember{Status: "member"},
			want:   false,
		},
		{
			name:   "creator",
			member: &api.ChatMember{Status: "creator"},
			want:   true,
		},
		{
			name: "administrator with restrict permission",
			member: &api.ChatMember{
				Status:             "administrator",
				CanRestrictMembers: true,
			},
			want: true,
		},
		{
			name: "administrator with manage permission only",
			member: &api.ChatMember{
				Status:        "administrator",
				CanManageChat: true,
			},
			want: false,
		},
		{
			name: "administrator with promote permission only",
			member: &api.ChatMember{
				Status:            "administrator",
				CanPromoteMembers: true,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := CanRestrictMembers(tt.member)
			if got != tt.want {
				t.Fatalf("CanRestrictMembers() = %v, want %v", got, tt.want)
			}
		})
	}
}
