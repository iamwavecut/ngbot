package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	dbsqlite "github.com/iamwavecut/ngbot/internal/db/sqlite"
	log "github.com/sirupsen/logrus"
)

type blockingNotSpammerStore struct {
	gatekeeperStore
	entered     chan struct{}
	enteredOnce sync.Once
	release     chan struct{}
}

type failingBindStore struct {
	gatekeeperStore
}

type deadlineCapturingClient struct {
	base     api.HTTPClient
	method   string
	observed chan time.Duration
}

type transportErrorClient struct {
	err error
}

func (c transportErrorClient) Do(*http.Request) (*http.Response, error) {
	return nil, c.err
}

type methodErrorClient struct {
	base   api.HTTPClient
	method string
	err    error
}

func (c methodErrorClient) Do(request *http.Request) (*http.Response, error) {
	if strings.HasSuffix(request.URL.Path, "/"+c.method) {
		return nil, c.err
	}
	return c.base.Do(request)
}

func (c *deadlineCapturingClient) Do(request *http.Request) (*http.Response, error) {
	if strings.HasSuffix(request.URL.Path, "/"+c.method) {
		deadline, ok := request.Context().Deadline()
		if !ok {
			return nil, errors.New("first response has no child deadline")
		}
		c.observed <- time.Until(deadline)
	}
	return c.base.Do(request)
}

func (s *failingBindStore) BindLeasedChallengeMessage(context.Context, string, string, int64, string, string, string, int, time.Time) (bool, error) {
	return false, errors.New("forced bind failure")
}

type blockingBanChecker struct {
	testGatekeeperBanChecker
	entered chan struct{}
	release chan struct{}
}

func (b *blockingBanChecker) CheckBan(ctx context.Context, _ int64) (bool, error) {
	close(b.entered)
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-b.release:
		return true, nil
	}
}

func (s *blockingNotSpammerStore) IsChatNotSpammer(ctx context.Context, _ int64, _ int64, _ string) (bool, error) {
	s.enteredOnce.Do(func() { close(s.entered) })
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
		Variants:   map[string]map[string]string{"en": {"A": "apple", "B": "paper", "C": "vehicle"}},
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
	secret := "https://api.telegram.org/bot123456:SECRET/sendMessage body=query-secret web-secret"
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method == testTelegramMethodSendMessage {
			return &testBotAPIError{code: http.StatusBadGateway, description: secret}
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
	var logOutput bytes.Buffer
	logger := log.New()
	logger.SetOutput(&logOutput)
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      client,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{moderationUnavailable: true},
		logger:     log.NewEntry(logger),
	}
	actionErr := gatekeeper.processChallengeAction(t.Context(), challenge)
	if actionErr == nil || strings.Contains(actionErr.Error(), "SECRET") {
		t.Fatalf("unsafe returned action error: %v", actionErr)
	}
	records, err := client.GetChallengeReconciliations(t.Context())
	if err != nil || len(records) != 1 || records[0].ActionPhase != db.ChallengePhaseNoticeMessageStarted {
		t.Fatalf("ambiguous notice failure was not retained for reconciliation: records=%#v err=%v", records, err)
	}
	if strings.Contains(logOutput.String(), "SECRET") || strings.Contains(logOutput.String(), "api.telegram.org") || strings.Contains(records[0].LastError, "SECRET") {
		t.Fatalf("secret-bearing error leaked: log=%q record=%#v", logOutput.String(), records[0])
	}
	active, err := client.GetChallengeByChatUser(t.Context(), challenge.ChatID, challenge.UserID)
	if err != nil || active == nil || active.Status != db.ChallengeStatusNoPrivilegesNotice || active.NoticeMessageID != 0 {
		t.Fatalf("no-rights lifecycle was not retained: challenge=%#v err=%v", active, err)
	}
	if remaining := time.Until(active.ExpiresAt); remaining < 29*time.Minute || remaining > 31*time.Minute {
		t.Fatalf("no-rights retention=%s", remaining)
	}
	if expired, err := client.GetExpiredChallenges(t.Context(), active.ExpiresAt.Add(-time.Second)); err != nil || len(expired) != 0 {
		t.Fatalf("notice expired before 30m: %#v err=%v", expired, err)
	}
	if expired, err := client.GetExpiredChallenges(t.Context(), active.ExpiresAt.Add(time.Second)); err != nil || len(expired) != 1 {
		t.Fatalf("notice did not expire after 30m: %#v err=%v", expired, err)
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
	records, err := client.GetChallengeReconciliations(t.Context())
	if err != nil || len(records) != 1 || records[0].ActionPhase != db.ChallengePhaseQueryAnswerStarted {
		t.Fatalf("invalid query was not retained for reconciliation: records=%#v err=%v", records, err)
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
	deadlineObserved := make(chan time.Duration, 1)
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method == testTelegramMethodJoinRequestQuery {
			responded <- struct{}{}
			return true
		}
		return true
	})
	botAPI.Client = &deadlineCapturingClient{base: botAPI.Client, method: testTelegramMethodJoinRequestQuery, observed: deadlineObserved}
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
	if remaining := <-deadlineObserved; remaining <= 0 || remaining > joinQueryResponseTimeout {
		t.Fatalf("queue response deadline = %s, want within %s", remaining, joinQueryResponseTimeout)
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

func TestLegacyQueuedJoinRequestTransitionsProtectedBoundaryToDMCaptcha(t *testing.T) {
	t.Parallel()
	client, err := dbsqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	var sends atomic.Int32
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		switch method {
		case testTelegramMethodJoinRequestQuery:
			return true
		case testTelegramMethodGetChat:
			return map[string]any{"id": 7001, testJSONType: telegramChatTypePrivate, testJSONFirstName: "N"}
		case testTelegramMethodSendMessage:
			sends.Add(1)
			return map[string]any{logFieldMessageID: 801}
		default:
			return true
		}
	})
	settings := webAppSettings()
	gatekeeper := &Gatekeeper{bot: botAPI, s: &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI}, settings: settings}, store: client, config: &config.Config{}, banChecker: &testGatekeeperBanChecker{}, Variants: map[string]map[string]string{"en": {"A": "apple", "B": testCaptchaBook, "C": testCaptchaCar}}}
	request := &api.ChatJoinRequest{Chat: api.Chat{ID: -7001, Title: testGroupTitle}, From: api.User{ID: 7001, FirstName: "N", LanguageCode: "en"}, UserChatID: 7001, QueryID: "legacy-query"}
	if err := gatekeeper.handleChatJoinRequest(t.Context(), &api.Update{ChatJoinRequest: request}, settings); err != nil {
		t.Fatal(err)
	}
	stored, err := client.GetChallengeByChatUser(t.Context(), request.Chat.ID, request.From.ID)
	if err != nil || stored == nil || stored.Status != db.ChallengeStatusPending || stored.ChallengeMessageID != 801 || sends.Load() != 1 {
		t.Fatalf("protected queue boundary did not become DM CAPTCHA: challenge=%#v sends=%d err=%v", stored, sends.Load(), err)
	}
}

func TestBanCheckPendingRetriesAcrossRestartAndExhaustsSafely(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	client, err := dbsqlite.NewSQLiteClient(t.Context(), dataDir, "test.db")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	challenge := &db.Challenge{CommChatID: 7101, UserID: 7101, ChatID: -7101, Status: db.ChallengeStatusBanCheckPending, CreatedAt: now, ExpiresAt: now.Add(time.Minute), NextAttemptAt: sql.NullTime{Time: now, Valid: true}}
	if _, err := client.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatal(err)
	}
	checker := &testGatekeeperBanChecker{checkErr: errors.New("transient provider")}
	gatekeeper := &Gatekeeper{store: client, config: &config.Config{}, banChecker: checker}
	_ = gatekeeper.processChallengeAction(t.Context(), challenge)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	client, err = dbsqlite.NewSQLiteClient(t.Context(), dataDir, "test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	due, err := client.GetDueChallenges(t.Context(), time.Now().Add(time.Minute))
	if err != nil || len(due) != 1 || due[0].Status != db.ChallengeStatusBanCheckPending {
		t.Fatalf("ban check not restart-due: %#v err=%v", due, err)
	}
	gatekeeper.store = client
	for i := due[0].AttemptCount; i < maxChallengeActionAttempts-1; i++ {
		if scheduled, err := client.ScheduleChallengeRetry(t.Context(), challenge.ChallengeID, db.ChallengeStatusBanCheckPending, time.Now(), db.GatekeeperErrorUnavailable); err != nil || !scheduled {
			t.Fatalf("seed prior retry %d: scheduled=%t err=%v", i, scheduled, err)
		}
	}
	due, _ = client.GetDueChallenges(t.Context(), time.Now().Add(time.Second))
	if len(due) != 1 {
		t.Fatalf("exhaustion action not due: %#v", due)
	}
	_ = gatekeeper.processChallengeAction(t.Context(), due[0])
	records, err := client.GetChallengeReconciliations(t.Context())
	if err != nil || len(records) != 1 || records[0].ActionStatus != db.ChallengeStatusBanCheckPending {
		t.Fatalf("ban check exhaustion not reconciled: %#v err=%v", records, err)
	}
	replacement := &db.Challenge{CommChatID: challenge.CommChatID, UserID: challenge.UserID, ChatID: challenge.ChatID, Status: db.ChallengeStatusPending, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	if _, err := client.CreateChallenge(t.Context(), replacement); err != nil {
		t.Fatalf("rejoin blocked after exhaustion: %v", err)
	}
}

func TestDurableChallengeCapabilityLookupFailureRetriesWithoutExternalEffect(t *testing.T) {
	t.Parallel()

	client, err := dbsqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		t.Fatalf("capability outage reached Telegram method %s", method)
		return nil
	})
	now := time.Now()
	challenge := &db.Challenge{
		CommChatID:    7151,
		UserID:        7151,
		ChatID:        -7151,
		Status:        db.ChallengeStatusApproveMemberPending,
		CreatedAt:     now,
		ExpiresAt:     now.Add(time.Minute),
		NextAttemptAt: sql.NullTime{Time: now, Valid: true},
	}
	if _, err := client.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatal(err)
	}
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI}, settings: webAppSettings()},
		store:      client,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{moderationErr: errors.New("capability lookup unavailable")},
	}

	err = gatekeeper.processChallengeAction(t.Context(), challenge)
	failure := bot.ClassifyUpdateFailure(err)
	if failure.Source != bot.UpdateFailureCapability || failure.Disposition != bot.UpdateFailureRetryable {
		t.Fatalf("capability failure = %#v", failure)
	}
	stored, loadErr := client.GetChallengeByChatUser(t.Context(), challenge.ChatID, challenge.UserID)
	if loadErr != nil || stored == nil || stored.Status != db.ChallengeStatusApproveMemberPending || !stored.NextAttemptAt.Valid || stored.AttemptCount != 1 {
		t.Fatalf("capability outage was not retryable: challenge=%#v err=%v", stored, loadErr)
	}
}

func TestStaleChallengeRestrictionUsesBoundedTemporaryDeadline(t *testing.T) {
	t.Parallel()

	fixedNow := time.Now().UTC().Truncate(time.Second).Add(time.Minute)
	wantUntil := fixedNow.Add(time.Minute).Unix()
	botAPI := newTestBotAPI(t, func(method string, request *http.Request) any {
		if method == testTelegramMethodRestrictChatMember {
			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if got := request.Form.Get("until_date"); got != strconv.FormatInt(wantUntil, 10) {
				t.Fatalf("serialized until_date = %q, want %d", got, wantUntil)
			}
			return true
		}
		if method == testTelegramMethodSendMessage {
			return map[string]any{logFieldMessageID: 901}
		}
		t.Fatalf("unexpected Telegram method %s", method)
		return nil
	})
	_, err := botAPI.RequestWithContext(t.Context(), api.RestrictChatMemberConfig{
		ChatMemberConfig: api.ChatMemberConfig{ChatConfig: api.ChatConfig{ChatID: -7161}, UserID: 7161},
		UntilDate:        temporaryRestrictionDeadline(fixedNow, fixedNow.Add(-time.Minute), time.Minute).Unix(),
		Permissions:      &api.ChatPermissions{},
	})
	if err != nil {
		t.Fatalf("serialize stale restriction: %v", err)
	}
}

func TestBanCheckReplayStopsBeforeProviderForNewAllowlistOrNoRights(t *testing.T) {
	for _, test := range []struct {
		name      string
		allowlist bool
		noRights  bool
	}{
		{name: "allowlist added after persistence", allowlist: true},
		{name: "confirmed no rights", noRights: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := dbsqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			now := time.Now()
			challenge := &db.Challenge{CommChatID: 7171, UserID: 7171, Username: "fresh_allow", ChatID: -7171, Status: db.ChallengeStatusBanCheckPending, CreatedAt: now, ExpiresAt: now.Add(time.Minute), NextAttemptAt: sql.NullTime{Time: now, Valid: true}}
			if _, err := client.CreateChallenge(t.Context(), challenge); err != nil {
				t.Fatal(err)
			}
			if test.allowlist {
				if _, err := client.CreateChatNotSpammerOverride(t.Context(), &db.ChatNotSpammerOverride{ChatID: challenge.ChatID, MatchType: db.NotSpammerMatchTypeUsername, MatchValue: challenge.Username, CreatedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			checker := &testGatekeeperBanChecker{banned: true, moderationUnavailable: test.noRights}
			gatekeeper := &Gatekeeper{store: client, config: &config.Config{}, banChecker: checker}
			if err := gatekeeper.processChallengeAction(t.Context(), challenge); err != nil {
				t.Fatalf("process replay: %v", err)
			}
			if checker.checkBanCalls != 0 || len(checker.bans) != 0 {
				t.Fatalf("replay reached provider or Telegram: checks=%d bans=%#v", checker.checkBanCalls, checker.bans)
			}
			if stored, err := client.GetChallengeByChatUser(t.Context(), challenge.ChatID, challenge.UserID); err != nil || stored != nil {
				t.Fatalf("safe replay recovery retained challenge: %#v err=%v", stored, err)
			}
		})
	}
}

func TestLegacyJoinTransportLogsNeverExposeSecretURL(t *testing.T) {
	t.Parallel()
	secret := "https://api.telegram.org/bot123456:SECRET/sendMessage?body=query-secret"
	for _, test := range []struct {
		name string
		run  func(*Gatekeeper, *db.Settings) error
	}{
		{name: "private chat probe", run: func(g *Gatekeeper, settings *db.Settings) error {
			request := &api.ChatJoinRequest{Chat: api.Chat{ID: -7201}, From: api.User{ID: 7201}, UserChatID: 7201}
			return g.handleChatJoinRequest(t.Context(), &api.Update{ChatJoinRequest: request}, settings)
		}},
		{name: "challenge send", run: func(g *Gatekeeper, settings *db.Settings) error {
			return g.startChallenge(t.Context(), nil, &api.User{ID: 7202, FirstName: "N"}, &api.Chat{ID: -7202}, -7202, -7202, settings)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newGatekeeperFlowStore()
			botAPI := newTestBotAPI(t, func(string, *http.Request) any { return true })
			botAPI.Client = transportErrorClient{err: &url.Error{Op: http.MethodPost, URL: secret, Err: errors.New("secret response body")}}
			settings := webAppSettings()
			var output bytes.Buffer
			logger := log.New()
			logger.SetOutput(&output)
			gatekeeper := &Gatekeeper{bot: botAPI, s: &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI}, settings: settings}, store: store, config: &config.Config{}, banChecker: &testGatekeeperBanChecker{moderationUnavailable: true}, logger: log.NewEntry(logger), Variants: map[string]map[string]string{"en": {"A": "apple", "B": testCaptchaBook, "C": testCaptchaCar}}}
			_ = test.run(gatekeeper, settings)
			if strings.Contains(output.String(), "SECRET") || strings.Contains(output.String(), "api.telegram.org") || strings.Contains(output.String(), "query-secret") {
				t.Fatalf("transport secret leaked in log: %s", output.String())
			}
		})
	}
}

func TestJoinQueryResponseTimeoutIsDurablyActionable(t *testing.T) {
	t.Parallel()

	client, err := dbsqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	botAPI := newTestBotAPI(t, func(string, *http.Request) any { return true })
	botAPI.Client = methodErrorClient{base: botAPI.Client, method: testTelegramMethodJoinRequestQuery, err: context.DeadlineExceeded}
	settings := webAppSettings()
	settings.GatekeeperCaptchaEnabled = false
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI}, settings: settings},
		store:      client,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}
	request := &api.ChatJoinRequest{
		Chat:       api.Chat{ID: -1006, Title: testGroupTitle, Type: testChatTypeSupergroup},
		From:       api.User{ID: 2006, FirstName: testFirstNameUser},
		UserChatID: 2006,
		QueryID:    "timeout-query",
	}
	_ = gatekeeper.handleChatJoinRequest(t.Context(), &api.Update{ChatJoinRequest: request}, settings)
	records, err := client.GetChallengeReconciliations(t.Context())
	if err != nil || len(records) != 1 || records[0].ActionStatus != db.ChallengeStatusBanCheckPending || records[0].ActionPhase != db.ChallengePhaseQueueResponseStarted {
		t.Fatalf("timed-out first response is not operator-actionable: records=%#v err=%v", records, err)
	}
	if !records[0].JoinRequestQueryPresent {
		t.Fatal("reconciliation lost redacted query-presence metadata")
	}
	if requeued, err := client.RequeueChallengeReconciliation(t.Context(), records[0].ID, records[0].Version, time.Now()); err == nil || requeued {
		t.Fatalf("ambiguous queue effect was requeued: requeued=%t err=%v", requeued, err)
	}
}

func TestAmbiguousWebAppResponseWaitsForBanCheckAndNeverFallsBack(t *testing.T) {
	client, err := dbsqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	checker := &blockingBanChecker{entered: make(chan struct{}), release: make(chan struct{})}
	var fallbackCalls atomic.Int32
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		if method == testTelegramMethodSendMessage {
			fallbackCalls.Add(1)
		}
		return true
	})
	botAPI.Client = methodErrorClient{base: botAPI.Client, method: testTelegramMethodSendJoinWebApp, err: context.DeadlineExceeded}
	settings := webAppSettings()
	gatekeeper := &Gatekeeper{bot: botAPI, s: &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI}, settings: settings}, store: client, config: &config.Config{GatekeeperWebApp: config.GatekeeperWebApp{PublicURL: testWebAppURL}}, banChecker: checker}
	request := &api.ChatJoinRequest{Chat: api.Chat{ID: -2020}, From: api.User{ID: 3020}, UserChatID: 3020, QueryID: "query-secret"}
	done := make(chan error, 1)
	go func() {
		done <- gatekeeper.handleChatJoinRequest(t.Context(), &api.Update{ChatJoinRequest: request}, settings)
	}()
	select {
	case <-checker.entered:
	case <-time.After(time.Second):
		t.Fatal("provider ban check did not run after ambiguous WebApp response")
	}
	if fallbackCalls.Load() != 0 {
		t.Fatalf("DM fallback ran before provider check: %d", fallbackCalls.Load())
	}
	records, err := client.GetChallengeReconciliations(t.Context())
	if err != nil || len(records) != 1 || records[0].ActionPhase != db.ChallengePhaseWebAppResponseStarted {
		t.Fatalf("ambiguous WebApp response not reconciled: %#v err=%v", records, err)
	}
	close(checker.release)
	if err := <-done; err != nil {
		t.Fatalf("join handler: %v", err)
	}
}

func TestAcceptedChallengeMessageBindFailureMovesToReconciliation(t *testing.T) {
	t.Parallel()

	client, err := dbsqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	store := &failingBindStore{gatekeeperStore: client}
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		switch method {
		case testTelegramMethodRestrictChatMember:
			return true
		case testTelegramMethodSendMessage:
			return map[string]any{logFieldMessageID: 501}
		default:
			return true
		}
	})
	settings := webAppSettings()
	settings.ID = -2001
	gatekeeper := &Gatekeeper{
		bot:   botAPI,
		s:     &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: settings},
		store: store, config: &config.Config{}, banChecker: &testGatekeeperBanChecker{},
		Variants: map[string]map[string]string{"en": {"A": "apple", "B": "paper", "C": "vehicle"}},
	}
	chat := &api.Chat{ID: settings.ID, Title: testGroupTitle, Type: testChatTypeSupergroup}
	user := &api.User{ID: 3001, FirstName: testFirstNameUser}
	if err := gatekeeper.startChallenge(t.Context(), nil, user, chat, chat.ID, chat.ID, settings); err == nil {
		t.Fatal("expected bind failure")
	}
	records, err := client.GetChallengeReconciliations(t.Context())
	if err != nil || len(records) != 1 || records[0].ArtifactMessageID != 501 || records[0].ActionPhase != db.ChallengePhasePublicMessageStarted {
		t.Fatalf("accepted unbound send was not reconciled: records=%#v err=%v", records, err)
	}
}

func TestWebAppCannotApproveWhileProviderBanCheckIsBlocked(t *testing.T) {
	t.Parallel()

	client, err := dbsqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	checker := &blockingBanChecker{entered: make(chan struct{}), release: make(chan struct{})}
	deadlineObserved := make(chan time.Duration, 1)
	botAPI := newTestBotAPI(t, func(string, *http.Request) any { return true })
	botAPI.Client = &deadlineCapturingClient{base: botAPI.Client, method: testTelegramMethodSendJoinWebApp, observed: deadlineObserved}
	settings := webAppSettings()
	gatekeeper := &Gatekeeper{
		bot:   botAPI,
		s:     &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI}, settings: settings},
		store: client, config: &config.Config{GatekeeperWebApp: config.GatekeeperWebApp{PublicURL: testWebAppURL}}, banChecker: checker,
	}
	request := &api.ChatJoinRequest{Chat: api.Chat{ID: -2002}, From: api.User{ID: 3002}, UserChatID: 3002, QueryID: "query"}
	done := make(chan error, 1)
	go func() {
		done <- gatekeeper.handleChatJoinRequest(t.Context(), &api.Update{ChatJoinRequest: request}, settings)
	}()
	select {
	case <-checker.entered:
	case <-time.After(time.Second):
		t.Fatal("provider ban check did not block")
	}
	if remaining := <-deadlineObserved; remaining <= 0 || remaining > joinQueryResponseTimeout {
		t.Fatalf("WebApp response deadline = %s, want within %s", remaining, joinQueryResponseTimeout)
	}
	challenge, err := client.GetChallengeByChatUser(t.Context(), request.Chat.ID, request.From.ID)
	if err != nil || challenge == nil || challenge.Status != db.ChallengeStatusBanCheckPending {
		t.Fatalf("missing durable pre-approval guard: challenge=%#v err=%v", challenge, err)
	}
	if claimed, err := client.ClaimForApproval(t.Context(), challenge.ChallengeID); err != nil || claimed {
		t.Fatalf("WebApp approval raced ban check: claimed=%t err=%v", claimed, err)
	}
	close(checker.release)
	if err := <-done; err != nil {
		t.Fatalf("complete banned request: %v", err)
	}
	if active, err := client.GetChallengeByChatUser(t.Context(), request.Chat.ID, request.From.ID); err != nil || active != nil {
		t.Fatalf("banned request retained active challenge: challenge=%#v err=%v", active, err)
	}
}

func TestPendingRequesterMissingDuringBanIsNotTreatedAsBanned(t *testing.T) {
	t.Parallel()

	client, err := dbsqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	var declines atomic.Int32
	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		switch method {
		case testTelegramMethodGetChatMember:
			return map[string]any{"status": "left", "user": map[string]any{"id": 3003, "is_bot": false, "first_name": "N"}}
		case testTelegramMethodBanChatMember:
			return &testBotAPIError{code: http.StatusBadRequest, description: "USER_NOT_PARTICIPANT"}
		case testTelegramMethodDeclineJoinRequest:
			declines.Add(1)
			return true
		default:
			return true
		}
	})
	now := time.Now()
	challenge := &db.Challenge{CommChatID: 3003, UserID: 3003, ChatID: -2003, Status: db.ChallengeStatusRejectPending, CreatedAt: now, ExpiresAt: now.Add(time.Minute), NextAttemptAt: sql.NullTime{Time: now, Valid: true}}
	if _, err := client.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatal(err)
	}
	gatekeeper := &Gatekeeper{bot: botAPI, s: &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI}, settings: webAppSettings()}, store: client, config: &config.Config{}, banChecker: &testGatekeeperBanChecker{}}
	_ = gatekeeper.processChallengeAction(t.Context(), challenge)
	if declines.Load() != 0 {
		t.Fatalf("declined after unproven ban: %d", declines.Load())
	}
	records, err := client.GetChallengeReconciliations(t.Context())
	if err != nil || len(records) != 1 || records[0].ActionPhase != db.ChallengePhaseRejectBanStarted {
		t.Fatalf("unproven ban was not reconciled: records=%#v err=%v", records, err)
	}
}

func TestKnownBannedCleanupCannotDeleteBlockedLeasedActions(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		status string
		method string
		commID int64
	}{
		{name: "approval", status: db.ChallengeStatusApproveQueryPending, method: testTelegramMethodJoinRequestQuery, commID: 4001},
		{name: "fallback", status: db.ChallengeStatusWebAppFallbackPending, method: testTelegramMethodSendMessage, commID: 4002},
		{name: "reject", status: db.ChallengeStatusRejectPending, method: testTelegramMethodBanChatMember, commID: 4003},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := dbsqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			entered := make(chan struct{})
			release := make(chan struct{})
			botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
				if method == test.method {
					close(entered)
					<-release
					if method == testTelegramMethodSendMessage {
						return map[string]any{logFieldMessageID: 901}
					}
					return true
				}
				switch method {
				case testTelegramMethodGetChat:
					return map[string]any{"id": test.commID, testJSONType: telegramChatTypePrivate, testJSONFirstName: "N"}
				case testTelegramMethodGetChatMember:
					return map[string]any{"status": "left", "user": map[string]any{"id": test.commID, "is_bot": false, "first_name": "N"}}
				case testTelegramMethodDeclineJoinRequest, testTelegramMethodDeleteMessage:
					return true
				default:
					return true
				}
			})
			now := time.Now()
			challenge := &db.Challenge{CommChatID: test.commID, UserID: test.commID, ChatID: -test.commID, Status: test.status, CreatedAt: now, ExpiresAt: now.Add(time.Minute), NextAttemptAt: sql.NullTime{Time: now, Valid: true}}
			if test.status == db.ChallengeStatusApproveQueryPending {
				challenge.JoinRequestQueryID = "query"
			}
			if test.status == db.ChallengeStatusWebAppFallbackPending {
				challenge.WebAppToken = "token"
				challenge.JoinRequestQueryID = "query"
			}
			if _, err := client.CreateChallenge(t.Context(), challenge); err != nil {
				t.Fatal(err)
			}
			gatekeeper := &Gatekeeper{bot: botAPI, s: &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI}, settings: webAppSettings()}, store: client, config: &config.Config{}, banChecker: &testGatekeeperBanChecker{}}
			done := make(chan error, 1)
			go func() { done <- gatekeeper.processChallengeAction(t.Context(), challenge) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("action did not block")
			}
			gatekeeper.cleanupKnownBannedArtifacts(t.Context(), challenge.ChatID, challenge.UserID, 0)
			stored, err := client.GetChallengeByChatUser(t.Context(), challenge.ChatID, challenge.UserID)
			if err != nil || stored == nil || stored.ActionOwner == "" || !strings.HasSuffix(stored.ActionPhase, "started") {
				t.Fatalf("cleanup deleted leased audit state: challenge=%#v err=%v", stored, err)
			}
			close(release)
			<-done
			active, err := client.GetChallengeByChatUser(t.Context(), challenge.ChatID, challenge.UserID)
			if err != nil {
				t.Fatalf("load post-cancellation state: %v", err)
			}
			if active != nil && active.Status == db.ChallengeStatusPassedWaitingMemberJoin {
				t.Fatalf("known-banned cancellation still produced approval: %#v", active)
			}
			records, err := client.GetChallengeReconciliations(t.Context())
			if err != nil || len(records) != 1 {
				t.Fatalf("canceled in-flight effect lacks audit record: records=%#v err=%v", records, err)
			}
		})
	}
}
