package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestKnownBannedConcurrentAccess(t *testing.T) {
	t.Parallel()

	svc, ok := NewBanService(nil, nil, BanServiceOptions{}).(*defaultBanService)
	if !ok {
		t.Fatalf("unexpected service type")
	}

	const (
		writers    = 8
		readers    = 8
		iterations = 2000
	)
	expiresAt := time.Now().Add(time.Hour)

	var wg sync.WaitGroup
	for w := range writers {
		offset := int64(w + 1)
		wg.Go(func() {
			for i := range int64(iterations) {
				svc.observe(offset*iterations+i, expiresAt)
			}
		})
	}
	for r := range readers {
		offset := int64(r + 1)
		wg.Go(func() {
			for i := range int64(iterations) {
				_ = svc.IsKnownBanned(offset*iterations + i)
			}
		})
	}
	wg.Go(func() {
		for i := range int64(iterations) {
			svc.publishSource("source", []int64{i})
		}
	})
	wg.Wait()

	svc.observe(42, expiresAt)
	if !svc.IsKnownBanned(42) || !svc.IsKnownBanned(iterations-1) {
		t.Fatalf("expected observed and published users to be known banned")
	}
}

func TestFetchBanlistSourceSkipsUserIDHeader(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write([]byte("user_id\n456\n123\n"))
	}))
	t.Cleanup(server.Close)

	fetch, err := fetchBanlistSource(t.Context(), server.Client(), server.URL, "", "", 0)
	if err != nil {
		t.Fatalf("fetch url: %v", err)
	}
	if !slices.Equal(fetch.IDs, []int64{123, 456}) {
		t.Fatalf("fetched ids = %v, want [123 456]", fetch.IDs)
	}
}

func TestCheckCASBanSendsBearerKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" || r.URL.Query().Has("api_key") {
			http.Error(w, `{"ok":false,"error_code":401,"description":"API key required"}`, http.StatusUnauthorized)
			return
		}
		switch r.URL.Query().Get(logFieldUserID) {
		case "123":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"offenses":1}}`))
		case "456":
			_, _ = w.Write([]byte(`{"ok":false,"description":"Record not found."}`))
		default:
			http.Error(w, "unexpected user", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	for userID, want := range map[int64]bool{123: true, 456: false} {
		banned, err := checkCASBan(t.Context(), server.Client(), server.URL+"/check?user_id=%d", "test-key", userID)
		if err != nil || banned != want {
			t.Fatalf("checkCASBan(%d) = %v, %v; want %v, nil", userID, banned, err, want)
		}
	}
}

func TestCheckCASBanRejectedKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":401,"description":"API key required"}`))
	}))
	t.Cleanup(server.Close)

	banned, err := checkCASBan(t.Context(), server.Client(), server.URL+"/check?user_id=%d", "secret-test-key", 123)
	if banned || !errors.Is(err, errCASKeyRejected) {
		t.Fatalf("checkCASBan = %v, %v; want false, errCASKeyRejected", banned, err)
	}
	if strings.Contains(err.Error(), "secret-test-key") || strings.Contains(err.Error(), "user_id=") {
		t.Fatalf("error leaks request details: %q", err)
	}
}

func TestDefaultOnlineBanCheckersWithoutCASKey(t *testing.T) {
	t.Parallel()

	names := func(checkers []onlineBanChecker) []string {
		result := make([]string, 0, len(checkers))
		for _, checker := range checkers {
			result = append(result, checker.name)
		}
		return result
	}
	if got := names(defaultOnlineBanCheckers("")); !slices.Equal(got, []string{onlineCheckerLoLs}) {
		t.Fatalf("checkers without key = %v, want [lols]", got)
	}
	if got := names(defaultOnlineBanCheckers("test-key")); !slices.Equal(got, []string{onlineCheckerLoLs, onlineCheckerCAS}) {
		t.Fatalf("checkers with key = %v, want [lols cas]", got)
	}
}

func TestCASKeyRejectionIsReportedOncePerInterval(t *testing.T) {
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	t.Cleanup(func() { logger.ReplaceHooks(previousHooks) })
	hook := logtest.NewLocal(logger)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":401,"description":"API key required"}`))
	}))
	t.Cleanup(server.Close)

	clock := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	svc := &defaultBanService{
		db:             newRecordingBanStore(),
		httpClient:     server.Client(),
		downloadClient: server.Client(),
		now:            func() time.Time { return clock },
		onlineCheckers: []onlineBanChecker{{
			name: onlineCheckerCAS,
			check: func(ctx context.Context, client *http.Client, userID int64) (bool, error) {
				return checkCASBan(ctx, client, server.URL+"/check?user_id=%d", "rejected-key", userID)
			},
		}},
	}
	rejectionErrors := func() int {
		count := 0
		for _, entry := range hook.AllEntries() {
			if entry.Level == log.ErrorLevel && entry.Message == casKeyRejectedLogMessage {
				count++
			}
		}
		return count
	}

	for i := range 50 {
		banned, err := svc.CheckBan(t.Context(), int64(1_000+i))
		if banned || err != nil {
			t.Fatalf("CheckBan #%d = %v, %v; want false, nil", i, banned, err)
		}
		clock = clock.Add(10 * time.Second)
	}
	if got := rejectionErrors(); got != 1 {
		t.Fatalf("rejection errors within interval = %d, want 1", got)
	}

	clock = clock.Add(casKeyRejectedLogInterval)
	if _, err := svc.CheckBan(t.Context(), 2_000); err != nil {
		t.Fatalf("CheckBan after interval: %v", err)
	}
	if got := rejectionErrors(); got != 2 {
		t.Fatalf("rejection errors after interval = %d, want 2", got)
	}
}

func TestCheckBanRacesProvidersAndPersistsOnlinePositive(t *testing.T) {
	t.Parallel()

	store := newRecordingBanStore()
	svc := &defaultBanService{
		db:         store,
		httpClient: http.DefaultClient,
		onlineCheckers: []onlineBanChecker{
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
	observation, ok := store.observation(789, "fast-positive/online")
	if !ok {
		t.Fatal("expected online positive to be persisted")
	}
	if observation.ExpiresAt.Sub(observation.ObservedAt) != banlistOnlineTTL {
		t.Fatalf("online positive TTL = %s, want %s", observation.ExpiresAt.Sub(observation.ObservedAt), banlistOnlineTTL)
	}
}

func TestCheckBanPublishesOnlinePositiveBeforeBlockedPersistence(t *testing.T) {
	t.Parallel()

	store := newBlockingOnlineBanStore()
	svc := NewBanService(nil, store, BanServiceOptions{}).(*defaultBanService)
	svc.sources = nil
	svc.onlineCheckers = []onlineBanChecker{
		{
			name: moderationTestBanlistProviderPositive,
			check: func(context.Context, *http.Client, int64) (bool, error) {
				return true, nil
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
	source := moderationTestBanlistProviderPositive + "/online"
	if _, ok := store.observation(789, source); ok {
		t.Fatal("blocked persistence unexpectedly completed")
	}
	store.releasePersistence()
	select {
	case <-store.persisted:
	case <-time.After(time.Second):
		t.Fatal("online persistence did not complete after writer was released")
	}
	if _, ok := store.observation(789, source); !ok {
		t.Fatal("online positive was not persisted by lifecycle worker")
	}
}

func TestRefreshIndependentSources(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/broken.txt" {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, "300\n200\n")
	}))
	t.Cleanup(server.Close)

	store := newRecordingBanStore()
	svc := &defaultBanService{
		db:             store,
		httpClient:     server.Client(),
		downloadClient: server.Client(),
		sources: []banlistSourceSpec{
			{name: db.BanlistSourceLoLsScammers, url: server.URL + "/broken.txt", interval: time.Hour},
			{name: db.BanlistSourceLoLsBanlist, url: server.URL + "/banlist.txt", interval: time.Hour},
		},
	}
	loadTestBanlists(t, svc)
	svc.refreshDueSources(t.Context(), time.Now().UTC())

	if saves, _ := store.counts(); saves != 1 {
		t.Fatalf("saves = %d, want 1", saves)
	}
	if !svc.IsKnownBanned(200) || !svc.IsKnownBanned(300) {
		t.Fatal("working source was not published while another source failed")
	}
	if _, ok := store.snapshot(db.BanlistSourceLoLsScammers); ok {
		t.Fatal("failed source stored a snapshot")
	}
}

func TestRefreshNotModifiedOnlyTouches(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == testETag && r.Header.Get("If-Modified-Since") == testLastModified {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = io.WriteString(w, "not-a-number\n")
	}))
	t.Cleanup(server.Close)

	store := newRecordingBanStore()
	snapshot := testBanlistSnapshot(t, db.BanlistSourceLoLsBanlist, []int64{1, 2, 3}, now.Add(-2*time.Hour))
	snapshot.ETag = sql.NullString{String: testETag, Valid: true}
	snapshot.LastModified = sql.NullString{String: testLastModified, Valid: true}
	store.put(snapshot)
	svc := newRefreshTestService(store, server)
	loadTestBanlists(t, svc)
	svc.refreshDueSources(t.Context(), now)

	saves, touches := store.counts()
	stored, _ := store.snapshot(db.BanlistSourceLoLsBanlist)
	if saves != 0 || touches != 1 || !stored.CheckedAt.Equal(now) || !svc.IsKnownBanned(2) {
		t.Fatalf("saves=%d touches=%d checked=%v known=%v", saves, touches, stored.CheckedAt, svc.IsKnownBanned(2))
	}
}

func TestRefreshUnchangedHashOnlyTouches(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "3\n1\n2\n")
	}))
	t.Cleanup(server.Close)

	store := newRecordingBanStore()
	store.put(testBanlistSnapshot(t, db.BanlistSourceLoLsBanlist, []int64{1, 2, 3}, now.Add(-2*time.Hour)))
	svc := newRefreshTestService(store, server)
	loadTestBanlists(t, svc)
	svc.refreshDueSources(t.Context(), now)

	if saves, touches := store.counts(); saves != 0 || touches != 1 {
		t.Fatalf("saves=%d touches=%d, want 0 and 1", saves, touches)
	}
}

func TestRefreshSkipsSourceCheckedWithinInterval(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "1\n")
	}))
	t.Cleanup(server.Close)

	store := newRecordingBanStore()
	store.put(testBanlistSnapshot(t, db.BanlistSourceLoLsBanlist, []int64{1, 2, 3}, now.Add(-30*time.Minute)))
	svc := newRefreshTestService(store, server)
	loadTestBanlists(t, svc)
	svc.refreshDueSources(t.Context(), now)

	if got := requests.Load(); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}
}

func TestRestartDoesNotRefetchFreshSources(t *testing.T) {
	t.Parallel()

	var mutex sync.Mutex
	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		requests[r.URL.Path]++
		mutex.Unlock()
		_, _ = io.WriteString(w, "10\n20\n")
	}))
	t.Cleanup(server.Close)

	store := newRecordingBanStore()
	newService := func() *defaultBanService {
		return &defaultBanService{
			db:             store,
			httpClient:     server.Client(),
			downloadClient: server.Client(),
			sources: []banlistSourceSpec{
				{name: db.BanlistSourceLoLsBanlist, url: server.URL + "/banlist.txt", interval: time.Hour},
				{name: db.BanlistSourceLoLsScammers, url: server.URL + "/scammers.txt", interval: time.Hour},
			},
		}
	}

	first := newService()
	if err := first.Start(t.Context()); err != nil {
		t.Fatalf("start first service: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for saves, _ := store.counts(); saves < 2; saves, _ = store.counts() {
		if time.Now().After(deadline) {
			t.Fatalf("startup refresh saved %d snapshots, want 2", saves)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := first.Stop(t.Context()); err != nil {
		t.Fatalf("stop first service: %v", err)
	}

	second := newService()
	if err := second.Start(t.Context()); err != nil {
		t.Fatalf("start second service: %v", err)
	}
	second.refreshDueSources(t.Context(), time.Now().UTC())
	if err := second.Stop(t.Context()); err != nil {
		t.Fatalf("stop second service: %v", err)
	}
	if !second.IsKnownBanned(10) {
		t.Fatal("restarted service did not load the stored snapshot")
	}

	mutex.Lock()
	defer mutex.Unlock()
	if requests["/banlist.txt"] != 1 || requests["/scammers.txt"] != 1 {
		t.Fatalf("requests = %v, want one per source", requests)
	}
}

func TestRefreshKeepsSnapshotOnEmptyBody(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	store := newRecordingBanStore()
	store.put(testBanlistSnapshot(t, db.BanlistSourceLoLsBanlist, sequentialIDs(1_000), now.Add(-2*time.Hour)))
	svc := newRefreshTestService(store, server)
	loadTestBanlists(t, svc)
	svc.refreshDueSources(t.Context(), now)
	svc.refreshDueSources(t.Context(), now.Add(61*time.Minute))

	stored, _ := store.snapshot(db.BanlistSourceLoLsBanlist)
	if saves, _ := store.counts(); saves != 0 || stored.IDCount != 1_000 || !svc.IsKnownBanned(500) {
		t.Fatalf("empty body changed the snapshot: saves=%d count=%d known=%v", saves, stored.IDCount, svc.IsKnownBanned(500))
	}
}

func TestShrinkGuardNeedsConfirmation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for _, id := range sequentialIDs(500) {
			_, _ = io.WriteString(w, strconv.FormatInt(id, 10)+"\n")
		}
	}))
	t.Cleanup(server.Close)

	store := newRecordingBanStore()
	store.put(testBanlistSnapshot(t, db.BanlistSourceLoLsBanlist, sequentialIDs(1_000), now.Add(-2*time.Hour)))
	svc := newRefreshTestService(store, server)
	loadTestBanlists(t, svc)

	for _, at := range []time.Time{now, now.Add(30 * time.Minute)} {
		svc.refreshDueSources(t.Context(), at)
		if saves, _ := store.counts(); saves != 0 || !svc.IsKnownBanned(700) {
			t.Fatalf("shrink applied at %v without confirmation", at)
		}
	}
	svc.refreshDueSources(t.Context(), now.Add(61*time.Minute))
	stored, _ := store.snapshot(db.BanlistSourceLoLsBanlist)
	if saves, _ := store.counts(); saves != 1 || stored.IDCount != 500 || svc.IsKnownBanned(700) || !svc.IsKnownBanned(400) {
		t.Fatalf("confirmed shrink not applied: saves=%d count=%d", saves, stored.IDCount)
	}
}

func TestRefreshOfConvertedLegacyUnion(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		served    []int64
		wantSaves int
		wantCount int
	}{
		{name: "first download replaces the union", served: sequentialIDs(500), wantSaves: 1, wantCount: 500},
		{name: "empty first download is held", served: nil, wantSaves: 0, wantCount: 1_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for _, id := range tc.served {
					_, _ = io.WriteString(w, strconv.FormatInt(id, 10)+"\n")
				}
			}))
			t.Cleanup(server.Close)

			store := newRecordingBanStore()
			store.put(testBanlistSnapshot(t, db.BanlistSourceLoLsBanlist, sequentialIDs(1_000), time.Unix(0, 0).UTC()))
			svc := newRefreshTestService(store, server)
			loadTestBanlists(t, svc)
			svc.refreshDueSources(t.Context(), now)

			stored, _ := store.snapshot(db.BanlistSourceLoLsBanlist)
			if saves, _ := store.counts(); saves != tc.wantSaves || stored.IDCount != tc.wantCount {
				t.Fatalf("saves=%d count=%d, want %d/%d", saves, stored.IDCount, tc.wantSaves, tc.wantCount)
			}
		})
	}
}

func TestRefreshDownloadOutlivesOnlineCheckTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, "3\n4\n")
	}))
	t.Cleanup(server.Close)

	store := newRecordingBanStore()
	svc := newRefreshTestService(store, server)
	svc.httpClient = &http.Client{Timeout: 20 * time.Millisecond}
	loadTestBanlists(t, svc)
	svc.refreshDueSources(t.Context(), time.Now().UTC())

	if saves, _ := store.counts(); saves != 1 || !svc.IsKnownBanned(4) {
		t.Fatalf("slow list download was not applied: saves=%d", saves)
	}
}

func TestOnlineBanDuringStartIsPersisted(t *testing.T) {
	t.Parallel()

	store := newRecordingBanStore()
	svc := NewBanService(nil, store, BanServiceOptions{}).(*defaultBanService)
	svc.sources = nil
	svc.onlineCheckers = []onlineBanChecker{{
		name:  onlineCheckerLoLs,
		check: func(context.Context, *http.Client, int64) (bool, error) { return true, nil },
	}}
	const users = 20
	var wg sync.WaitGroup
	wg.Go(func() {
		if err := svc.Start(t.Context()); err != nil {
			t.Errorf("start: %v", err)
		}
	})
	wg.Go(func() {
		for userID := range int64(users) {
			if banned, err := svc.CheckBan(t.Context(), userID+1); err != nil || !banned {
				t.Errorf("CheckBan(%d) = %v, %v", userID+1, banned, err)
			}
		}
	})
	wg.Wait()

	deadline := time.Now().Add(5 * time.Second)
	for userID := int64(1); userID <= users; userID++ {
		for _, ok := store.observation(userID, onlineCheckerLoLs+banlistOnlineSourceSuffix); !ok; _, ok = store.observation(userID, onlineCheckerLoLs+banlistOnlineSourceSuffix) {
			if time.Now().After(deadline) {
				t.Fatalf("online ban of user %d was not persisted", userID)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if err := svc.Stop(t.Context()); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestFrozenSourceIsNeverFetched(t *testing.T) {
	t.Parallel()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "1\n")
	}))
	t.Cleanup(server.Close)

	store := newRecordingBanStore()
	snapshot := testBanlistSnapshot(t, db.BanlistSourceCASLegacy, []int64{77}, time.Unix(0, 0))
	snapshot.Frozen = true
	store.put(snapshot)
	svc := &defaultBanService{
		db:             store,
		httpClient:     server.Client(),
		downloadClient: server.Client(),
		sources:        []banlistSourceSpec{{name: db.BanlistSourceCASLegacy, url: server.URL, interval: time.Hour}},
	}
	loadTestBanlists(t, svc)
	svc.refreshDueSources(t.Context(), time.Now().UTC())

	if requests.Load() != 0 || !svc.IsKnownBanned(77) {
		t.Fatalf("frozen source: requests=%d known=%v", requests.Load(), svc.IsKnownBanned(77))
	}
}

func TestCorruptSnapshotIsRefetched(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = io.WriteString(w, "9\n")
	}))
	t.Cleanup(server.Close)

	store := newRecordingBanStore()
	payload := []byte{0x02}
	sum := sha256.Sum256(payload)
	store.put(&db.BanlistSnapshot{
		Source:        db.BanlistSourceLoLsBanlist,
		Format:        db.BanlistSnapshotFormatV1,
		IDCount:       5,
		Payload:       payload,
		PayloadSHA256: sum[:],
		ETag:          sql.NullString{String: testETag, Valid: true},
		ChangedAt:     now,
		CheckedAt:     now,
	})
	svc := newRefreshTestService(store, server)
	loadTestBanlists(t, svc)
	if svc.IsKnownBanned(1) {
		t.Fatal("corrupt payload was loaded")
	}
	svc.refreshDueSources(t.Context(), now)

	if saves, _ := store.counts(); saves != 1 || !svc.IsKnownBanned(9) {
		t.Fatalf("corrupt snapshot was not refetched: saves=%d", saves)
	}
}

func TestObservationsExpire(t *testing.T) {
	t.Parallel()

	clock := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	store := newRecordingBanStore()
	svc := &defaultBanService{
		db:         store,
		httpClient: http.DefaultClient,
		now:        func() time.Time { return clock },
		onlineCheckers: []onlineBanChecker{{
			name:  onlineCheckerLoLs,
			check: func(context.Context, *http.Client, int64) (bool, error) { return true, nil },
		}},
	}
	if banned, err := svc.CheckBan(t.Context(), 42); err != nil || !banned {
		t.Fatalf("CheckBan = %v, %v; want true, nil", banned, err)
	}
	if observation, ok := store.observation(42, "lols/online"); !ok || !observation.ExpiresAt.Equal(clock.Add(banlistOnlineTTL)) {
		t.Fatalf("persisted observation = %+v, %v", observation, ok)
	}

	clock = clock.Add(23 * time.Hour)
	if !svc.IsKnownBanned(42) {
		t.Fatal("observation expired before its TTL")
	}
	clock = clock.Add(2 * time.Hour)
	if svc.IsKnownBanned(42) {
		t.Fatal("observation is visible after its TTL")
	}
	svc.pruneObservations(t.Context(), clock)
	if _, ok := store.observation(42, "lols/online"); ok {
		t.Fatal("expired observation was not pruned")
	}
}

func TestHourlyFeedCreatesObservations(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "5\n6\n")
	}))
	t.Cleanup(server.Close)

	store := newRecordingBanStore()
	newService := func(at time.Time) *defaultBanService {
		return &defaultBanService{
			db:             store,
			httpClient:     server.Client(),
			downloadClient: server.Client(),
			now:            func() time.Time { return at },
			sources: []banlistSourceSpec{{
				name: db.BanlistSourceLoLsHourly, url: server.URL, interval: time.Hour, observationTTL: banlistHourlyTTL,
			}},
		}
	}
	svc := newService(now)
	loadTestBanlists(t, svc)
	svc.refreshDueSources(t.Context(), now)

	for _, userID := range []int64{5, 6} {
		observation, ok := store.observation(userID, db.BanlistSourceLoLsHourly)
		if !ok || !observation.ObservedAt.Equal(now) || !observation.ExpiresAt.Equal(now.Add(banlistHourlyTTL)) {
			t.Fatalf("observation %d = %+v, %v", userID, observation, ok)
		}
	}
	if !svc.IsKnownBanned(5) {
		t.Fatal("hourly feed ID is not known banned")
	}

	restarted := newService(now.Add(10 * time.Minute))
	loadTestBanlists(t, restarted)
	restarted.refreshDueSources(t.Context(), now.Add(10*time.Minute))
	if got := requests.Load(); got != 1 {
		t.Fatalf("hourly feed requests after restart = %d, want 1", got)
	}
}

func newRefreshTestService(store *recordingBanStore, server *httptest.Server) *defaultBanService {
	return &defaultBanService{
		db:             store,
		httpClient:     server.Client(),
		downloadClient: server.Client(),
		sources:        []banlistSourceSpec{{name: db.BanlistSourceLoLsBanlist, url: server.URL, interval: time.Hour}},
	}
}

func loadTestBanlists(t *testing.T, svc *defaultBanService) {
	t.Helper()
	if err := svc.loadBanlists(t.Context()); err != nil {
		t.Fatalf("load banlists: %v", err)
	}
}

func sequentialIDs(n int) []int64 {
	ids := make([]int64, n)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	return ids
}

func testBanlistSnapshot(t *testing.T, source string, ids []int64, checkedAt time.Time) *db.BanlistSnapshot {
	t.Helper()
	payload, err := db.EncodeBanlistIDs(ids)
	if err != nil {
		t.Fatalf("encode banlist IDs: %v", err)
	}
	sum := sha256.Sum256(payload)
	return &db.BanlistSnapshot{
		Source:        source,
		Format:        db.BanlistSnapshotFormatV1,
		IDCount:       len(ids),
		Payload:       payload,
		PayloadSHA256: sum[:],
		ChangedAt:     checkedAt,
		CheckedAt:     checkedAt,
	}
}

type observationKey struct {
	userID int64
	source string
}

type recordingBanStore struct {
	mutex          sync.Mutex
	snapshots      map[string]db.BanlistSnapshot
	observations   map[observationKey]db.BanlistObservation
	saves          int
	touches        int
	cleanupCalls   int
	cleanupErr     error
	retentionCalls int
	retentionLimit int
}

func newRecordingBanStore() *recordingBanStore {
	return &recordingBanStore{
		snapshots:    make(map[string]db.BanlistSnapshot),
		observations: make(map[observationKey]db.BanlistObservation),
	}
}

func (s *recordingBanStore) put(snapshot *db.BanlistSnapshot) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	stored := *snapshot
	stored.Payload = slices.Clone(snapshot.Payload)
	s.snapshots[snapshot.Source] = stored
}

func (s *recordingBanStore) snapshot(source string) (db.BanlistSnapshot, bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	snapshot, ok := s.snapshots[source]
	return snapshot, ok
}

func (s *recordingBanStore) observation(userID int64, source string) (db.BanlistObservation, bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	observation, ok := s.observations[observationKey{userID: userID, source: source}]
	return observation, ok
}

func (s *recordingBanStore) counts() (saves, touches int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.saves, s.touches
}

func (s *recordingBanStore) BanlistSnapshots(context.Context) ([]db.BanlistSnapshot, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	snapshots := make([]db.BanlistSnapshot, 0, len(s.snapshots))
	for _, snapshot := range s.snapshots {
		snapshot.Payload = slices.Clone(snapshot.Payload)
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func (s *recordingBanStore) SaveBanlistSnapshot(ctx context.Context, snapshot *db.BanlistSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.put(snapshot)
	s.mutex.Lock()
	s.saves++
	s.mutex.Unlock()
	return nil
}

func (s *recordingBanStore) TouchBanlistSnapshot(_ context.Context, source string, etag, lastModified sql.NullString, checkedAt time.Time) (bool, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	snapshot, ok := s.snapshots[source]
	if !ok {
		return false, nil
	}
	snapshot.ETag = etag
	snapshot.LastModified = lastModified
	snapshot.CheckedAt = checkedAt
	s.snapshots[source] = snapshot
	s.touches++
	return true, nil
}

func (s *recordingBanStore) BanlistObservations(_ context.Context, now time.Time) ([]db.BanlistObservation, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	observations := make([]db.BanlistObservation, 0, len(s.observations))
	for _, observation := range s.observations {
		if observation.ExpiresAt.After(now) {
			observations = append(observations, observation)
		}
	}
	return observations, nil
}

func (s *recordingBanStore) UpsertBanlistObservations(_ context.Context, observations []db.BanlistObservation) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	for _, observation := range observations {
		key := observationKey{userID: observation.UserID, source: observation.Source}
		if existing, ok := s.observations[key]; ok && existing.ExpiresAt.After(observation.ExpiresAt) {
			observation.ExpiresAt = existing.ExpiresAt
		}
		s.observations[key] = observation
	}
	return nil
}

func (s *recordingBanStore) DeleteExpiredBanlistObservations(_ context.Context, now time.Time, limit int) (int64, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	var deleted int64
	for key, observation := range s.observations {
		if deleted == int64(limit) {
			break
		}
		if !observation.ExpiresAt.After(now) {
			delete(s.observations, key)
			deleted++
		}
	}
	return deleted, nil
}

func (s *recordingBanStore) CleanupRetainedRecords(_ context.Context, _ time.Time, limit int) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.retentionCalls++
	s.retentionLimit = limit
	return nil
}

func (s *recordingBanStore) AddRestriction(context.Context, *db.UserRestriction) error {
	return nil
}

func (s *recordingBanStore) EnsureRestrictionSnapshot(_ context.Context, restriction *db.UserRestriction) (*db.UserRestriction, error) {
	return restriction, nil
}

func (s *recordingBanStore) RemoveRestriction(context.Context, int64, int64) error {
	return nil
}

func (s *recordingBanStore) GetActiveRestriction(context.Context, int64, int64) (*db.UserRestriction, error) {
	return nil, nil
}

func (s *recordingBanStore) GetRestriction(context.Context, int64, int64) (*db.UserRestriction, error) {
	return nil, nil
}

func (s *recordingBanStore) RemoveExpiredRestrictions(context.Context) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.cleanupCalls++
	return s.cleanupErr
}

type periodicRetentionStore struct {
	*recordingBanStore
	mutex    sync.Mutex
	calls    int
	periodic chan struct{}
	once     sync.Once
}

func (s *periodicRetentionStore) CleanupRetainedRecords(context.Context, time.Time, int) error {
	s.mutex.Lock()
	s.calls++
	calls := s.calls
	s.mutex.Unlock()
	if calls >= 2 {
		s.once.Do(func() { close(s.periodic) })
	}
	return nil
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

func (s *blockingOnlineBanStore) UpsertBanlistObservations(ctx context.Context, observations []db.BanlistObservation) error {
	close(s.started)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
	}
	err := s.recordingBanStore.UpsertBanlistObservations(ctx, observations)
	close(s.persisted)
	return err
}

func TestBanServiceStartCleansExpiredRestrictions(t *testing.T) {
	t.Parallel()

	store := newRecordingBanStore()
	service := &defaultBanService{
		db:         store,
		httpClient: http.DefaultClient,
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("start ban service: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.cleanupCalls != 1 {
		t.Fatalf("startup cleanup calls = %d, want 1", store.cleanupCalls)
	}
	if store.retentionCalls != 1 || store.retentionLimit != retainedRecordsCleanupBatchSize {
		t.Fatalf("startup retention cleanup = %d calls with limit %d", store.retentionCalls, store.retentionLimit)
	}
}

func TestBanServiceRunsRetentionCleanupPeriodically(t *testing.T) {
	store := &periodicRetentionStore{
		recordingBanStore: newRecordingBanStore(),
		periodic:          make(chan struct{}),
	}
	service := &defaultBanService{
		db:                  store,
		httpClient:          http.DefaultClient,
		maintenanceInterval: time.Millisecond,
	}
	if err := service.Start(t.Context()); err != nil {
		t.Fatalf("start ban service: %v", err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })

	select {
	case <-store.periodic:
	case <-time.After(time.Second):
		t.Fatal("periodic retention cleanup did not run")
	}
}

func TestBanServiceStartFailsWhenRestrictionCleanupFails(t *testing.T) {
	t.Parallel()

	store := newRecordingBanStore()
	store.cleanupErr = errors.New("database unavailable")
	service := &defaultBanService{db: store}
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
		db:         newRecordingBanStore(),
		httpClient: http.DefaultClient,
		onlineCheckers: []onlineBanChecker{
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
		db:         newRecordingBanStore(),
		httpClient: http.DefaultClient,
		onlineCheckers: []onlineBanChecker{
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
