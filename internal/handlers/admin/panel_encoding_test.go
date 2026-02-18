package handlers

import (
	"encoding/base64"
	"encoding/binary"
	"regexp"
	"testing"
)

func TestEncodeChatID_UsesDeepLinkSafeCharset(t *testing.T) {
	re := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	cases := []int64{
		123,
		-123,
		-1001234567890,
	}
	for _, chatID := range cases {
		encoded := encodeChatID(chatID)
		if !re.MatchString(encoded) {
			t.Fatalf("encoded chat id %q contains unsupported chars", encoded)
		}
	}
}

func TestChatIDRoundTrip(t *testing.T) {
	cases := []int64{
		1,
		123,
		999999999,
		-1,
		-123,
		-1001234567890,
	}
	for _, chatID := range cases {
		encoded := encodeChatID(chatID)
		decoded, err := decodeChatID(encoded)
		if err != nil {
			t.Fatalf("decodeChatID(%q) failed: %v", encoded, err)
		}
		if decoded != chatID {
			t.Fatalf("round trip mismatch: got %d, want %d", decoded, chatID)
		}
	}
}

func TestDecodeChatID_LegacyTildePrefix(t *testing.T) {
	var chatID int64 = -100123
	legacy := legacyEncodeChatID(chatID)
	decoded, err := decodeChatID(legacy)
	if err != nil {
		t.Fatalf("decodeChatID(%q) failed: %v", legacy, err)
	}
	if decoded != chatID {
		t.Fatalf("decoded legacy chat id mismatch: got %d, want %d", decoded, chatID)
	}
}

func legacyEncodeChatID(chatID int64) string {
	negative := chatID < 0
	if negative {
		chatID = -chatID
	}
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(chatID))
	encoded := base64.RawURLEncoding.EncodeToString(buf)
	if negative {
		return "~" + encoded
	}
	return encoded
}
