package handlers

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

type (
	banlistSourceSpec struct {
		name           string
		url            string
		interval       time.Duration
		observationTTL time.Duration
	}

	banlistFetch struct {
		NotModified  bool
		IDs          []int64
		ETag         string
		LastModified string
	}
)

func defaultBanlistSources() []banlistSourceSpec {
	return []banlistSourceSpec{
		{name: db.BanlistSourceLoLsBanlist, url: banlistURL, interval: time.Hour},
		{name: db.BanlistSourceLoLsScammers, url: scammersURL, interval: time.Hour},
		{name: db.BanlistSourceLoLsHourly, url: banlistURLHourly, interval: time.Hour, observationTTL: banlistHourlyTTL},
	}
}

func fetchBanlistSource(ctx context.Context, client *http.Client, url, etag, lastModified string, sizeHint int) (banlistFetch, error) {
	var lastErr error
	for attempt := range banServiceMaxRetries {
		fetch, err := fetchBanlistSourceOnce(ctx, client, url, etag, lastModified, sizeHint)
		if err == nil {
			return fetch, nil
		}
		lastErr = err

		if attempt == banServiceMaxRetries-1 {
			break
		}

		backoff := time.Duration(attempt+1) * banServiceRetryStep
		select {
		case <-ctx.Done():
			return banlistFetch{}, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return banlistFetch{}, fmt.Errorf("fetch %s failed after retries: %w", url, lastErr)
}

func fetchBanlistSourceOnce(ctx context.Context, client *http.Client, url, etag, lastModified string, sizeHint int) (banlistFetch, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return banlistFetch{}, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "text/plain")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}

	resp, err := client.Do(req)
	if err != nil {
		return banlistFetch{}, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	fetch := banlistFetch{
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
	}
	if resp.StatusCode == http.StatusNotModified {
		fetch.NotModified = true
		if fetch.ETag == "" {
			fetch.ETag = etag
		}
		if fetch.LastModified == "" {
			fetch.LastModified = lastModified
		}
		return fetch, nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return banlistFetch{}, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	ids, err := parseBanlistIDs(resp.Body, sizeHint)
	if err != nil {
		return banlistFetch{}, err
	}
	fetch.IDs = db.NormalizeBanlistIDs(ids)
	return fetch, nil
}

func parseBanlistIDs(body io.Reader, sizeHint int) ([]int64, error) {
	ids := make([]int64, 0, max(sizeHint+sizeHint/banlistSizeHintSlackDivisor, banlistMinParseCapacity))
	reader := csv.NewReader(body)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	reader.ReuseRecord = true
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read CSV response: %w", err)
		}
		if len(record) == 0 {
			continue
		}
		userIDStr := strings.TrimSpace(record[0])
		if userIDStr == "" {
			continue
		}
		switch strings.ToLower(userIDStr) {
		case "id", logFieldUserID:
			continue
		}
		userID, err := strconv.ParseInt(userIDStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to parse user ID: %w", err)
		}
		ids = append(ids, userID)
	}
	return ids, nil
}
