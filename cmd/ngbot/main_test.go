package main

import (
	"slices"
	"testing"
	"time"
)

func TestConfigureUpdatesRequestsMessageReactionsOnly(t *testing.T) {
	t.Parallel()

	updates := configureUpdates(time.Minute).AllowedUpdates
	if !slices.Contains(updates, "message_reaction") {
		t.Fatalf("expected message_reaction updates, got %#v", updates)
	}
	if slices.Contains(updates, "message_reaction_count") {
		t.Fatalf("did not expect message_reaction_count updates, got %#v", updates)
	}
}
