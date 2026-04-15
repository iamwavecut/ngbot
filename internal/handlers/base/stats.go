package base

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/iamwavecut/ngbot/internal/i18n"
)

type StatsStore interface {
	GetKV(ctx context.Context, key string) (string, error)
	SetKV(ctx context.Context, key string, value string) error
}

type ChatStatsSummary struct {
	ChallengeStarted int
	ChallengePassed  int
	ChallengeFailed  int
	LLMChecked       int
	HeuristicSpam    int
	SpamConfirmed    int
	FalsePositive    int
}

const (
	StatChallengeStarted = "challenge_started"
	StatChallengePassed  = "challenge_passed"
	StatChallengeFailed  = "challenge_failed"
	StatLLMChecked       = "llm_checked"
	StatHeuristicSpam    = "heuristic_spam"
	StatSpamConfirmed    = "spam_confirmed"
	StatFalsePositive    = "false_positive"
	statsDateLayout      = "2006-01-02"
)

func IncrementDailyStat(ctx context.Context, store StatsStore, chatID int64, metric string) error {
	return IncrementDailyStatAt(ctx, store, chatID, metric, time.Now().UTC())
}

func IncrementDailyStatAt(ctx context.Context, store StatsStore, chatID int64, metric string, now time.Time) error {
	if store == nil || chatID == 0 || metric == "" {
		return nil
	}

	key := StatsKey(chatID, now, metric)
	raw, err := store.GetKV(ctx, key)
	if err != nil {
		return err
	}

	value, err := parseStatValue(raw)
	if err != nil {
		return fmt.Errorf("parse stat value for %s: %w", key, err)
	}

	return store.SetKV(ctx, key, strconv.Itoa(value+1))
}

func LoadStatsSummary(ctx context.Context, store StatsStore, chatID int64, now time.Time, days int) (ChatStatsSummary, error) {
	var summary ChatStatsSummary
	if store == nil || chatID == 0 || days <= 0 {
		return summary, nil
	}

	for i := range days {
		day := now.UTC().AddDate(0, 0, -i)
		var err error
		summary.ChallengeStarted, err = loadStatValue(ctx, store, StatsKey(chatID, day, StatChallengeStarted), summary.ChallengeStarted)
		if err != nil {
			return summary, err
		}
		summary.ChallengePassed, err = loadStatValue(ctx, store, StatsKey(chatID, day, StatChallengePassed), summary.ChallengePassed)
		if err != nil {
			return summary, err
		}
		summary.ChallengeFailed, err = loadStatValue(ctx, store, StatsKey(chatID, day, StatChallengeFailed), summary.ChallengeFailed)
		if err != nil {
			return summary, err
		}
		summary.LLMChecked, err = loadStatValue(ctx, store, StatsKey(chatID, day, StatLLMChecked), summary.LLMChecked)
		if err != nil {
			return summary, err
		}
		summary.HeuristicSpam, err = loadStatValue(ctx, store, StatsKey(chatID, day, StatHeuristicSpam), summary.HeuristicSpam)
		if err != nil {
			return summary, err
		}
		summary.SpamConfirmed, err = loadStatValue(ctx, store, StatsKey(chatID, day, StatSpamConfirmed), summary.SpamConfirmed)
		if err != nil {
			return summary, err
		}
		summary.FalsePositive, err = loadStatValue(ctx, store, StatsKey(chatID, day, StatFalsePositive), summary.FalsePositive)
		if err != nil {
			return summary, err
		}
	}

	return summary, nil
}

func FormatStatsSummary(lang string, summary ChatStatsSummary) string {
	return strings.Join([]string{
		i18n.Get("Last 7 days", lang),
		fmt.Sprintf(i18n.Get("Challenges: %d started, %d passed, %d failed", lang), summary.ChallengeStarted, summary.ChallengePassed, summary.ChallengeFailed),
		fmt.Sprintf(i18n.Get("Spam checks: %d LLM, %d heuristic", lang), summary.LLMChecked, summary.HeuristicSpam),
		fmt.Sprintf(i18n.Get("Outcomes: %d spam, %d false positive", lang), summary.SpamConfirmed, summary.FalsePositive),
	}, "\n")
}

func StatsKey(chatID int64, now time.Time, metric string) string {
	return fmt.Sprintf("stats:%d:%s:%s", chatID, now.UTC().Format(statsDateLayout), metric)
}

func loadStatValue(ctx context.Context, store StatsStore, key string, current int) (int, error) {
	raw, err := store.GetKV(ctx, key)
	if err != nil {
		return current, err
	}
	value, err := parseStatValue(raw)
	if err != nil {
		return current, fmt.Errorf("parse stat value for %s: %w", key, err)
	}
	return current + value, nil
}

func parseStatValue(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	return value, nil
}
