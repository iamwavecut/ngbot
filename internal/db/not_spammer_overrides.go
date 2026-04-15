package db

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var notSpammerUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func NormalizeChatNotSpammerMatch(matchType string, matchValue string) (string, string, error) {
	matchType = strings.TrimSpace(matchType)
	matchValue = strings.TrimSpace(matchValue)

	switch matchType {
	case NotSpammerMatchTypeUserID:
		userID, err := strconv.ParseInt(matchValue, 10, 64)
		if err != nil || userID <= 0 {
			return "", "", fmt.Errorf("invalid user id")
		}
		return matchType, strconv.FormatInt(userID, 10), nil
	case NotSpammerMatchTypeUsername:
		username := NormalizeChatNotSpammerUsername(matchValue)
		if username == "" || !notSpammerUsernamePattern.MatchString(username) {
			return "", "", fmt.Errorf("invalid username")
		}
		return matchType, username, nil
	default:
		return "", "", fmt.Errorf("unsupported match type")
	}
}

func NormalizeChatNotSpammerUsername(username string) string {
	username = strings.TrimSpace(username)
	username = strings.TrimPrefix(username, "@")
	return strings.ToLower(username)
}
