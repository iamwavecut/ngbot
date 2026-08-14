package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/db/sqlite"
	log "github.com/sirupsen/logrus"
)

type settingsCommitBarrierStore struct {
	serviceStore
	firstCommitDone chan struct{}
	releaseFirst    chan struct{}
	calls           atomic.Int32
}

func (s *settingsCommitBarrierStore) SetSettings(ctx context.Context, settings *db.Settings) error {
	_, err := s.CommitSettings(ctx, settings)
	return err
}

func (s *settingsCommitBarrierStore) CommitSettings(ctx context.Context, settings *db.Settings) (*db.Settings, error) {
	committed, err := s.serviceStore.CommitSettings(ctx, settings)
	if s.calls.Add(1) == 1 {
		close(s.firstCommitDone)
		<-s.releaseFirst
	}
	return committed, err
}

type memberWarmupBarrierStore struct {
	serviceStore
	snapshotLoaded  chan struct{}
	releaseSnapshot chan struct{}
}

type memberLookupBarrierStore struct {
	serviceStore
	loaded  chan struct{}
	release chan struct{}
}

func (s *memberLookupBarrierStore) IsMember(ctx context.Context, chatID, userID int64) (bool, error) {
	isMember, err := s.serviceStore.IsMember(ctx, chatID, userID)
	close(s.loaded)
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-s.release:
		return isMember, err
	}
}

func (s *memberWarmupBarrierStore) GetAllMembers(ctx context.Context) (map[int64][]int64, error) {
	members, err := s.serviceStore.GetAllMembers(ctx)
	close(s.snapshotLoaded)
	<-s.releaseSnapshot
	return members, err
}

func TestWarmupCacheUsesStoredMemberIDs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbClient, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = dbClient.Close() })

	const (
		chatID = int64(-100123)
		userID = int64(777)
	)
	if err := dbClient.SetSettings(ctx, db.DefaultSettings(chatID)); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	if err := dbClient.InsertMember(ctx, chatID, userID); err != nil {
		t.Fatalf("insert member: %v", err)
	}

	service := NewService(ctx, &api.BotAPI{}, dbClient, "en", log.NewEntry(log.New()))
	if err := service.warmupCache(ctx); err != nil {
		t.Fatalf("warmup cache: %v", err)
	}

	if _, ok := service.memberCache[chatID][userID]; !ok {
		t.Fatalf("expected real user ID %d to be cached, got %#v", userID, service.memberCache[chatID])
	}
	if _, ok := service.memberCache[chatID][0]; ok {
		t.Fatalf("did not expect slice index 0 to be cached as a user ID")
	}
}

func TestSettingsCacheUsesIndependentSnapshots(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbClient, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = dbClient.Close() })

	const chatID = int64(-100456)
	service := NewService(ctx, &api.BotAPI{}, dbClient, "en", log.NewEntry(log.New()))
	first, err := service.GetSettings(ctx, chatID)
	if err != nil {
		t.Fatalf("get first settings: %v", err)
	}
	first.Language = "ru"

	second, err := service.GetSettings(ctx, chatID)
	if err != nil {
		t.Fatalf("get second settings: %v", err)
	}
	if second.Language == first.Language {
		t.Fatalf("caller mutation leaked into cache: got language %q", second.Language)
	}
}

func TestFailedSettingsWriteDoesNotPublishCache(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbClient, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}

	const chatID = int64(-100789)
	service := NewService(ctx, &api.BotAPI{}, dbClient, "en", log.NewEntry(log.New()))
	before, err := service.GetSettings(ctx, chatID)
	if err != nil {
		t.Fatalf("prime settings cache: %v", err)
	}
	if err := dbClient.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	changed := cloneSettings(before)
	changed.Language = "ru"
	if err := service.SetSettings(ctx, changed); err == nil {
		t.Fatal("expected settings write to fail")
	}
	after, err := service.GetSettings(ctx, chatID)
	if err != nil {
		t.Fatalf("read cached settings after failed write: %v", err)
	}
	if after.Language != before.Language {
		t.Fatalf("failed write changed cache: got %q want %q", after.Language, before.Language)
	}
}

func TestWarmupDoesNotOverwriteNewerSettings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbClient, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = dbClient.Close() })

	const chatID = int64(-100987)
	stored := db.DefaultSettings(chatID)
	stored.Language = "en"
	if err := dbClient.SetSettings(ctx, stored); err != nil {
		t.Fatalf("store settings: %v", err)
	}

	service := NewService(ctx, &api.BotAPI{}, dbClient, "en", log.NewEntry(log.New()))
	newer := cloneSettings(stored)
	newer.Language = "ru"
	service.settingsCache[chatID] = newer
	if err := service.warmupCache(ctx); err != nil {
		t.Fatalf("warmup cache: %v", err)
	}
	if got := service.settingsCache[chatID].Language; got != newer.Language {
		t.Fatalf("warmup overwrote newer cache: got %q want %q", got, newer.Language)
	}
}

func TestSettingsCachePublishesNormalizedCommittedSnapshot(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dbClient, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = dbClient.Close() })

	const chatID = int64(-1001234)
	service := NewService(ctx, &api.BotAPI{}, dbClient, "en", log.NewEntry(log.New()))
	settings := db.DefaultSettings(chatID)
	settings.Enabled = true
	settings.GatekeeperEnabled = false
	settings.GatekeeperCaptchaOptionsCount = 7
	settings.CommunityVotingMinVotersOverride = -9
	if err := service.SetSettings(ctx, settings); err != nil {
		t.Fatalf("set settings: %v", err)
	}

	cached, err := service.GetSettings(ctx, chatID)
	if err != nil {
		t.Fatalf("get cached settings: %v", err)
	}
	stored, err := dbClient.GetSettings(ctx, chatID)
	if err != nil {
		t.Fatalf("get stored settings: %v", err)
	}
	if *cached != *stored {
		t.Fatalf("cache and SQLite diverged: cached=%+v stored=%+v", cached, stored)
	}
	if cached.Enabled || cached.GatekeeperCaptchaOptionsCount != 5 || cached.CommunityVotingMinVotersOverride != db.SettingsOverrideInherit {
		t.Fatalf("cache did not publish normalized settings: %+v", cached)
	}
}

func TestConcurrentSettingsWritesPublishLatestCommit(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dbClient, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = dbClient.Close() })

	store := &settingsCommitBarrierStore{
		serviceStore:    dbClient,
		firstCommitDone: make(chan struct{}),
		releaseFirst:    make(chan struct{}),
	}
	service := NewService(ctx, &api.BotAPI{}, store, "en", log.NewEntry(log.New()))
	const chatID = int64(-1002345)
	first := db.DefaultSettings(chatID)
	first.Language = "en"
	second := db.DefaultSettings(chatID)
	second.Language = "ru"

	firstDone := make(chan error, 1)
	go func() { firstDone <- service.SetSettings(ctx, first) }()
	<-store.firstCommitDone
	secondDone := make(chan error, 1)
	go func() { secondDone <- service.SetSettings(ctx, second) }()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second settings write: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second settings write did not commit while first publication was delayed")
	}
	close(store.releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first settings write: %v", err)
	}

	cached, err := service.GetSettings(ctx, chatID)
	if err != nil {
		t.Fatalf("get cached settings: %v", err)
	}
	stored, err := dbClient.GetSettings(ctx, chatID)
	if err != nil {
		t.Fatalf("get stored settings: %v", err)
	}
	if cached.Language != stored.Language {
		t.Fatalf("cache published stale commit: cached=%q stored=%q", cached.Language, stored.Language)
	}
}

func TestMemberWarmupDoesNotResurrectConcurrentDeletion(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dbClient, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = dbClient.Close() })

	const (
		chatID = int64(-1003456)
		userID = int64(4321)
	)
	if err := dbClient.SetSettings(ctx, db.DefaultSettings(chatID)); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	if err := dbClient.InsertMember(ctx, chatID, userID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	store := &memberWarmupBarrierStore{
		serviceStore:    dbClient,
		snapshotLoaded:  make(chan struct{}),
		releaseSnapshot: make(chan struct{}),
	}
	service := NewService(ctx, &api.BotAPI{}, store, "en", log.NewEntry(log.New()))
	warmupDone := make(chan error, 1)
	go func() { warmupDone <- service.warmupCache(ctx) }()
	<-store.snapshotLoaded
	if err := service.DeleteMember(ctx, chatID, userID); err != nil {
		t.Fatalf("delete member while warmup is paused: %v", err)
	}
	close(store.releaseSnapshot)
	if err := <-warmupDone; err != nil {
		t.Fatalf("complete warmup: %v", err)
	}

	service.cacheMutex.RLock()
	_, resurrected := service.memberCache[chatID][userID]
	service.cacheMutex.RUnlock()
	if resurrected {
		t.Fatal("stale warmup snapshot resurrected a deleted member")
	}
	stored, err := dbClient.IsMember(ctx, chatID, userID)
	if err != nil {
		t.Fatalf("check stored member: %v", err)
	}
	if stored {
		t.Fatal("deleted member survived in SQLite")
	}
}

func TestIsMemberDoesNotCachePositiveLookupAcrossDeleteMember(t *testing.T) {
	ctx := t.Context()
	dbClient, err := sqlite.NewSQLiteClient(ctx, t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = dbClient.Close() })

	const (
		chatID = int64(-1004567)
		userID = int64(9876)
	)
	if err := dbClient.SetSettings(ctx, db.DefaultSettings(chatID)); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	if err := dbClient.InsertMember(ctx, chatID, userID); err != nil {
		t.Fatalf("insert member: %v", err)
	}

	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var result any
		switch r.URL.Path {
		case "/botTEST/getMe":
			result = map[string]any{"id": 1, testTelegramFieldIsBot: true, testTelegramFieldFirstName: "test"}
		case "/botTEST/getChatMember":
			result = map[string]any{"status": "member", "user": map[string]any{"id": userID, testTelegramFieldIsBot: false, testTelegramFieldFirstName: "member"}}
		default:
			t.Fatalf("unexpected Telegram path %q", r.URL.Path)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"ok": true, testTelegramFieldResult: result}); err != nil {
			t.Fatalf("encode Telegram response: %v", err)
		}
	}))
	t.Cleanup(telegram.Close)
	botAPI, err := api.NewBotAPIWithOptions("TEST", api.WithAPIEndpoint(fmt.Sprintf("%s/bot%%s/%%s", telegram.URL)), api.WithHTTPClient(telegram.Client()))
	if err != nil {
		t.Fatalf("new Telegram bot API: %v", err)
	}
	store := &memberLookupBarrierStore{serviceStore: dbClient, loaded: make(chan struct{}), release: make(chan struct{})}
	service := NewService(ctx, botAPI, store, "en", log.NewEntry(log.New()))

	lookupDone := make(chan error, 1)
	go func() {
		_, lookupErr := service.IsMember(ctx, chatID, userID)
		lookupDone <- lookupErr
	}()
	<-store.loaded
	if err := service.DeleteMember(ctx, chatID, userID); err != nil {
		t.Fatalf("delete member while lookup is paused: %v", err)
	}
	close(store.release)
	if err := <-lookupDone; err != nil {
		t.Fatalf("complete membership lookup: %v", err)
	}

	service.cacheMutex.RLock()
	_, resurrected := service.memberCache[chatID][userID]
	service.cacheMutex.RUnlock()
	if resurrected {
		t.Fatal("stale membership lookup republished a deleted member")
	}
}
