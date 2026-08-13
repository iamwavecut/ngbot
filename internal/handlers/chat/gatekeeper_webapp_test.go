package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
	log "github.com/sirupsen/logrus"
)

func TestJoinCaptchaHealthReflectsReadiness(t *testing.T) {
	t.Parallel()

	gatekeeper := &Gatekeeper{config: &config.Config{GatekeeperWebApp: config.GatekeeperWebApp{
		MaxConcurrent:     2,
		RequestsPerMinute: 10,
	}}}
	handler := gatekeeper.joinCaptchaWebAppHandler()

	live := httptest.NewRecorder()
	handler.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if live.Code != http.StatusOK {
		t.Fatalf("liveness status = %d", live.Code)
	}

	notReady := httptest.NewRecorder()
	handler.ServeHTTP(notReady, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if notReady.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial readiness status = %d, want 503", notReady.Code)
	}

	gatekeeper.webAppReady.Store(true)
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("ready status = %d", ready.Code)
	}
}

func TestJoinCaptchaRateLimitUsesTrustedForwardedClient(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	limiter := newJoinCaptchaRateLimiter(1, time.Minute, func() time.Time { return now })
	handler := joinCaptchaRateLimitMiddleware(limiter, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	request := func(forwardedFor string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, joinCaptchaPath, nil)
		req.RemoteAddr = "127.0.0.1:42000"
		req.Header.Set("X-Forwarded-For", forwardedFor)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	if got := request("203.0.113.10").Code; got != http.StatusNoContent {
		t.Fatalf("first request status = %d", got)
	}
	if got := request("203.0.113.10").Code; got != http.StatusTooManyRequests {
		t.Fatalf("repeated client status = %d, want 429", got)
	}
	if got := request("203.0.113.11").Code; got != http.StatusNoContent {
		t.Fatalf("distinct client status = %d", got)
	}
}

func TestJoinCaptchaRateLimiterEvictsOldestClientAtCapacity(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	limiter := newJoinCaptchaRateLimiter(2, time.Hour, func() time.Time { return now })
	for index := range joinCaptchaRateClientLimit {
		client := fmt.Sprintf("client-%04d", index)
		if !limiter.allow(client) {
			t.Fatalf("client %d was unexpectedly rejected", index)
		}
		now = now.Add(time.Millisecond)
	}
	if len(limiter.clients) != joinCaptchaRateClientLimit {
		t.Fatalf("client state size = %d, want %d", len(limiter.clients), joinCaptchaRateClientLimit)
	}

	if !limiter.allow("new-legitimate-client") {
		t.Fatal("4097th distinct client was globally rate limited")
	}
	if len(limiter.clients) != joinCaptchaRateClientLimit {
		t.Fatalf("client state grew to %d, want bounded %d", len(limiter.clients), joinCaptchaRateClientLimit)
	}
	if _, exists := limiter.clients["client-0000"]; exists {
		t.Fatal("oldest client state was not evicted")
	}
}

func TestJoinCaptchaRateLimiterDropsExpiredClientStateAtCapacity(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	limiter := newJoinCaptchaRateLimiter(1, time.Minute, func() time.Time { return now })
	for index := range joinCaptchaRateClientLimit {
		if !limiter.allow(fmt.Sprintf("expired-client-%04d", index)) {
			t.Fatalf("client %d was unexpectedly rejected", index)
		}
	}
	now = now.Add(time.Minute)
	if !limiter.allow("new-client") {
		t.Fatal("new client was rejected after prior state expired")
	}
	if len(limiter.clients) != 1 {
		t.Fatalf("expired client state was retained: size=%d, want 1", len(limiter.clients))
	}
}

func TestJoinCaptchaAdmissionRejectsOverflow(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	release := make(chan struct{})
	handler := joinCaptchaAdmissionMiddleware(1, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, joinCaptchaPath, nil))
	}()
	<-entered

	overflow := httptest.NewRecorder()
	handler.ServeHTTP(overflow, httptest.NewRequest(http.MethodGet, joinCaptchaPath, nil))
	if overflow.Code != http.StatusServiceUnavailable {
		t.Fatalf("overflow status = %d, want 503", overflow.Code)
	}
	close(release)
	<-firstDone
}

func TestJoinCaptchaTelemetryDoesNotLogBearerOrToken(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := log.New()
	logger.SetOutput(&output)
	logger.SetFormatter(&log.JSONFormatter{})
	handler := joinCaptchaTelemetryMiddleware(log.NewEntry(logger), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/arbitrary-webapp-bearer-secret?token=webapp-bearer-secret", nil)
	req.Header.Set("Authorization", "Bearer authorization-secret")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	logged := output.String()
	for _, secret := range []string{"webapp-bearer-secret", "authorization-secret", "Authorization"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("telemetry leaked %q in %q", secret, logged)
		}
	}
	if strings.Contains(logged, "/arbitrary-webapp-bearer-secret") {
		t.Fatalf("telemetry logged an untrusted raw path: %q", logged)
	}
	if !strings.Contains(logged, `"http_route":"other"`) || !strings.Contains(logged, `"status":204`) {
		t.Fatalf("telemetry is missing safe request metadata: %q", logged)
	}
}

func TestWebAppServeFailureIsReportedAsFatal(t *testing.T) {
	t.Parallel()

	serveErr := stderrors.New("serve failed")
	reported := make(chan error, 1)
	gatekeeper := &Gatekeeper{
		config: &config.Config{GatekeeperWebApp: config.GatekeeperWebApp{
			ListenAddr:        "127.0.0.1:0",
			MaxConcurrent:     2,
			RequestsPerMinute: 10,
		}},
		serveWebApp: func(*http.Server, net.Listener) error { return serveErr },
		webAppFatalError: func(err error) {
			reported <- err
		},
	}
	if err := gatekeeper.startWebAppServer(t.Context()); err != nil {
		t.Fatalf("startWebAppServer: %v", err)
	}
	select {
	case err := <-reported:
		if !stderrors.Is(err, serveErr) {
			t.Fatalf("reported error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WebApp Serve failure was not reported")
	}
}

func TestJoinCaptchaRenderedClientPostsOneAnswerWithoutUncaughtException(t *testing.T) {
	t.Parallel()

	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required for the rendered WebApp client test")
	}

	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{store: store, config: &config.Config{}}
	req := httptest.NewRequest(http.MethodGet, joinCaptchaPath+"?token="+url.QueryEscape(challenge.WebAppToken), nil)
	rr := httptest.NewRecorder()
	gatekeeper.joinCaptchaWebAppHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("render challenge: status %d: %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	scriptStart := strings.LastIndex(body, "<script nonce=")
	if scriptStart < 0 {
		t.Fatal("rendered client script not found")
	}
	scriptStart += strings.Index(body[scriptStart:], ">") + 1
	scriptEnd := strings.Index(body[scriptStart:], "</script>")
	if scriptEnd < 0 {
		t.Fatal("rendered client script terminator not found")
	}
	clientScript := body[scriptStart : scriptStart+scriptEnd]

	harness := fmt.Sprintf(`
const vm = require("node:vm");
const source = Buffer.from(%q, "base64").toString("utf8");
let click;
let answerPosts = 0;
let uncaught = 0;
const makeClassList = () => ({ add() {}, remove() {} });
const makeElement = () => ({
  dataset: {}, disabled: false, textContent: "", classList: makeClassList(),
  addEventListener(type, handler) { if (type === "click") click = handler; },
  setAttribute() {}, focus() {}
});
const root = makeElement();
const title = makeElement();
const status = makeElement();
const countdown = makeElement();
const prompt = makeElement();
const button = makeElement();
button.dataset.choice = %q;
const document = {
  body: { dataset: { token: %q } },
  querySelector(selector) {
    return ({ "main": root, "[data-title]": title, "[data-status]": status,
      "[data-countdown]": countdown, "[data-prompt]": prompt })[selector] || null;
  },
  querySelectorAll(selector) { return selector === "[data-choice]" ? [button] : []; }
};
const app = { initData: "signed", ready() {}, expand() {}, close() {} };
const sandbox = {
  window: { Telegram: { WebApp: app }, clearTimeout() {}, setTimeout(fn) { fn(); },
    clearInterval() {}, setInterval() { return 1; } },
  document, URLSearchParams, Uint8Array, TextDecoder,
  setTimeout(fn) { fn(); },
  fetch: async (target, options) => {
    if (target.endsWith("/answer")) {
      if (!options || options.method !== "POST") throw new Error("answer request must use POST");
      answerPosts++;
      if (options.redirect !== "error") throw new Error("redirect mode was not error");
    }
    return { ok: true, json: async () => ({ ok: true, done: true, state: "passed", message: "done" }) };
  }
};
process.on("unhandledRejection", () => { uncaught++; });
process.on("uncaughtException", () => { uncaught++; });
(async () => {
  vm.runInNewContext(source, sandbox);
  await new Promise(resolve => setImmediate(resolve));
  answerPosts = 0;
  if (!click) throw new Error("choice click handler was not installed");
  click();
  await new Promise(resolve => setImmediate(resolve));
  await new Promise(resolve => setImmediate(resolve));
  process.stdout.write(JSON.stringify({ answerPosts, uncaught }));
})().catch(error => { process.stderr.write(error.stack); process.exitCode = 1; });
`, base64.StdEncoding.EncodeToString([]byte(clientScript)), challenge.SuccessUUID, challenge.WebAppToken)

	output, err := exec.CommandContext(t.Context(), node, "-e", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("execute rendered client: %v\n%s", err, output)
	}
	var result struct {
		AnswerPosts int `json:"answerPosts"`
		Uncaught    int `json:"uncaught"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode rendered client result %q: %v", output, err)
	}
	if result.AnswerPosts != 1 || result.Uncaught != 0 {
		t.Fatalf("rendered click result = %+v, want one answer POST and no uncaught exception", result)
	}
}

func TestJoinCaptchaAnswerApprovesMatchingTokenUserAndChoice(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		switch method {
		case testTelegramMethodJoinRequestQuery, testTelegramMethodBanChatMember:
			return true
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		testWebAppFormToken:    {challenge.WebAppToken},
		testWebAppFormChoice:   {challenge.SuccessUUID},
		testWebAppFormInitData: {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
	}
	req := httptest.NewRequest(http.MethodPost, "/gatekeeper/join-captcha/answer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	gatekeeper.handleJoinCaptchaAnswer(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["ok"] != true || body["done"] != true {
		t.Fatalf("unexpected response: %#v", body)
	}

	answers := recorder.byMethod(testTelegramMethodJoinRequestQuery)
	if len(answers) != 1 {
		t.Fatalf("expected one query answer, got %d", len(answers))
	}
	if answers[0].form.Get("chat_join_request_query_id") != challenge.JoinRequestQueryID {
		t.Fatalf("unexpected query id: %q", answers[0].form.Get("chat_join_request_query_id"))
	}
	if answers[0].form.Get("result") != "approve" {
		t.Fatalf("expected approve result, got %q", answers[0].form.Get("result"))
	}
	got := store.onlyChallenge(t)
	if got.Status != db.ChallengeStatusPassedWaitingMemberJoin {
		t.Fatalf("expected handoff status, got %q", got.Status)
	}
}

func TestHandleJoinCaptchaAnswerConflictsWhenAlreadyClaimed(t *testing.T) {
	t.Parallel()

	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	claimed, err := store.ClaimForApproval(t.Context(), challenge.ChallengeID)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !claimed {
		t.Fatal("expected first approval claim to win")
	}
	got := store.onlyChallenge(t)
	if got.Status != db.ChallengeStatusApproveQueryPending {
		t.Fatalf("expected status to become approval-pending after claim, got %q", got.Status)
	}

	claimed, err = store.ClaimForApproval(t.Context(), challenge.ChallengeID)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed {
		t.Fatal("expected second approval claim to lose once the row left pending")
	}

	fallback := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	fallback.CommChatID = 7002
	fallback.UserID = 99
	fallback.ChatID = -100777
	fallback.WebAppToken = "fallback-claimed-token"
	fallback.Status = db.ChallengeStatusWebAppFallbackPending
	if _, err := store.CreateChallenge(t.Context(), fallback); err != nil {
		t.Fatalf("create fallback-claimed challenge: %v", err)
	}

	claimed, err = store.ClaimForApproval(t.Context(), fallback.ChallengeID)
	if err != nil {
		t.Fatalf("claim against fallback-claimed row: %v", err)
	}
	if claimed {
		t.Fatal("expected approval claim to lose when a fallback already claimed the row")
	}
}

func TestHandleJoinCaptchaAnswerReportsProcessingAfterLostApprovalResponse(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		t.Fatalf("replayed answer must not repeat Telegram action: %s", method)
		return nil
	})
	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	challenge.Status = db.ChallengeStatusApproveQueryPending
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{bot: botAPI, store: store, config: &config.Config{}}
	form := url.Values{
		testWebAppFormToken:    {challenge.WebAppToken},
		testWebAppFormChoice:   {challenge.SuccessUUID},
		testWebAppFormInitData: {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
	}
	req := httptest.NewRequest(http.MethodPost, joinCaptchaAnswerPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	gatekeeper.joinCaptchaWebAppHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rr.Code, rr.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["done"] != true || response["state"] != "processing" {
		t.Fatalf("expected processing replay response, got %#v", response)
	}
}

func TestTestJoinCaptchaCommandSendsWebAppButton(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		switch method {
		case testTelegramMethodSendMessage:
			return recorder.nextSendMessageResult()
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})
	botAPI.Self.UserName = testBotUsername
	store := newGatekeeperFlowStore()
	gatekeeper := &Gatekeeper{
		bot:    botAPI,
		s:      &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:  store,
		config: &config.Config{GatekeeperWebApp: config.GatekeeperWebApp{PublicURL: testWebAppURL}},
	}

	user := &api.User{ID: 42, FirstName: testFirstNameNeo}
	chat := &api.Chat{ID: user.ID, Type: telegramChatTypePrivate}
	update := &api.Update{Message: commandMessage(chat, user, "/test_join_captcha")}

	proceed, err := gatekeeper.Handle(t.Context(), update, chat, user)
	if err != nil {
		t.Fatalf("handle command: %v", err)
	}
	if proceed {
		t.Fatalf("expected command to stop propagation")
	}

	messages := recorder.byMethod(testTelegramMethodSendMessage)
	if len(messages) != 1 {
		t.Fatalf("expected one message, got %d", len(messages))
	}
	replyMarkup := messages[0].form.Get("reply_markup")
	if !strings.Contains(replyMarkup, `"web_app"`) || !strings.Contains(replyMarkup, `https://guard.example/gatekeeper/join-captcha?token=`) {
		t.Fatalf("expected web app button, got %q", replyMarkup)
	}
	challenge := store.onlyChallenge(t)
	if !strings.HasPrefix(challenge.JoinRequestQueryID, joinCaptchaTestQueryPrefix) {
		t.Fatalf("expected test query id, got %q", challenge.JoinRequestQueryID)
	}
	if challenge.WebAppToken == "" || challenge.UserID != user.ID || challenge.ChatID != chat.ID {
		t.Fatalf("unexpected challenge: %#v", challenge)
	}
}

func TestJoinCaptchaURLBuildsFromValidatedOrigin(t *testing.T) {
	t.Parallel()

	gatekeeper := &Gatekeeper{config: &config.Config{GatekeeperWebApp: config.GatekeeperWebApp{PublicURL: "https://guard.example/"}}}
	got, err := gatekeeper.joinCaptchaURL("a token&next=https://evil.example")
	if err != nil {
		t.Fatalf("joinCaptchaURL: %v", err)
	}
	want := "https://guard.example/gatekeeper/join-captcha?token=a+token%26next%3Dhttps%3A%2F%2Fevil.example"
	if got != want {
		t.Fatalf("joinCaptchaURL = %q, want %q", got, want)
	}
}

func TestJoinCaptchaAnswerCompletesTestChallengeWithoutJoinQueryAnswer(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		t.Fatalf("unexpected bot method: %s", method)
		return nil
	})
	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	challenge.CommChatID = challenge.UserID
	challenge.ChatID = challenge.UserID
	challenge.JoinRequestQueryID = joinCaptchaTestQueryPrefix + "local"
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		testWebAppFormToken:    {challenge.WebAppToken},
		testWebAppFormChoice:   {challenge.SuccessUUID},
		testWebAppFormInitData: {signedWebAppInitData(t, botAPI.Token, "runtime-webapp-query", challenge.UserID)},
	}
	req := httptest.NewRequest(http.MethodPost, "/gatekeeper/join-captcha/answer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	gatekeeper.handleJoinCaptchaAnswer(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["ok"] != true || body["done"] != true {
		t.Fatalf("unexpected response: %#v", body)
	}
	if len(recorder.requests) != 0 {
		t.Fatalf("expected no bot requests, got %d", len(recorder.requests))
	}
	if len(store.challenges) != 0 {
		t.Fatalf("expected test challenge to be deleted, got %d rows", len(store.challenges))
	}
}

func TestJoinCaptchaWebAppSecurityHeadersAllowOnlyTelegramWebFraming(t *testing.T) {
	t.Parallel()

	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		store:  store,
		config: &config.Config{},
	}

	req := httptest.NewRequest(http.MethodGet, joinCaptchaPath+"?token="+url.QueryEscape(challenge.WebAppToken), nil)
	rr := httptest.NewRecorder()

	gatekeeper.joinCaptchaWebAppHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}

	header := rr.Header()
	if got := header.Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options blocks Telegram Web framing: %q", got)
	}
	if got := header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("expected no-referrer, got %q", got)
	}
	if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected nosniff, got %q", got)
	}
	if got := header.Get("Cross-Origin-Resource-Policy"); got != "cross-origin" {
		t.Fatalf("expected Telegram Web-compatible resource policy, got %q", got)
	}
	if got := header.Get("X-Robots-Tag"); !strings.Contains(got, "noindex") || !strings.Contains(got, "noai") {
		t.Fatalf("expected robot and ai indexing denial, got %q", got)
	}
	if got := header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("expected no-store cache control, got %q", got)
	}
	if got := header.Get("Permissions-Policy"); !strings.Contains(got, "camera=()") || !strings.Contains(got, "geolocation=()") {
		t.Fatalf("expected disabled browser capabilities, got %q", got)
	}

	csp := header.Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'none'",
		"frame-ancestors https://web.telegram.org",
		"object-src 'none'",
		"connect-src 'self'",
		"https://telegram.org",
		"script-src 'nonce-",
		"style-src 'nonce-",
	} {
		if !strings.Contains(csp, want) {
			t.Fatalf("expected CSP to contain %q, got %q", want, csp)
		}
	}
	if strings.Contains(csp, "'unsafe-inline'") {
		t.Fatalf("CSP must not allow unsafe inline execution: %q", csp)
	}
	if strings.Contains(csp, "evil.example") {
		t.Fatalf("attacker origin must not be allowed to frame the Mini App: %q", csp)
	}
	if body := rr.Body.String(); !strings.Contains(body, `nonce="`) || !strings.Contains(body, `name="robots"`) || !strings.Contains(body, `data-countdown`) || !strings.Contains(body, `data-feedback`) || !strings.Contains(body, `is-bad`) || !strings.Contains(body, `class="status"`) {
		t.Fatalf("expected rendered page to carry nonce, robots meta tags, countdown, and visual feedback")
	}
}

func TestJoinCaptchaWebAppLocalizesAndObfuscatesChallengeText(t *testing.T) {
	t.Parallel()

	store := newGatekeeperFlowStore()
	optionsJSON, err := encodeWebAppCaptchaOptions("ru", []webAppCaptchaOption{
		{ID: testCorrectChoice, Symbol: "🐩"},
		{ID: testWrongChoice, Symbol: "🍎"},
	})
	if err != nil {
		t.Fatalf("encode options: %v", err)
	}
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	challenge.CaptchaPrompt = testChallengePromptRU
	challenge.CaptchaOptionsJSON = optionsJSON
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		store:  store,
		config: &config.Config{},
	}

	req := httptest.NewRequest(http.MethodGet, joinCaptchaPath+"?token="+url.QueryEscape(challenge.WebAppToken), nil)
	rr := httptest.NewRecorder()

	gatekeeper.joinCaptchaWebAppHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"Контроль входа", "Проверка", "Выберите ", "секунд", "Жду выбор", "Проверяю ответ", `<html lang="ru">`, "telegram-web-app.js?63", `role="status"`, "prefers-reduced-motion"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected localized page to contain %q, got %q", want, body)
		}
	}
	for _, leaked := range []string{testChallengePromptRU, "🐩", "🍎"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("expected captcha text %q to be obfuscated, got %q", leaked, body)
		}
	}
}

func TestJoinCaptchaWebAppRendersReadableMissingChallengePage(t *testing.T) {
	t.Parallel()

	gatekeeper := &Gatekeeper{
		store:  newGatekeeperFlowStore(),
		config: &config.Config{},
	}

	req := httptest.NewRequest(http.MethodGet, joinCaptchaPath+"?token=missing", nil)
	rr := httptest.NewRecorder()

	gatekeeper.joinCaptchaWebAppHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("expected html error page, got %q", got)
	}
	body := rr.Body.String()
	for _, want := range []string{"404", "missing, already used, or no longer active", "Open a fresh CAPTCHA"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected error page to contain %q, got %q", want, body)
		}
	}
}

func TestJoinCaptchaWebAppRendersLocalizedMissingChallengePage(t *testing.T) {
	t.Parallel()

	gatekeeper := &Gatekeeper{
		store:  newGatekeeperFlowStore(),
		config: &config.Config{},
	}

	req := httptest.NewRequest(http.MethodGet, joinCaptchaPath+"?token=missing", nil)
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.1")
	rr := httptest.NewRecorder()

	gatekeeper.joinCaptchaWebAppHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"404", "Эта CAPTCHA не найдена", "Откройте новую CAPTCHA"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected localized error page to contain %q, got %q", want, body)
		}
	}
}

func TestJoinCaptchaWebAppRendersReadableExpiredChallengePage(t *testing.T) {
	t.Parallel()

	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(-time.Minute))
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		store:  store,
		config: &config.Config{},
	}

	req := httptest.NewRequest(http.MethodGet, joinCaptchaPath+"?token="+url.QueryEscape(challenge.WebAppToken), nil)
	rr := httptest.NewRecorder()

	gatekeeper.joinCaptchaWebAppHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"404", "has expired", "Open a fresh CAPTCHA"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected expired page to contain %q, got %q", want, body)
		}
	}
}

func TestJoinCaptchaWebAppRobotsAndSitemapDenyCrawlers(t *testing.T) {
	t.Parallel()

	gatekeeper := &Gatekeeper{
		store:  newGatekeeperFlowStore(),
		config: &config.Config{},
	}

	robotsReq := httptest.NewRequest(http.MethodGet, joinCaptchaRobotsPath, nil)
	robotsRR := httptest.NewRecorder()

	gatekeeper.joinCaptchaWebAppHandler().ServeHTTP(robotsRR, robotsReq)

	if robotsRR.Code != http.StatusOK {
		t.Fatalf("unexpected robots status %d: %s", robotsRR.Code, robotsRR.Body.String())
	}
	robotsBody := robotsRR.Body.String()
	for _, want := range []string{"User-agent: *", "Disallow: /", "Noindex: /", "User-agent: gptbot", "User-agent: claudebot"} {
		if !strings.Contains(robotsBody, want) {
			t.Fatalf("expected robots.txt to contain %q, got %q", want, robotsBody)
		}
	}
	if got := robotsRR.Header().Get("X-Robots-Tag"); !strings.Contains(got, "noindex") || !strings.Contains(got, "noimageai") {
		t.Fatalf("expected robots header, got %q", got)
	}

	sitemapReq := httptest.NewRequest(http.MethodGet, joinCaptchaSitemapPath, nil)
	sitemapRR := httptest.NewRecorder()

	gatekeeper.joinCaptchaWebAppHandler().ServeHTTP(sitemapRR, sitemapReq)

	if sitemapRR.Code != http.StatusOK {
		t.Fatalf("unexpected sitemap status %d: %s", sitemapRR.Code, sitemapRR.Body.String())
	}
	if body := sitemapRR.Body.String(); !strings.Contains(body, "<urlset") || strings.Contains(body, "<url>") {
		t.Fatalf("expected an empty sitemap, got %q", body)
	}
}

func TestJoinCaptchaWebAppBlocksKnownCrawlerUserAgent(t *testing.T) {
	t.Parallel()

	gatekeeper := &Gatekeeper{
		store:  newGatekeeperFlowStore(),
		config: &config.Config{},
	}

	req := httptest.NewRequest(http.MethodGet, joinCaptchaPath+"?token=missing", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 GPTBot/1.0")
	rr := httptest.NewRecorder()

	gatekeeper.joinCaptchaWebAppHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
		t.Fatalf("expected robots denial header, got %q", got)
	}
}

func TestJoinCaptchaAnswerRejectsCrossSitePostBeforeValidation(t *testing.T) {
	t.Parallel()

	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		store:  store,
		config: &config.Config{},
	}

	form := url.Values{
		testWebAppFormToken:  {challenge.WebAppToken},
		testWebAppFormChoice: {challenge.SuccessUUID},
	}
	req := httptest.NewRequest(http.MethodPost, joinCaptchaAnswerPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := httptest.NewRecorder()

	gatekeeper.joinCaptchaWebAppHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	if got := store.onlyChallenge(t); got.Attempts != 0 {
		t.Fatalf("expected no challenge mutation, got attempts=%d", got.Attempts)
	}
}

func TestJoinCaptchaAnswerRejectsCrossOriginPostWithoutFetchMetadata(t *testing.T) {
	t.Parallel()

	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		store:  store,
		config: &config.Config{},
	}

	form := url.Values{
		testWebAppFormToken:  {challenge.WebAppToken},
		testWebAppFormChoice: {challenge.SuccessUUID},
	}
	req := httptest.NewRequest(http.MethodPost, joinCaptchaAnswerPath, strings.NewReader(form.Encode()))
	req.Host = "antifraud.rtfm.rsvp"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()

	gatekeeper.joinCaptchaWebAppHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	if got := store.onlyChallenge(t); got.Attempts != 0 {
		t.Fatalf("expected no challenge mutation, got attempts=%d", got.Attempts)
	}
}

func TestJoinCaptchaAnswerRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	gatekeeper := &Gatekeeper{
		store:  newGatekeeperFlowStore(),
		config: &config.Config{},
	}
	body := strings.NewReader(strings.Repeat("a", int(joinCaptchaMaxRequestBodyBytes)+1))
	req := httptest.NewRequest(http.MethodPost, joinCaptchaAnswerPath, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	gatekeeper.handleJoinCaptchaAnswer(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
}

func TestJoinCaptchaAnswerRejectsInvalidInitData(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		t.Fatalf("unexpected bot method: %s", method)
		return nil
	})
	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		testWebAppFormToken:    {challenge.WebAppToken},
		testWebAppFormChoice:   {challenge.SuccessUUID},
		testWebAppFormInitData: {"bad=init"},
	}
	req := httptest.NewRequest(http.MethodPost, "/gatekeeper/join-captcha/answer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	gatekeeper.handleJoinCaptchaAnswer(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	if len(recorder.requests) != 0 {
		t.Fatalf("expected no bot requests, got %d", len(recorder.requests))
	}
}

func TestJoinCaptchaAnswerRejectsWrongUser(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		t.Fatalf("unexpected bot method: %s", method)
		return nil
	})
	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		testWebAppFormToken:    {challenge.WebAppToken},
		testWebAppFormChoice:   {challenge.SuccessUUID},
		testWebAppFormInitData: {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, 99)},
	}
	req := httptest.NewRequest(http.MethodPost, "/gatekeeper/join-captcha/answer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	gatekeeper.handleJoinCaptchaAnswer(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	if len(recorder.requests) != 0 {
		t.Fatalf("expected no bot requests, got %d", len(recorder.requests))
	}
}

func TestJoinCaptchaAnswerIncrementsWrongChoiceWithoutAnsweringQuery(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		t.Fatalf("unexpected bot method: %s", method)
		return nil
	})
	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		testWebAppFormToken:    {challenge.WebAppToken},
		testWebAppFormChoice:   {testWrongChoice},
		testWebAppFormInitData: {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
	}
	req := httptest.NewRequest(http.MethodPost, "/gatekeeper/join-captcha/answer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	gatekeeper.handleJoinCaptchaAnswer(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["ok"] != false || body["done"] != false {
		t.Fatalf("unexpected response: %#v", body)
	}
	if len(recorder.requests) != 0 {
		t.Fatalf("expected no bot requests, got %d", len(recorder.requests))
	}
	got := store.onlyChallenge(t)
	if got.Attempts != 1 {
		t.Fatalf("expected one failed attempt, got %d", got.Attempts)
	}
}

func TestJoinCaptchaAnswerUsesChallengeLocaleForVisibleErrors(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		t.Fatalf("unexpected bot method: %s", method)
		return nil
	})
	store := newGatekeeperFlowStore()
	optionsJSON, err := encodeWebAppCaptchaOptions("ru", []webAppCaptchaOption{
		{ID: testCorrectChoice, Symbol: "🐩"},
		{ID: testWrongChoice, Symbol: "🍎"},
	})
	if err != nil {
		t.Fatalf("encode options: %v", err)
	}
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	challenge.CaptchaPrompt = testChallengePromptRU
	challenge.CaptchaOptionsJSON = optionsJSON
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		testWebAppFormToken:    {challenge.WebAppToken},
		testWebAppFormChoice:   {testWrongChoice},
		testWebAppFormInitData: {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
	}
	req := httptest.NewRequest(http.MethodPost, "/gatekeeper/join-captcha/answer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	gatekeeper.handleJoinCaptchaAnswer(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["message"] != "Не тот вариант. Попробуйте ещё раз." {
		t.Fatalf("expected localized message, got %#v", body)
	}
}

func TestJoinCaptchaAnswerBlocksAfterTooManyWrongChoices(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		switch method {
		case testTelegramMethodGetChatMember:
			return map[string]any{
				"status": testMemberStatusLeft,
				"user":   map[string]any{"id": 42, testJSONIsBot: false, testJSONFirstName: testFirstNameNeo},
			}
		case testTelegramMethodJoinRequestQuery, testTelegramMethodBanChatMember:
			return true
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})
	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	challenge.Attempts = maxChallengeAttempts - 1
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		testWebAppFormToken:    {challenge.WebAppToken},
		testWebAppFormChoice:   {testWrongChoice},
		testWebAppFormInitData: {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
	}
	req := httptest.NewRequest(http.MethodPost, "/gatekeeper/join-captcha/answer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	gatekeeper.handleJoinCaptchaAnswer(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["ok"] != false || body["done"] != true {
		t.Fatalf("expected terminal blocked response, got %#v", body)
	}
	answers := recorder.byMethod(testTelegramMethodJoinRequestQuery)
	if len(answers) != 1 {
		t.Fatalf("expected one query answer, got %d", len(answers))
	}
	if answers[0].form.Get("result") != testJoinRequestDecline {
		t.Fatalf("expected decline result, got %q", answers[0].form.Get("result"))
	}
	if len(store.challenges) != 0 {
		t.Fatalf("expected failed challenge to be deleted, got %d rows", len(store.challenges))
	}
}

func TestJoinCaptchaAnswerReportsExpiredChallengeWithoutPunishment(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		switch method {
		case testTelegramMethodGetChatMember:
			return map[string]any{
				"status": testMemberStatusLeft,
				"user":   map[string]any{"id": 42, testJSONIsBot: false, testJSONFirstName: testFirstNameNeo},
			}
		case testTelegramMethodJoinRequestQuery, testTelegramMethodBanChatMember:
			return true
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})
	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(-time.Minute))
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		testWebAppFormToken:    {challenge.WebAppToken},
		testWebAppFormChoice:   {challenge.SuccessUUID},
		testWebAppFormInitData: {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
	}
	req := httptest.NewRequest(http.MethodPost, "/gatekeeper/join-captcha/answer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	gatekeeper.handleJoinCaptchaAnswer(rr, req)

	if rr.Code != http.StatusGone {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["ok"] != false || body["done"] != true {
		t.Fatalf("expected terminal expired response, got %#v", body)
	}
	if body["state"] != "expired" {
		t.Fatalf("expected expired state, got %#v", body)
	}
	if len(recorder.requests) != 0 {
		t.Fatalf("expired answer must not punish before DM fallback, got %d Telegram calls", len(recorder.requests))
	}
	if len(store.challenges) != 1 {
		t.Fatalf("expected expired challenge to remain durable for fallback, got %d rows", len(store.challenges))
	}
}

func TestStartJoinRequestWebAppChallengeFallsBackDurablyOnSendFailure(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPIWithErrors(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		switch method {
		case testTelegramMethodSendJoinWebApp:
			return nil
		case testTelegramMethodGetChat:
			if r.Form.Get("chat_id") == "9001" {
				return map[string]any{"id": 9001, testJSONType: telegramChatTypePrivate, testJSONFirstName: testFirstNameNeo}
			}
			return map[string]any{"id": -100123, testJSONType: testChatTypeSupergroup, testJSONTitle: testGroupTitle}
		case testTelegramMethodSendMessage:
			return recorder.nextSendMessageResult()
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	}, map[string]int{
		testTelegramMethodSendJoinWebApp: 400,
	})

	store := newGatekeeperFlowStore()
	gatekeeper := &Gatekeeper{
		bot: botAPI,
		s: &gatekeeperTestService{
			testBotService: testBotService{botAPI: botAPI, language: "en"},
			settings:       webAppSettings(),
		},
		store:      store,
		config:     &config.Config{GatekeeperWebApp: config.GatekeeperWebApp{PublicURL: testWebAppURL}},
		banChecker: &testGatekeeperBanChecker{},
	}

	req := &api.ChatJoinRequest{
		Chat:       api.Chat{ID: -100123, Type: testChatTypeSupergroup},
		From:       api.User{ID: 42, FirstName: testFirstNameNeo},
		UserChatID: 9001,
		QueryID:    testJoinQueryID,
	}

	sendErr := gatekeeper.startJoinRequestWebAppChallenge(context.Background(), req, webAppSettings())
	if sendErr == nil {
		t.Fatal("expected non-nil error from startJoinRequestWebAppChallenge when send fails")
	}

	if len(store.challenges) != 0 {
		t.Fatalf("ambiguous WebApp response remained active: %d rows", len(store.challenges))
	}
	if len(recorder.byMethod(testTelegramMethodJoinRequestQuery)) != 0 {
		t.Fatal("join request query must remain durable until the CAPTCHA resolves")
	}
}

func newWebAppChallenge(expiresAt time.Time) *db.Challenge {
	return &db.Challenge{
		CommChatID:         9001,
		UserID:             42,
		ChatID:             -100123,
		Status:             db.ChallengeStatusPending,
		SuccessUUID:        testCorrectChoice,
		WebAppToken:        "join-token",
		JoinRequestQueryID: testJoinQueryID,
		CaptchaPrompt:      testChallengePrompt,
		CaptchaOptionsJSON: testCaptchaOptionsJSON,
		CreatedAt:          time.Now(),
		ExpiresAt:          expiresAt,
	}
}

func webAppSettings() *db.Settings {
	return &db.Settings{
		GatekeeperEnabled:        true,
		GatekeeperCaptchaEnabled: true,
		ChallengeTimeout:         (3 * time.Minute).Nanoseconds(),
	}
}

func staleSignedWebAppInitData(t *testing.T, token string, queryID string, userID int64, authDate time.Time) string {
	t.Helper()

	userJSON := fmt.Sprintf(`{"id":%d,%q:%q}`, userID, testJSONFirstName, testFirstNameNeo)
	values := url.Values{
		"auth_date":  {strconv.FormatInt(authDate.Unix(), 10)},
		"query_id":   {queryID},
		logFieldUser: {userJSON},
	}

	dataCheck := make([]string, 0, len(values))
	for key, value := range values {
		dataCheck = append(dataCheck, key+"="+value[0])
	}
	sort.Strings(dataCheck)

	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))

	hash := hmac.New(sha256.New, secret.Sum(nil))
	hash.Write([]byte(strings.Join(dataCheck, "\n")))
	values.Set("hash", hex.EncodeToString(hash.Sum(nil)))

	return values.Encode()
}

func signedWebAppInitData(t *testing.T, token string, queryID string, userID int64) string {
	t.Helper()
	return staleSignedWebAppInitData(t, token, queryID, userID, time.Now())
}

func TestHandleJoinCaptchaAnswerRejectsStaleInitData(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		t.Fatalf("unexpected bot method: %s", method)
		return nil
	})

	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(time.Minute))
	challenge.CommChatID = 9001
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		testWebAppFormToken:    {challenge.WebAppToken},
		testWebAppFormChoice:   {challenge.SuccessUUID},
		testWebAppFormInitData: {staleSignedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID, time.Now().Add(-2*time.Hour))},
	}
	req := httptest.NewRequest(http.MethodPost, "/gatekeeper/join-captcha/answer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	gatekeeper.handleJoinCaptchaAnswer(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for stale init data, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleJoinCaptchaAnswerRejectsFutureInitData(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
		t.Fatalf("unexpected bot method: %s", method)
		return nil
	})
	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(time.Minute))
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}
	form := url.Values{
		testWebAppFormToken:    {challenge.WebAppToken},
		testWebAppFormChoice:   {challenge.SuccessUUID},
		testWebAppFormInitData: {staleSignedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID, time.Now().Add(2*time.Hour))},
	}
	req := httptest.NewRequest(http.MethodPost, joinCaptchaAnswerPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	gatekeeper.handleJoinCaptchaAnswer(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("future init data status = %d, want 401: %s", rr.Code, rr.Body.String())
	}
}

func TestJoinCaptchaStatusReportsDurableStatesWithoutRepeatingActions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     string
		expiresAt  time.Time
		wantStatus int
		wantState  string
		wantOK     bool
	}{
		{name: "pending", status: db.ChallengeStatusPending, expiresAt: time.Now().Add(time.Minute), wantStatus: http.StatusOK, wantState: "pending"},
		{name: "approval in progress", status: db.ChallengeStatusApproveQueryPending, expiresAt: time.Now().Add(time.Minute), wantStatus: http.StatusAccepted, wantState: "processing", wantOK: true},
		{name: "passed", status: db.ChallengeStatusPassedWaitingMemberJoin, expiresAt: time.Now().Add(time.Minute), wantStatus: http.StatusOK, wantState: "passed", wantOK: true},
		{name: "rejected", status: db.ChallengeStatusRejectPending, expiresAt: time.Now().Add(time.Minute), wantStatus: http.StatusForbidden, wantState: "rejected"},
		{name: "expired", status: db.ChallengeStatusPending, expiresAt: time.Now().Add(-time.Minute), wantStatus: http.StatusGone, wantState: "expired"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			botAPI := newTestBotAPI(t, func(method string, _ *http.Request) any {
				t.Fatalf("unexpected bot method: %s", method)
				return nil
			})
			store := newGatekeeperFlowStore()
			challenge := newWebAppChallenge(tt.expiresAt)
			challenge.Status = tt.status
			if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
				t.Fatalf("create challenge: %v", err)
			}
			gatekeeper := &Gatekeeper{bot: botAPI, store: store, config: &config.Config{}}
			form := url.Values{
				testWebAppFormToken:    {challenge.WebAppToken},
				testWebAppFormInitData: {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
			}
			req := httptest.NewRequest(http.MethodPost, "/gatekeeper/join-captcha/status", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()
			gatekeeper.joinCaptchaWebAppHandler().ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
			var response map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response["state"] != tt.wantState || response["ok"] != tt.wantOK {
				t.Fatalf("response = %+v, want state=%q ok=%t", response, tt.wantState, tt.wantOK)
			}
		})
	}
}

func TestHandleJoinCaptchaAnswerPersistsApprovalRetryWhenApproveFails(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPIWithErrors(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodJoinRequestQuery:
			return nil
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	}, map[string]int{
		testTelegramMethodJoinRequestQuery: 502,
	})

	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(time.Minute))
	challenge.CommChatID = 9001
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		testWebAppFormToken:    {challenge.WebAppToken},
		testWebAppFormChoice:   {challenge.SuccessUUID},
		testWebAppFormInitData: {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
	}
	req := httptest.NewRequest(http.MethodPost, "/gatekeeper/join-captcha/answer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	gatekeeper.handleJoinCaptchaAnswer(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["done"] != true || body["state"] != "processing" {
		t.Fatalf("expected durable processing response, got %#v", body)
	}
	if len(store.challenges) != 0 {
		t.Fatalf("uncertain query result remained eligible for blind retry: %#v", store.challenges)
	}
}

func TestHandleJoinCaptchaAnswerDeclinesKnownBannedUser(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		switch method {
		case testTelegramMethodGetChatMember:
			return map[string]any{
				"status": testMemberStatusLeft,
				"user":   map[string]any{"id": 42, testJSONIsBot: false, testJSONFirstName: testFirstNameNeo},
			}
		case testTelegramMethodJoinRequestQuery, testTelegramMethodBanChatMember:
			return true
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	challenge.CommChatID = 9001
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	banChecker := &testGatekeeperBanChecker{
		knownBanned: map[int64]bool{challenge.UserID: true},
	}
	gatekeeper := &Gatekeeper{
		bot:        botAPI,
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: banChecker,
	}

	form := url.Values{
		testWebAppFormToken:    {challenge.WebAppToken},
		testWebAppFormChoice:   {challenge.SuccessUUID},
		testWebAppFormInitData: {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
	}
	req := httptest.NewRequest(http.MethodPost, "/gatekeeper/join-captcha/answer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	gatekeeper.handleJoinCaptchaAnswer(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for known-banned user, got %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["ok"] != false || body["done"] != true {
		t.Fatalf("expected terminal blocked response, got %#v", body)
	}

	answers := recorder.byMethod(testTelegramMethodJoinRequestQuery)
	if len(answers) != 1 {
		t.Fatalf("expected one query answer, got %d", len(answers))
	}
	if got := answers[0].form.Get("result"); got != testJoinRequestDecline {
		t.Fatalf("expected decline result for banned user, got %q", got)
	}

	if len(store.challenges) != 0 {
		t.Fatalf("expected challenge to be deleted after ban decline, got %d rows", len(store.challenges))
	}
}

func TestHandleJoinCaptchaAnswerAllowsManuallyAllowlistedKnownBannedUser(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		switch method {
		case testTelegramMethodJoinRequestQuery, testTelegramMethodBanChatMember:
			return true
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	})

	store := newGatekeeperFlowStore()
	store.isNotSpammer = true
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	gatekeeper := &Gatekeeper{
		bot:    botAPI,
		s:      &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:  store,
		config: &config.Config{},
		banChecker: &testGatekeeperBanChecker{
			knownBanned: map[int64]bool{challenge.UserID: true},
		},
	}

	form := url.Values{
		testWebAppFormToken:    {challenge.WebAppToken},
		testWebAppFormChoice:   {challenge.SuccessUUID},
		testWebAppFormInitData: {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
	}
	req := httptest.NewRequest(http.MethodPost, "/gatekeeper/join-captcha/answer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	gatekeeper.handleJoinCaptchaAnswer(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for manually allowlisted user, got %d: %s", rr.Code, rr.Body.String())
	}
	answers := recorder.byMethod(testTelegramMethodJoinRequestQuery)
	if len(answers) != 1 {
		t.Fatalf("expected one query answer, got %d", len(answers))
	}
	if got := answers[0].form.Get("result"); got != "approve" {
		t.Fatalf("expected approve result for manually allowlisted user, got %q", got)
	}
	got := store.onlyChallenge(t)
	if got.Status != db.ChallengeStatusPassedWaitingMemberJoin {
		t.Fatalf("expected handoff status, got %q", got.Status)
	}
}

func TestJoinCaptchaReadinessRequiresSignedBoundInitData(t *testing.T) {
	t.Parallel()

	store := newGatekeeperFlowStore()
	expiresAt := time.Now().Add(3 * time.Minute)
	challenge := newWebAppChallenge(expiresAt)
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		bot:    newTestBotAPI(t, func(method string, _ *http.Request) any { t.Fatalf("unexpected bot method: %s", method); return nil }),
		store:  store,
		config: &config.Config{},
	}

	req := httptest.NewRequest(http.MethodGet, joinCaptchaPath+"?token="+url.QueryEscape(challenge.WebAppToken), nil)
	rr := httptest.NewRecorder()

	gatekeeper.joinCaptchaWebAppHandler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rr.Code, rr.Body.String())
	}
	if got := store.onlyChallenge(t); got.WebAppOpenedAt.Valid {
		t.Fatal("unauthenticated GET must not mark WebApp readiness")
	}

	invalidForm := url.Values{
		testWebAppFormToken:    {challenge.WebAppToken},
		testWebAppFormInitData: {"bad=init"},
	}
	invalidReq := httptest.NewRequest(http.MethodPost, "/gatekeeper/join-captcha/ready", strings.NewReader(invalidForm.Encode()))
	invalidReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	invalidRR := httptest.NewRecorder()
	gatekeeper.joinCaptchaWebAppHandler().ServeHTTP(invalidRR, invalidReq)
	if invalidRR.Code != http.StatusUnauthorized {
		t.Fatalf("invalid readiness status = %d, want 401: %s", invalidRR.Code, invalidRR.Body.String())
	}
	if got := store.onlyChallenge(t); got.WebAppOpenedAt.Valid {
		t.Fatal("invalid readiness must not mark WebApp opened")
	}

	validForm := url.Values{
		testWebAppFormToken: {challenge.WebAppToken},
		testWebAppFormInitData: {signedWebAppInitData(
			t,
			gatekeeper.bot.Token,
			challenge.JoinRequestQueryID,
			challenge.UserID,
		)},
	}
	validReq := httptest.NewRequest(http.MethodPost, "/gatekeeper/join-captcha/ready", strings.NewReader(validForm.Encode()))
	validReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	validRR := httptest.NewRecorder()
	gatekeeper.joinCaptchaWebAppHandler().ServeHTTP(validRR, validReq)
	if validRR.Code != http.StatusOK {
		t.Fatalf("valid readiness status = %d, want 200: %s", validRR.Code, validRR.Body.String())
	}
	got := store.onlyChallenge(t)
	if !got.WebAppOpenedAt.Valid {
		t.Fatal("signed readiness must mark WebApp opened")
	}
	if !got.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("expected ExpiresAt to be unchanged, got %v (want %v)", got.ExpiresAt, expiresAt)
	}
}

func commandMessage(chat *api.Chat, user *api.User, text string) *api.Message {
	return &api.Message{
		MessageID: 1,
		Chat:      *chat,
		From:      user,
		Text:      text,
		Date:      time.Now().Unix(),
		Entities: []api.MessageEntity{{
			Type:   testEntityBotCommand,
			Offset: 0,
			Length: len(text),
		}},
	}
}
