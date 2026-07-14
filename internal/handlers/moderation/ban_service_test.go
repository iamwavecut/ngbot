package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
)

func TestKnownBannedConcurrentAccess(t *testing.T) {
	t.Parallel()

	svc, ok := NewBanService(nil, nil).(*defaultBanService)
	if !ok {
		t.Fatalf("unexpected service type")
	}

	const (
		writers    = 8
		readers    = 8
		iterations = 2000
	)

	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func(offset int64) {
			defer wg.Done()
			for i := range int64(iterations) {
				svc.markKnownBanned(offset*iterations + i)
			}
		}(int64(w + 1))
	}

	for r := range readers {
		wg.Add(1)
		go func(offset int64) {
			defer wg.Done()
			for i := range int64(iterations) {
				_ = svc.IsKnownBanned(offset*iterations + i)
			}
		}(int64(r + 1))
	}

	wg.Go(func() {
		for i := range int64(iterations) {
			svc.setKnownBanned(map[int64]struct{}{i: {}})
		}
	})

	wg.Wait()

	svc.markKnownBanned(42)
	if !svc.IsKnownBanned(42) {
		t.Fatalf("expected marked user to be known banned")
	}
}

func TestSetKnownBannedCopiesInput(t *testing.T) {
	t.Parallel()

	svc, ok := NewBanService(nil, nil).(*defaultBanService)
	if !ok {
		t.Fatalf("unexpected service type")
	}

	input := map[int64]struct{}{1: {}}
	svc.setKnownBanned(input)
	input[2] = struct{}{}

	if svc.IsKnownBanned(2) {
		t.Fatalf("service map should be isolated from caller map")
	}
}

func TestFetchURLParsesCASExportCSVWithHeader(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write([]byte("user_id\n123\n456\n"))
	}))
	t.Cleanup(server.Close)

	ids, err := fetchURL(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("fetch url: %v", err)
	}

	for _, userID := range []int64{123, 456} {
		if _, ok := ids[userID]; !ok {
			t.Fatalf("expected user id %d in fetched ids: %#v", userID, ids)
		}
	}
}

func TestCheckCASBanPositiveAndNotFound(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get(logFieldUserID) {
		case "123":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"reasons":[2],"offenses":1}}`))
		case "456":
			_, _ = w.Write([]byte(`{"ok":false,"description":"Record not found."}`))
		default:
			t.Fatalf("unexpected user id: %s", r.URL.Query().Get(logFieldUserID))
		}
	}))
	t.Cleanup(server.Close)

	client := server.Client()
	banned, err := checkCASBan(context.Background(), client, server.URL+"/check?user_id=%d", 123)
	if err != nil {
		t.Fatalf("check cas positive: %v", err)
	}
	if !banned {
		t.Fatal("expected CAS positive response to be banned")
	}

	banned, err = checkCASBan(context.Background(), client, server.URL+"/check?user_id=%d", 456)
	if err != nil {
		t.Fatalf("check cas not found: %v", err)
	}
	if banned {
		t.Fatal("expected CAS not-found response to be clean")
	}
}

func TestCheckBanRacesProvidersAndPersistsOnlinePositive(t *testing.T) {
	t.Parallel()

	store := newRecordingBanStore()
	svc := &defaultBanService{
		db:          store,
		httpClient:  http.DefaultClient,
		knownBanned: map[int64]struct{}{},
		providers: []banlistProvider{
			{
				name: "slow-clean",
				check: func(ctx context.Context, _ *http.Client, _ int64) (bool, error) {
					select {
					case <-ctx.Done():
						return false, ctx.Err()
					case <-time.After(time.Second):
						return false, nil
					}
				},
			},
			{
				name: "fast-positive",
				check: func(context.Context, *http.Client, int64) (bool, error) {
					return true, nil
				},
			},
		},
	}

	banned, err := svc.CheckBan(context.Background(), 789)
	if err != nil {
		t.Fatalf("check ban: %v", err)
	}
	if !banned {
		t.Fatal("expected fast positive provider to win")
	}
	if !svc.IsKnownBanned(789) {
		t.Fatal("expected online positive to be stored in memory")
	}
	if !store.hasSource(banlistSourceKey{provider: "fast-positive", feedType: banlistFeedOnline}, 789) {
		t.Fatalf("expected online positive to be persisted, got %#v", store.sourceCalls)
	}
	call := store.sourceCalls[0]
	if call.expiresAt == nil || call.expiresAt.Sub(call.seenAt) != banlistOnlineTTL {
		t.Fatalf("expected online positive TTL %s, got %#v", banlistOnlineTTL, call.expiresAt)
	}
}

func TestBootstrapLoadsStoredBanlistBeforeSkippingFetch(t *testing.T) {
	t.Parallel()

	store := newRecordingBanStore()
	store.banlist[200] = struct{}{}
	store.kv[kvKeyLastDailyFetch] = time.Now().Format(time.RFC3339)
	store.kv[kvKeyLastHourlyFetch] = time.Now().Format(time.RFC3339)

	svc := &defaultBanService{
		db:          store,
		httpClient:  http.DefaultClient,
		knownBanned: map[int64]struct{}{},
		providers:   defaultBanlistProviders(),
	}

	if err := svc.bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if !svc.IsKnownBanned(200) {
		t.Fatal("expected stored DB banlist to be loaded into memory")
	}
}

func TestFetchKnownBannedDailyAppliesPartialProviderSuccess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch path.Base(r.URL.Path) {
		case "ok.txt":
			_, _ = w.Write([]byte("300\n"))
		case "fail.txt":
			http.Error(w, "failed", http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	store := newRecordingBanStore()
	failedKey := banlistSourceKey{provider: "fail", feedType: banlistFeedDaily}
	store.sources[failedKey] = map[int64]*time.Time{400: nil}
	store.banlist[400] = struct{}{}
	svc := &defaultBanService{
		db:          store,
		httpClient:  server.Client(),
		knownBanned: map[int64]struct{}{},
		providers: []banlistProvider{
			{
				name:      "ok",
				dailyURLs: []string{server.URL + "/ok.txt"},
			},
			{
				name:      "fail",
				dailyURLs: []string{server.URL + "/fail.txt"},
			},
		},
	}

	if err := svc.FetchKnownBannedDaily(context.Background()); err != nil {
		t.Fatalf("fetch known banned daily: %v", err)
	}
	if !store.hasSource(banlistSourceKey{provider: "ok", feedType: banlistFeedDaily}, 300) {
		t.Fatalf("expected successful provider ID to be persisted, got %#v", store.sourceCalls)
	}
	if !svc.IsKnownBanned(300) {
		t.Fatal("expected successful provider ID to be loaded into memory")
	}
	if !svc.IsKnownBanned(400) {
		t.Fatal("expected failed provider's prior snapshot to remain effective")
	}
	if _, ok := store.sources[failedKey][400]; !ok {
		t.Fatal("expected failed provider snapshot to remain unchanged")
	}
	if got := store.kv[kvKeyLastDailyFetch]; got != "" {
		t.Fatalf("partial refresh moved retry timestamp: %q", got)
	}
}

type recordingBanStore struct {
	kv          map[string]string
	banlist     map[int64]struct{}
	sources     map[banlistSourceKey]map[int64]*time.Time
	sourceCalls []banlistSourceCall
}

type banlistSourceKey struct {
	provider string
	feedType string
}

type banlistSourceCall struct {
	key       banlistSourceKey
	userIDs   []int64
	seenAt    time.Time
	expiresAt *time.Time
	replace   bool
}

func newRecordingBanStore() *recordingBanStore {
	return &recordingBanStore{
		kv:      make(map[string]string),
		banlist: make(map[int64]struct{}),
		sources: make(map[banlistSourceKey]map[int64]*time.Time),
	}
}

func (s *recordingBanStore) GetKV(_ context.Context, key string) (string, error) {
	return s.kv[key], nil
}

func (s *recordingBanStore) SetKV(_ context.Context, key string, value string) error {
	s.kv[key] = value
	return nil
}

func (s *recordingBanStore) ApplyBanlistSource(
	_ context.Context,
	provider, feedType, _ string,
	userIDs []int64,
	seenAt time.Time,
	expiresAt *time.Time,
	replace bool,
) error {
	key := banlistSourceKey{provider: provider, feedType: feedType}
	call := banlistSourceCall{
		key:       key,
		userIDs:   append([]int64(nil), userIDs...),
		seenAt:    seenAt,
		expiresAt: expiresAt,
		replace:   replace,
	}
	s.sourceCalls = append(s.sourceCalls, call)
	if replace || s.sources[key] == nil {
		s.sources[key] = make(map[int64]*time.Time)
	}
	for _, userID := range userIDs {
		s.sources[key][userID] = expiresAt
	}
	s.banlist = make(map[int64]struct{})
	for sourceKey, source := range s.sources {
		for userID, expiry := range source {
			if expiry != nil && !expiry.After(seenAt) {
				delete(source, userID)
				continue
			}
			s.banlist[userID] = struct{}{}
		}
		if len(source) == 0 {
			delete(s.sources, sourceKey)
		}
	}
	return nil
}

func (s *recordingBanStore) GetBanlist(context.Context) (map[int64]struct{}, error) {
	result := make(map[int64]struct{}, len(s.banlist))
	for userID := range s.banlist {
		result[userID] = struct{}{}
	}
	return result, nil
}

func (s *recordingBanStore) AddRestriction(context.Context, *db.UserRestriction) error {
	return nil
}

func (s *recordingBanStore) RemoveRestriction(context.Context, int64, int64) error {
	return nil
}

func (s *recordingBanStore) GetActiveRestriction(context.Context, int64, int64) (*db.UserRestriction, error) {
	return nil, nil
}

func (s *recordingBanStore) hasSource(key banlistSourceKey, userID int64) bool {
	_, ok := s.sources[key][userID]
	return ok
}

func TestCheckBanFailsOpenOnProviderErrors(t *testing.T) {
	t.Parallel()

	svc := &defaultBanService{
		db:          newRecordingBanStore(),
		httpClient:  http.DefaultClient,
		knownBanned: map[int64]struct{}{},
		providers: []banlistProvider{
			{
				name: "broken",
				check: func(context.Context, *http.Client, int64) (bool, error) {
					return false, errors.New("provider is down")
				},
			},
		},
	}

	banned, err := svc.CheckBan(context.Background(), 999)
	if err != nil {
		t.Fatalf("expected provider errors to fail open, got %v", err)
	}
	if banned {
		t.Fatal("expected provider error to be treated as not banned")
	}
}

func TestCheckBanReturnsCallerCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	svc := &defaultBanService{
		db:          newRecordingBanStore(),
		httpClient:  http.DefaultClient,
		knownBanned: map[int64]struct{}{},
		providers: []banlistProvider{
			{
				name: "slow",
				check: func(ctx context.Context, _ *http.Client, _ int64) (bool, error) {
					<-ctx.Done()
					return false, ctx.Err()
				},
			},
		},
	}

	banned, err := svc.CheckBan(ctx, 1000)
	if err == nil {
		t.Fatal("expected caller cancellation error")
	}
	if banned {
		t.Fatal("expected canceled check to be not banned")
	}
}

func TestCheckLoLsBanParsesPositiveResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("id"); got != strconv.FormatInt(1234, 10) {
			t.Fatalf("unexpected id: %s", got)
		}
		_, _ = w.Write([]byte(`{"ok":true,"user_id":1234,"banned":true}`))
	}))
	t.Cleanup(server.Close)

	banned, err := checkLoLsBan(context.Background(), server.Client(), server.URL+"/account?id=%d", 1234)
	if err != nil {
		t.Fatalf("check lols ban: %v", err)
	}
	if !banned {
		t.Fatal("expected LoLs positive response to be banned")
	}
}
