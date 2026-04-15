package base

import (
	"context"
	"testing"
	"time"
)

type statsTestStore struct {
	values map[string]string
}

func (s *statsTestStore) GetKV(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *statsTestStore) SetKV(_ context.Context, key string, value string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	return nil
}

func TestLoadStatsSummaryAggregatesDailyCounters(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	store := &statsTestStore{values: map[string]string{
		StatsKey(42, now, StatChallengeStarted):                  "2",
		StatsKey(42, now.AddDate(0, 0, -1), StatChallengePassed): "1",
		StatsKey(42, now, StatChallengeFailed):                   "3",
		StatsKey(42, now, StatLLMChecked):                        "4",
		StatsKey(42, now.AddDate(0, 0, -2), StatHeuristicSpam):   "2",
		StatsKey(42, now, StatSpamConfirmed):                     "5",
		StatsKey(42, now.AddDate(0, 0, -3), StatFalsePositive):   "1",
	}}

	summary, err := LoadStatsSummary(context.Background(), store, 42, now, 7)
	if err != nil {
		t.Fatalf("LoadStatsSummary returned error: %v", err)
	}

	if summary.ChallengeStarted != 2 || summary.ChallengePassed != 1 || summary.ChallengeFailed != 3 {
		t.Fatalf("unexpected challenge summary: %#v", summary)
	}
	if summary.LLMChecked != 4 || summary.HeuristicSpam != 2 {
		t.Fatalf("unexpected spam check summary: %#v", summary)
	}
	if summary.SpamConfirmed != 5 || summary.FalsePositive != 1 {
		t.Fatalf("unexpected outcome summary: %#v", summary)
	}
}

func TestIncrementDailyStatStoresCounters(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	store := &statsTestStore{values: map[string]string{}}

	if err := IncrementDailyStatAt(context.Background(), store, 42, StatLLMChecked, now); err != nil {
		t.Fatalf("IncrementDailyStatAt returned error: %v", err)
	}
	if err := IncrementDailyStatAt(context.Background(), store, 42, StatLLMChecked, now); err != nil {
		t.Fatalf("IncrementDailyStatAt returned error: %v", err)
	}

	if got := store.values[StatsKey(42, now, StatLLMChecked)]; got != "2" {
		t.Fatalf("unexpected stored stat value: %q", got)
	}
}
