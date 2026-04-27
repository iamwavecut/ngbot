package handlers

import (
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
)

func TestResolveStatusFromVotes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		votes       []*db.SpamVote
		required    int
		timedOut    bool
		wantStatus  string
		wantResolve bool
	}{
		{
			name:        "insufficient before timeout",
			votes:       []*db.SpamVote{{Vote: false}},
			required:    2,
			timedOut:    false,
			wantStatus:  "",
			wantResolve: false,
		},
		{
			name:        "insufficient after timeout",
			votes:       []*db.SpamVote{{Vote: true}},
			required:    3,
			timedOut:    true,
			wantStatus:  "false_positive",
			wantResolve: true,
		},
		{
			name: "tie waits for deciding vote",
			votes: []*db.SpamVote{
				{Vote: true},
				{Vote: false},
			},
			required:    2,
			timedOut:    false,
			wantStatus:  "",
			wantResolve: false,
		},
		{
			name: "tie after timeout resolves false positive",
			votes: []*db.SpamVote{
				{Vote: true},
				{Vote: false},
			},
			required:    2,
			timedOut:    true,
			wantStatus:  "false_positive",
			wantResolve: true,
		},
		{
			name: "spam majority",
			votes: []*db.SpamVote{
				{Vote: false},
				{Vote: false},
				{Vote: true},
			},
			required:    2,
			timedOut:    false,
			wantStatus:  "spam",
			wantResolve: true,
		},
		{
			name: "not spam majority",
			votes: []*db.SpamVote{
				{Vote: true},
				{Vote: true},
				{Vote: false},
			},
			required:    2,
			timedOut:    false,
			wantStatus:  "false_positive",
			wantResolve: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotStatus, gotResolve := resolveStatusFromVotes(tt.votes, tt.required, tt.timedOut)
			if gotStatus != tt.wantStatus || gotResolve != tt.wantResolve {
				t.Fatalf("unexpected resolution: got (%q,%v) want (%q,%v)", gotStatus, gotResolve, tt.wantStatus, tt.wantResolve)
			}
		})
	}
}

func TestResolveVotingPolicyWithOverrides(t *testing.T) {
	t.Parallel()

	base := config.SpamControl{
		VotingTimeoutMinutes: 7 * time.Minute,
		MinVoters:            2,
		MaxVoters:            10,
		MinVotersPercentage:  5,
	}

	inherit := resolveVotingPolicy(base, nil)
	if inherit.Timeout != 7*time.Minute || inherit.MinVoters != 2 || inherit.MaxVoters != 10 || inherit.MinVotersPercentage != 5 {
		t.Fatalf("unexpected inherit policy: %#v", inherit)
	}

	settings := &db.Settings{
		CommunityVotingTimeoutOverrideNS:        (3 * time.Minute).Nanoseconds(),
		CommunityVotingMinVotersOverride:        5,
		CommunityVotingMaxVotersOverride:        20,
		CommunityVotingMinVotersPercentOverride: 10,
	}
	overridden := resolveVotingPolicy(base, settings)
	if overridden.Timeout != 3*time.Minute || overridden.MinVoters != 5 || overridden.MaxVoters != 20 || overridden.MinVotersPercentage != 10 {
		t.Fatalf("unexpected overridden policy: %#v", overridden)
	}

	invalid := &db.Settings{
		CommunityVotingTimeoutOverrideNS:        0,
		CommunityVotingMinVotersOverride:        0,
		CommunityVotingMaxVotersOverride:        -5,
		CommunityVotingMinVotersPercentOverride: -10,
	}
	normalized := resolveVotingPolicy(base, invalid)
	if normalized.Timeout != 5*time.Minute {
		t.Fatalf("unexpected normalized timeout: %s", normalized.Timeout)
	}
	if normalized.MinVoters != 1 {
		t.Fatalf("unexpected normalized min voters: %d", normalized.MinVoters)
	}
	if normalized.MaxVoters != 0 {
		t.Fatalf("unexpected normalized max voters: %d", normalized.MaxVoters)
	}
	if normalized.MinVotersPercentage != 0 {
		t.Fatalf("unexpected normalized min voters percent: %v", normalized.MinVotersPercentage)
	}
}

func TestCreateInChatNotificationDoesNotEnableMarkdown(t *testing.T) {
	t.Parallel()

	sc := &SpamControl{}
	msg := &api.Message{
		Chat: api.Chat{ID: -100, Type: "supergroup"},
		From: &api.User{
			ID:        200,
			FirstName: "A_B [broken",
		},
		Text: "_broken [markdown",
	}

	chattable := sc.createInChatNotification(msg, 1, "en", true)
	reply, ok := chattable.(api.MessageConfig)
	if !ok {
		t.Fatalf("unexpected notification type: %T", chattable)
	}
	if reply.ParseMode != "" {
		t.Fatalf("expected plain text notification, got parse mode %q", reply.ParseMode)
	}
}
