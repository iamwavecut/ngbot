package bot

import (
	"encoding/json"
	"strings"
	"testing"

	api "github.com/OvyFlash/telegram-bot-api"
)

func TestExtractTextFromMessageIncludesRichMessageText(t *testing.T) {
	t.Parallel()

	const payload = `{
		"message_id": 1,
		"date": 1,
		"chat": {"id": -100, "type": "supergroup"},
		"rich_message": {
			"blocks": [
				{"type": "heading", "text": "Special offer", "size": 2},
				{"type": "paragraph", "text": [
					{"type": "bold", "text": "Buy now"},
					{"type": "url", "text": "our site", "url": "https://example.test"}
				]},
				{"type": "photo", "photo": [{"file_id": "secret-file-id"}], "caption": {"text": "Limited time"}}
			]
		}
	}`
	var message api.Message
	if err := json.Unmarshal([]byte(payload), &message); err != nil {
		t.Fatalf("unmarshal rich message: %v", err)
	}

	content := ExtractTextFromMessage(&message)
	for _, expected := range []string{"Special offer", "Buy now", "our site", "https://example.test", "Limited time"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("rich content %q does not contain %q", content, expected)
		}
	}
	if strings.Contains(content, "secret-file-id") {
		t.Fatalf("rich content includes media metadata: %q", content)
	}
	if fullContent := ExtractContentFromMessage(&message); fullContent != content {
		t.Fatalf("full rich content = %q, want %q", fullContent, content)
	}
}

func TestExtractTextFromMessageIgnoresEmptyRichMedia(t *testing.T) {
	t.Parallel()

	message := &api.Message{
		RichMessage: &api.RichMessage{
			Blocks: []api.RichBlock{
				api.RichBlockVideo{
					Type:  "video",
					Video: api.Video{FileID: "video", FileUniqueID: "video-unique"},
				},
			},
		},
	}

	if content := ExtractTextFromMessage(message); content != "" {
		t.Fatalf("empty rich media content = %q, want empty", content)
	}
}
