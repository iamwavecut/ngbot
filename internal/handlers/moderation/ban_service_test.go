package handlers

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
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

func TestCheckBanPublishesOnlinePositiveBeforeBlockedPersistence(t *testing.T) {
	t.Parallel()

	store := newBlockingOnlineBanStore()
	store.kv[kvKeyLastDailyFetch] = time.Now().Format(time.RFC3339)
	store.kv[kvKeyLastHourlyFetch] = time.Now().Format(time.RFC3339)
	svc := &defaultBanService{
		db:          store,
		httpClient:  http.DefaultClient,
		knownBanned: map[int64]struct{}{},
		providers: []banlistProvider{
			{
				name: "positive",
				check: func(context.Context, *http.Client, int64) (bool, error) {
					return true, nil
				},
			},
		},
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start ban service: %v", err)
	}
	t.Cleanup(func() {
		store.releasePersistence()
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = svc.Stop(stopCtx)
	})

	banned, err := svc.CheckBan(context.Background(), 789)
	if err != nil {
		t.Fatalf("check ban: %v", err)
	}
	if !banned || !svc.IsKnownBanned(789) {
		t.Fatal("online positive was not published before persistence")
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("online persistence worker did not receive observation")
	}
	if store.hasSource(banlistSourceKey{provider: "positive", feedType: banlistFeedOnline}, 789) {
		t.Fatal("blocked persistence unexpectedly completed")
	}
	store.releasePersistence()
	select {
	case <-store.persisted:
	case <-time.After(time.Second):
		t.Fatal("online persistence did not complete after writer was released")
	}
	if !store.hasSource(banlistSourceKey{provider: "positive", feedType: banlistFeedOnline}, 789) {
		t.Fatal("online positive was not persisted by lifecycle worker")
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
		knownBanned: map[int64]struct{}{400: {}},
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
	if store.getBanlistCalls != 0 {
		t.Fatalf("refresh reloaded the full banlist %d times", store.getBanlistCalls)
	}
}

func TestFetchKnownBannedDailyAppliesProjectionDeltaInMemory(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("200\n"))
	}))
	t.Cleanup(server.Close)

	store := newRecordingBanStore()
	key := banlistSourceKey{provider: "provider", feedType: banlistFeedDaily}
	store.sources[key] = map[int64]*time.Time{100: nil}
	store.banlist[100] = struct{}{}
	svc := &defaultBanService{
		db:          store,
		httpClient:  server.Client(),
		knownBanned: map[int64]struct{}{100: {}},
		providers: []banlistProvider{{
			name:      "provider",
			dailyURLs: []string{server.URL},
		}},
	}

	if err := svc.FetchKnownBannedDaily(context.Background()); err != nil {
		t.Fatalf("fetch known banned daily: %v", err)
	}
	if svc.IsKnownBanned(100) {
		t.Fatal("dropped provider ID remained in memory")
	}
	if !svc.IsKnownBanned(200) {
		t.Fatal("new provider ID was not added to memory")
	}
	if store.getBanlistCalls != 0 {
		t.Fatalf("refresh reloaded the full banlist %d times", store.getBanlistCalls)
	}
}

func TestBanlistProjectionDeltasArePublishedInCommitOrder(t *testing.T) {
	t.Parallel()

	store := &orderedDeltaBanStore{
		recordingBanStore: newRecordingBanStore(),
		firstStarted:      make(chan struct{}),
		secondEntered:     make(chan struct{}),
		releaseFirst:      make(chan struct{}),
	}
	svc := &defaultBanService{
		db:          store,
		knownBanned: map[int64]struct{}{},
	}
	errCh := make(chan error, 2)
	go func() {
		_, _, err := svc.applyBanlistSource(context.Background(), "first", banlistFeedDaily, "first", nil, time.Now(), nil, true)
		errCh <- err
	}()
	<-store.firstStarted
	go func() {
		_, _, err := svc.applyBanlistSource(context.Background(), "second", banlistFeedDaily, "second", nil, time.Now(), nil, true)
		errCh <- err
	}()

	overlapped := false
	select {
	case <-store.secondEntered:
		overlapped = true
	case <-time.After(100 * time.Millisecond):
	}
	close(store.releaseFirst)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("apply ordered banlist delta: %v", err)
		}
	}
	if overlapped {
		t.Fatal("second banlist update reached the store before the first delta was published")
	}
	if svc.IsKnownBanned(42) {
		t.Fatal("out-of-order deltas resurrected an ID removed by the later commit")
	}
}

type recordingBanStore struct {
	kv                  map[string]string
	banlist             map[int64]struct{}
	sources             map[banlistSourceKey]map[int64]*time.Time
	sourceCalls         []banlistSourceCall
	getBanlistCalls     int
	cleanupCalls        int
	cleanupErr          error
	banlistCleanupCalls int
	banlistCleanupErr   error
}

type blockingOnlineBanStore struct {
	*recordingBanStore
	started     chan struct{}
	release     chan struct{}
	persisted   chan struct{}
	releaseOnce sync.Once
}

func (s *blockingOnlineBanStore) releasePersistence() {
	s.releaseOnce.Do(func() { close(s.release) })
}

func newBlockingOnlineBanStore() *blockingOnlineBanStore {
	return &blockingOnlineBanStore{
		recordingBanStore: newRecordingBanStore(),
		started:           make(chan struct{}),
		release:           make(chan struct{}),
		persisted:         make(chan struct{}),
	}
}

func (s *blockingOnlineBanStore) ApplyBanlistSource(
	ctx context.Context,
	provider, feedType, generation string,
	userIDs []int64,
	seenAt time.Time,
	expiresAt *time.Time,
	replace bool,
) ([]int64, []int64, error) {
	close(s.started)
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-s.release:
	}
	added, removed, err := s.recordingBanStore.ApplyBanlistSource(
		ctx,
		provider,
		feedType,
		generation,
		userIDs,
		seenAt,
		expiresAt,
		replace,
	)
	close(s.persisted)
	return added, removed, err
}

type orderedDeltaBanStore struct {
	*recordingBanStore
	mutex         sync.Mutex
	calls         int
	firstStarted  chan struct{}
	secondEntered chan struct{}
	releaseFirst  chan struct{}
}

func (s *orderedDeltaBanStore) ApplyBanlistSource(
	context.Context,
	string, string, string,
	[]int64,
	time.Time,
	*time.Time,
	bool,
) ([]int64, []int64, error) {
	s.mutex.Lock()
	s.calls++
	call := s.calls
	s.mutex.Unlock()
	if call == 1 {
		close(s.firstStarted)
		<-s.releaseFirst
		return []int64{42}, nil, nil
	}
	close(s.secondEntered)
	return nil, []int64{42}, nil
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
) ([]int64, []int64, error) {
	before := maps.Clone(s.banlist)
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
	added := make([]int64, 0)
	for userID := range s.banlist {
		if _, ok := before[userID]; !ok {
			added = append(added, userID)
		}
	}
	removed := make([]int64, 0)
	for userID := range before {
		if _, ok := s.banlist[userID]; !ok {
			removed = append(removed, userID)
		}
	}
	return added, removed, nil
}

func (s *recordingBanStore) GetBanlist(context.Context) (map[int64]struct{}, error) {
	s.getBanlistCalls++
	result := make(map[int64]struct{}, len(s.banlist))
	for userID := range s.banlist {
		result[userID] = struct{}{}
	}
	return result, nil
}

func (s *recordingBanStore) CleanupBanlistSources(context.Context) error {
	s.banlistCleanupCalls++
	return s.banlistCleanupErr
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

func (s *recordingBanStore) RemoveExpiredRestrictions(context.Context) error {
	s.cleanupCalls++
	return s.cleanupErr
}

func (s *recordingBanStore) hasSource(key banlistSourceKey, userID int64) bool {
	_, ok := s.sources[key][userID]
	return ok
}

func TestBanServiceStartCleansExpiredRestrictions(t *testing.T) {
	t.Parallel()

	store := newRecordingBanStore()
	store.kv[kvKeyLastDailyFetch] = time.Now().Format(time.RFC3339)
	store.kv[kvKeyLastHourlyFetch] = time.Now().Format(time.RFC3339)
	service := &defaultBanService{
		db:          store,
		httpClient:  http.DefaultClient,
		knownBanned: map[int64]struct{}{},
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("start ban service: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	if store.cleanupCalls != 1 {
		t.Fatalf("startup cleanup calls = %d, want 1", store.cleanupCalls)
	}
}

func TestBanServiceStartFailsWhenRestrictionCleanupFails(t *testing.T) {
	t.Parallel()

	store := newRecordingBanStore()
	store.cleanupErr = errors.New("database unavailable")
	service := &defaultBanService{db: store, knownBanned: map[int64]struct{}{}}
	if err := service.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "remove expired restrictions") {
		t.Fatalf("start error = %v, want restriction cleanup failure", err)
	}
	if service.started {
		t.Fatal("service started after required cleanup failed")
	}
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
