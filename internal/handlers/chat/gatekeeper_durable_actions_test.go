package handlers

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
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

type failingBindStore struct {
	gatekeeperStore
}

type deadlineCapturingClient struct {
	base     api.HTTPClient
	method   string
	observed chan time.Duration
}

type contextTimeoutClient struct{}

func (contextTimeoutClient) Do(request *http.Request) (*http.Response, error) {
	<-request.Context().Done()
	return nil, request.Context().Err()
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
	records, err := client.GetChallengeReconciliations(t.Context())
	if err != nil || len(records) != 1 || records[0].ActionPhase != db.ChallengePhaseNoticeMessageStarted {
		t.Fatalf("ambiguous notice failure was not retained for reconciliation: records=%#v err=%v", records, err)
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

func TestJoinQueryResponseTimeoutIsDurablyActionable(t *testing.T) {
	t.Parallel()

	client, err := dbsqlite.NewSQLiteClient(t.Context(), t.TempDir(), "test.db")
	if err != nil {
		t.Fatalf("new sqlite client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	botAPI := newTestBotAPI(t, func(string, *http.Request) any { return true })
	botAPI.Client = contextTimeoutClient{}
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
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_ = gatekeeper.handleChatJoinRequest(ctx, &api.Update{ChatJoinRequest: request}, settings)
	records, err := client.GetChallengeReconciliations(t.Context())
	if err != nil || len(records) != 1 || records[0].ActionStatus != db.ChallengeStatusBanCheckPending {
		t.Fatalf("timed-out first response is not operator-actionable: records=%#v err=%v", records, err)
	}
	if !records[0].JoinRequestQueryPresent {
		t.Fatal("reconciliation lost redacted query-presence metadata")
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
