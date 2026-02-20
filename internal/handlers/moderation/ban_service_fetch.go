package handlers

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

func fetchURLs(ctx context.Context, client *http.Client, urls []string) (map[int64]struct{}, error) {
	results := make(map[int64]struct{})
	for _, url := range urls {
		ids, err := fetchURLWithRetry(ctx, client, url)
		if err != nil {
			return nil, err
		}
		for userID := range ids {
			results[userID] = struct{}{}
		}
	}
	return results, nil
}

func fetchURLWithRetry(ctx context.Context, client *http.Client, url string) (map[int64]struct{}, error) {
	var lastErr error
	for attempt := range banServiceMaxRetries {
		ids, err := fetchURL(ctx, client, url)
		if err == nil {
			return ids, nil
		}
		lastErr = err

		if attempt == banServiceMaxRetries-1 {
			break
		}

		backoff := time.Duration(attempt+1) * banServiceRetryStep
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return nil, fmt.Errorf("fetch %s failed after retries: %w", url, lastErr)
}

func fetchURL(ctx context.Context, client *http.Client, url string) (map[int64]struct{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("accept", "text/plain")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	results := make(map[int64]struct{})
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		userIDStr := strings.TrimSpace(scanner.Text())
		if userIDStr == "" {
			continue
		}
		userID, err := strconv.ParseInt(userIDStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse user ID: %w", err)
		}
		results[userID] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan response body: %w", err)
	}
	return results, nil
}

func (s *defaultBanService) getLastDailyFetch(ctx context.Context) (time.Time, error) {
	val, err := s.db.GetKV(ctx, kvKeyLastDailyFetch)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get last daily fetch time: %w", err)
	}
	if val == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, val)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse last daily fetch time: %w", err)
	}
	return t, nil
}

func (s *defaultBanService) getLastHourlyFetch(ctx context.Context) (time.Time, error) {
	val, err := s.db.GetKV(ctx, kvKeyLastHourlyFetch)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get last hourly fetch time: %w", err)
	}
	if val == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, val)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse last hourly fetch time: %w", err)
	}
	return t, nil
}

func (s *defaultBanService) updateLastDailyFetch(ctx context.Context) error {
	return s.db.SetKV(ctx, kvKeyLastDailyFetch, time.Now().Format(time.RFC3339))
}

func (s *defaultBanService) updateLastHourlyFetch(ctx context.Context) error {
	return s.db.SetKV(ctx, kvKeyLastHourlyFetch, time.Now().Format(time.RFC3339))
}

func (s *defaultBanService) FetchKnownBannedDaily(ctx context.Context) error {
	results, err := fetchURLs(ctx, s.httpClient, []string{scammersURL, banlistURL})
	if err != nil {
		return fmt.Errorf("failed to fetch known banned daily: %w", err)
	}

	userIDs := make([]int64, 0, len(results))
	for userID := range results {
		userIDs = append(userIDs, userID)
	}
	if err := s.db.UpsertBanlist(ctx, userIDs); err != nil {
		return fmt.Errorf("failed to upsert banlist: %w", err)
	}
	fullBanlist, err := s.db.GetBanlist(ctx)
	if err != nil {
		return fmt.Errorf("failed to get banlist: %w", err)
	}
	s.setKnownBanned(fullBanlist)
	log.WithField("count", len(fullBanlist)).Debug("fetched known banned ids daily")

	if err := s.updateLastDailyFetch(ctx); err != nil {
		log.WithError(err).Error("Failed to update last daily fetch time")
	}

	return nil
}

func (s *defaultBanService) FetchKnownBanned(ctx context.Context) error {
	results, err := fetchURLs(ctx, s.httpClient, []string{banlistURLHourly})
	if err != nil {
		return fmt.Errorf("failed to fetch known banned hourly: %w", err)
	}
	userIDs := make([]int64, 0, len(results))
	for userID := range results {
		userIDs = append(userIDs, userID)
	}
	if err := s.db.UpsertBanlist(ctx, userIDs); err != nil {
		return fmt.Errorf("failed to upsert banlist: %w", err)
	}
	fullBanlist, err := s.db.GetBanlist(ctx)
	if err != nil {
		return fmt.Errorf("failed to get banlist: %w", err)
	}
	s.setKnownBanned(fullBanlist)
	log.WithField("count", len(fullBanlist)).Debug("fetched known banned ids hourly")

	if err := s.updateLastHourlyFetch(ctx); err != nil {
		log.WithError(err).Error("Failed to update last hourly fetch time")
	}

	return nil
}
