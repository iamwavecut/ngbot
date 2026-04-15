package handlers

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

const spamCaseStatusFalsePositive = "false_positive"

type parsedNotSpammerReference struct {
	MatchType  string
	MatchValue string
}

func parseNotSpammerReference(input string) (*parsedNotSpammerReference, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	if userID, ok := parsePositiveUserID(input); ok {
		return &parsedNotSpammerReference{
			MatchType:  db.NotSpammerMatchTypeUserID,
			MatchValue: strconv.FormatInt(userID, 10),
		}, nil
	}

	lowerInput := strings.ToLower(input)
	if strings.HasPrefix(lowerInput, "t.me/") || strings.HasPrefix(lowerInput, "telegram.me/") {
		return parseNotSpammerReferenceURL("https://" + input)
	}

	if strings.Contains(input, "://") {
		return parseNotSpammerReferenceURL(input)
	}

	matchType, matchValue, err := db.NormalizeChatNotSpammerMatch(db.NotSpammerMatchTypeUsername, input)
	if err != nil {
		return nil, err
	}
	return &parsedNotSpammerReference{
		MatchType:  matchType,
		MatchValue: matchValue,
	}, nil
}

func parseNotSpammerReferenceURL(raw string) (*parsedNotSpammerReference, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}

	switch strings.ToLower(parsedURL.Scheme) {
	case "tg":
		return parseTGProfileReference(parsedURL)
	case "http", "https":
		return parseHTTPProfileReference(parsedURL)
	default:
		return nil, fmt.Errorf("unsupported URL scheme")
	}
}

func parseTGProfileReference(parsedURL *url.URL) (*parsedNotSpammerReference, error) {
	switch strings.ToLower(parsedURL.Host) {
	case "user":
		userID, ok := parsePositiveUserID(parsedURL.Query().Get("id"))
		if !ok {
			return nil, fmt.Errorf("invalid user id")
		}
		return &parsedNotSpammerReference{
			MatchType:  db.NotSpammerMatchTypeUserID,
			MatchValue: strconv.FormatInt(userID, 10),
		}, nil
	case "resolve":
		domain := strings.TrimSpace(parsedURL.Query().Get("domain"))
		matchType, matchValue, err := db.NormalizeChatNotSpammerMatch(db.NotSpammerMatchTypeUsername, domain)
		if err != nil {
			return nil, err
		}
		return &parsedNotSpammerReference{
			MatchType:  matchType,
			MatchValue: matchValue,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported tg URL")
	}
}

func parseHTTPProfileReference(parsedURL *url.URL) (*parsedNotSpammerReference, error) {
	host := strings.ToLower(parsedURL.Hostname())
	if host != "t.me" && host != "telegram.me" {
		return nil, fmt.Errorf("unsupported host")
	}

	path := strings.Trim(strings.TrimSpace(parsedURL.Path), "/")
	if path == "" || strings.Contains(path, "/") {
		return nil, fmt.Errorf("unsupported profile path")
	}

	matchType, matchValue, err := db.NormalizeChatNotSpammerMatch(db.NotSpammerMatchTypeUsername, path)
	if err != nil {
		return nil, err
	}
	return &parsedNotSpammerReference{
		MatchType:  matchType,
		MatchValue: matchValue,
	}, nil
}

func parsePositiveUserID(value string) (int64, bool) {
	userID, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || userID <= 0 {
		return 0, false
	}
	return userID, true
}

func notSpammerReferenceLabel(override *db.ChatNotSpammerOverride) string {
	if override == nil {
		return ""
	}
	switch override.MatchType {
	case db.NotSpammerMatchTypeUsername:
		return "@" + override.MatchValue
	default:
		return override.MatchValue
	}
}

func (a *Admin) rehabilitateNotSpammerUser(ctx context.Context, chatID int64, userID int64) {
	if a == nil || a.banService == nil || userID <= 0 {
		return
	}

	spamCase, err := a.store.GetActiveSpamCase(ctx, chatID, userID)
	if err == nil && spamCase != nil {
		now := time.Now()
		spamCase.Status = spamCaseStatusFalsePositive
		spamCase.ResolvedAt = &now
		_ = a.store.UpdateSpamCase(ctx, spamCase)
	}

	isRestricted, err := a.banService.IsRestricted(ctx, chatID, userID)
	if err == nil && isRestricted {
		_ = a.banService.UnmuteUser(ctx, chatID, userID)
	}

	member, err := a.getChatMember(chatID, userID)
	if err != nil || member == nil {
		return
	}
	if member.WasKicked() {
		_ = a.banService.UnbanUser(ctx, chatID, userID)
	}
}
