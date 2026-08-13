package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	dbsqlite "github.com/iamwavecut/ngbot/internal/db/sqlite"
)

type blockingNotSpammerStore struct {
	gatekeeperStore
	entered chan struct{}
	release chan struct{}
}

func (s *blockingNotSpammerStore) IsChatNotSpammer(ctx context.Context, _ int64, _ int64, _ string) (bool, error) {
	close(s.entered)
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-s.release:
		return false, nil
	}
}

func TestPublicChallengePersistsRestrictionActionBeforeTelegram(t *testing.T) {
	t.Parallel()

	client, err := dbsqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	restrictEntered := make(chan struct{})
	restrictRelease := make(chan struct{})
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		switch method {
		case testTelegramMethodRestrictChatMember:
			close(restrictEntered)
			<-restrictRelease
			return true
		case testTelegramMethodSendMessage:
			return map[string]any{logFieldMessageID: 99}
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})
	settings := &db.Settings{
		ID:                            -1001,
		GatekeeperEnabled:             true,
		GatekeeperCaptchaEnabled:      true,
		GatekeeperCaptchaOptionsCount: 3,
		ChallengeTimeout:              time.Minute.Nanoseconds(),
	}
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: settings},
		store:      client,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
		Variants:   map[string]map[string]string{"en": {"A": "apple", "B": "book", "C": "vehicle"}},
	}
	chat := api.Chat{ID: settings.ID, Title: testGroupTitle, Type: testChatTypeSupergroup}
	user := api.User{ID: 2001, FirstName: testFirstNameUser}
	done := make(chan error, 1)
	go func() {
		done <- gatekeeper.startChallenge(t.Context(), nil, &user, &chat, chat.ID, chat.ID, settings)
	}()
	select {
	case <-restrictEntered:
	case <-time.After(time.Second):
		t.Fatal("restrictChatMember was not called")
	}
	challenge, err := client.GetChallengeByChatUser(t.Context(), chat.ID, user.ID)
	close(restrictRelease)
	startErr := <-done
	if err != nil {
		t.Fatalf("load challenge while restriction blocked: %v", err)
	}
	if challenge == nil || challenge.Status != db.ChallengeStatusRestrictPending || challenge.ActionOwner == "" || !challenge.ActionLeaseUntil.Valid {
		t.Fatalf("restriction was not durably owned before Telegram: %#v", challenge)
	}
	if startErr != nil {
		t.Fatalf("start challenge: %v", startErr)
	}
}

func TestDirectAndSchedulerShareOneChallengeActionLease(t *testing.T) {
	t.Parallel()

	client, err := dbsqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	firstEntered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != testTelegramMethodBanChatMember {
			t.Fatalf("unexpected bot method: %s", method)
		}
		if calls.Add(1) == 1 {
			close(firstEntered)
		}
		<-release
		return true
	})
	now := time.Now()
	challenge := &db.Challenge{
		CommChatID:     -1002,
		UserID:         2002,
		ChatID:         -1002,
		Status:         db.ChallengeStatusRejectPending,
		UserRestricted: true,
		CreatedAt:      now,
		ExpiresAt:      now.Add(time.Minute),
		NextAttemptAt:  sql.NullTime{Time: now, Valid: true},
	}
	if _, err := client.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI}, settings: webAppSettings()},
		store:      client,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}
	var wg sync.WaitGroup
	wg.Go(func() { _ = gatekeeper.processChallengeAction(t.Context(), challenge) })
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first ban action did not start")
	}
	wg.Go(func() { _ = gatekeeper.processDueChallengeActions(t.Context()) })
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("direct handler and scheduler duplicated action: calls=%d", got)
	}
}

func TestTransientFallbackGetChatFailureRetriesWithoutDecline(t *testing.T) {
	t.Parallel()

	client, err := dbsqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	var declines atomic.Int32
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		switch method {
		case testTelegramMethodGetChat:
			return &testBotAPIError{code: http.StatusBadGateway, description: "temporary upstream failure"}
		case testTelegramMethodJoinRequestQuery, testTelegramMethodDeclineJoinRequest:
			declines.Add(1)
			return true
		default:
			return true
		}
	})
	now := time.Now()
	challenge := &db.Challenge{
		CommChatID:         2003,
		UserID:             2003,
		ChatID:             -1003,
		Status:             db.ChallengeStatusWebAppFallbackPending,
		WebAppToken:        "token",
		JoinRequestQueryID: "query",
		CreatedAt:          now,
		ExpiresAt:          now.Add(time.Minute),
		NextAttemptAt:      sql.NullTime{Time: now, Valid: true},
	}
	if _, err := client.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI}, settings: webAppSettings()},
		store:      client,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}
	_ = gatekeeper.processChallengeAction(t.Context(), challenge)
	if got := declines.Load(); got != 0 {
		t.Fatalf("transient getChat failure declined join request: calls=%d", got)
	}
	stored, err := client.GetChallengeByChatUser(t.Context(), challenge.ChatID, challenge.UserID)
	if err != nil {
		t.Fatalf("load retry state: %v", err)
	}
	if stored == nil || stored.Status != db.ChallengeStatusWebAppFallbackPending || stored.AttemptCount != 1 || !stored.NextAttemptAt.Valid {
		t.Fatalf("transient fallback was not retried durably: %#v", stored)
	}
}

func TestNoRightsNoticeFailureRetainsDurableRetry(t *testing.T) {
	t.Parallel()

	client, err := dbsqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method == testTelegramMethodSendMessage {
			return &testBotAPIError{code: http.StatusBadGateway, description: "temporary send failure"}
		}
		return true
	})
	now := time.Now()
	challenge := &db.Challenge{
		CommChatID:    -1004,
		UserID:        2004,
		ChatID:        -1004,
		Status:        db.ChallengeStatusRejectPending,
		CreatedAt:     now,
		ExpiresAt:     now.Add(time.Minute),
		NextAttemptAt: sql.NullTime{Time: now, Valid: true},
	}
	if _, err := client.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      client,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{moderationUnavailable: true},
	}
	_ = gatekeeper.processChallengeAction(t.Context(), challenge)
	stored, err := client.GetChallengeByChatUser(t.Context(), challenge.ChatID, challenge.UserID)
	if err != nil {
		t.Fatalf("load no-rights retry: %v", err)
	}
	if stored == nil || stored.Status != db.ChallengeStatusRejectPending || stored.AttemptCount != 1 || !stored.NextAttemptAt.Valid {
		t.Fatalf("notice failure erased durable state: %#v", stored)
	}
}

func TestInvalidJoinQueryRemainsDurableUntilReconciliation(t *testing.T) {
	t.Parallel()

	client, err := dbsqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method != testTelegramMethodJoinRequestQuery {
			t.Fatalf("unexpected bot method: %s", method)
		}
		return &testBotAPIError{code: http.StatusBadRequest, description: "QUERY_ID_INVALID"}
	})
	now := time.Now()
	challenge := &db.Challenge{
		CommChatID:         2006,
		UserID:             2006,
		ChatID:             -1006,
		Status:             db.ChallengeStatusApproveQueryPending,
		JoinRequestQueryID: "expired-query",
		CreatedAt:          now,
		ExpiresAt:          now.Add(time.Minute),
		NextAttemptAt:      sql.NullTime{Time: now, Valid: true},
	}
	if _, err := client.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI}, settings: webAppSettings()},
		store:      client,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}
	_ = gatekeeper.processChallengeAction(t.Context(), challenge)
	stored, err := client.GetChallengeByChatUser(t.Context(), challenge.ChatID, challenge.UserID)
	if err != nil {
		t.Fatalf("load uncertain query state: %v", err)
	}
	if stored == nil || stored.Status != db.ChallengeStatusApproveQueryPending || stored.AttemptCount != 1 || !stored.NextAttemptAt.Valid {
		t.Fatalf("invalid query was falsely recorded as approval: %#v", stored)
	}
}

func TestTerminalMemberApprovalErrorCompletesLeasedAction(t *testing.T) {
	t.Parallel()

	client, err := dbsqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		switch method {
		case "approveChatJoinRequest":
			return &testBotAPIError{code: http.StatusBadRequest, description: "USER_ALREADY_PARTICIPANT"}
		default:
			return true
		}
	})
	now := time.Now()
	challenge := &db.Challenge{
		CommChatID:    2007,
		UserID:        2007,
		ChatID:        -1007,
		Status:        db.ChallengeStatusApproveMemberPending,
		CreatedAt:     now,
		ExpiresAt:     now.Add(time.Minute),
		NextAttemptAt: sql.NullTime{Time: now, Valid: true},
	}
	if _, err := client.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI}, settings: webAppSettings()},
		store:      client,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}
	if err := gatekeeper.processChallengeAction(t.Context(), challenge); err != nil {
		t.Fatalf("process terminal approval: %v", err)
	}
	stored, err := client.GetChallengeByChatUser(t.Context(), challenge.ChatID, challenge.UserID)
	if err != nil {
		t.Fatalf("load completed approval: %v", err)
	}
	if stored == nil || stored.Status != db.ChallengeStatusPassedWaitingMemberJoin || stored.AttemptCount != 0 || stored.NextAttemptAt.Valid {
		t.Fatalf("terminal approval was not completed idempotently: %#v", stored)
	}
}

func TestJoinQueryRespondsBeforeSlowAllowlistLookup(t *testing.T) {
	t.Parallel()

	client, err := dbsqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	store := &blockingNotSpammerStore{
		gatekeeperStore: client,
		entered:         make(chan struct{}),
		release:         make(chan struct{}),
	}
	responded := make(chan struct{}, 1)
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method == testTelegramMethodJoinRequestQuery {
			responded <- struct{}{}
			return true
		}
		return true
	})
	settings := webAppSettings()
	settings.GatekeeperCaptchaEnabled = false
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI}, settings: settings},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}
	request := &api.ChatJoinRequest{
		Chat:       api.Chat{ID: -1005, Title: testGroupTitle, Type: testChatTypeSupergroup},
		From:       api.User{ID: 2005, FirstName: testFirstNameUser},
		UserChatID: 2005,
		QueryID:    "deadline-query",
	}
	done := make(chan error, 1)
	go func() {
		done <- gatekeeper.handleChatJoinRequest(t.Context(), &api.Update{ChatJoinRequest: request}, settings)
	}()
	select {
	case <-responded:
	case <-store.entered:
		select {
		case <-responded:
		default:
			close(store.release)
			<-done
			t.Fatal("slow allowlist lookup ran before the join-query response")
		}
	case <-time.After(time.Second):
		close(store.release)
		<-done
		t.Fatal("join-query response missed bounded fast path")
	}
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("slow moderation work did not continue after query response")
	}
	close(store.release)
	if err := <-done; err != nil {
		t.Fatalf("handle join request: %v", err)
	}
}
