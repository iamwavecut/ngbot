package handlers

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
)

const messageIDEncodedLen = 6

func encodeChatID(chatID int64) string {
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

func decodeChatID(value string) (int64, error) {
	negative := strings.HasPrefix(value, "~")
	if negative {
		value = strings.TrimPrefix(value, "~")
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, fmt.Errorf("invalid chat id: %w", err)
	}
	if len(data) != 8 {
		return 0, fmt.Errorf("invalid chat id length")
	}
	id := int64(binary.BigEndian.Uint64(data))
	if negative {
		return -id, nil
	}
	return id, nil
}

func encodeUint64Min(value uint64) string {
	if value == 0 {
		return base64.RawURLEncoding.EncodeToString([]byte{0})
	}
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, value)
	i := 0
	for i < len(buf) && buf[i] == 0 {
		i++
	}
	return base64.RawURLEncoding.EncodeToString(buf[i:])
}

func decodeUint64Min(value string) (uint64, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, fmt.Errorf("invalid id: %w", err)
	}
	if len(data) == 0 || len(data) > 8 {
		return 0, fmt.Errorf("invalid id length")
	}
	if len(data) < 8 {
		padded := make([]byte, 8-len(data))
		data = append(padded, data...)
	}
	return binary.BigEndian.Uint64(data), nil
}

func encodeMessageID(messageID int) string {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, uint32(messageID))
	return base64.RawURLEncoding.EncodeToString(buf)
}

func decodeMessageID(value string) (int, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, fmt.Errorf("invalid message id: %w", err)
	}
	if len(data) != 4 {
		return 0, fmt.Errorf("invalid message id length")
	}
	return int(binary.BigEndian.Uint32(data)), nil
}

func splitDeletePayload(payload string) (string, string, bool) {
	if len(payload) <= messageIDEncodedLen+1 {
		return "", "", false
	}
	idx := len(payload) - messageIDEncodedLen - 1
	if idx <= 0 || idx >= len(payload) || payload[idx] != '_' {
		return "", "", false
	}
	return payload[:idx], payload[idx+1:], true
}
