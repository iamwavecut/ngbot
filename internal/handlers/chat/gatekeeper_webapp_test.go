package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/db"
)

func TestJoinCaptchaAnswerApprovesMatchingTokenUserAndChoice(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		switch method {
		case testTelegramMethodJoinRequestQuery:
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
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		"token":     {challenge.WebAppToken},
		"choice":    {challenge.SuccessUUID},
		"init_data": {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
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

	claimed, err := store.ClaimWebAppChallengeForApproval(t.Context(), challenge.WebAppToken)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !claimed {
		t.Fatal("expected first approval claim to win")
	}
	got := store.onlyChallenge(t)
	if got.Status != db.ChallengeStatusPassedWaitingMemberJoin {
		t.Fatalf("expected status to become passed after claim, got %q", got.Status)
	}

	claimed, err = store.ClaimWebAppChallengeForApproval(t.Context(), challenge.WebAppToken)
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

	claimed, err = store.ClaimWebAppChallengeForApproval(t.Context(), fallback.WebAppToken)
	if err != nil {
		t.Fatalf("claim against fallback-claimed row: %v", err)
	}
	if claimed {
		t.Fatal("expected approval claim to lose when a fallback already claimed the row")
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
	botAPI.Self.UserName = "testbot"
	store := newGatekeeperFlowStore()
	gatekeeper := &Gatekeeper{
		s:      &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:  store,
		config: &config.Config{GatekeeperWebApp: config.GatekeeperWebApp{PublicURL: "https://guard.example"}},
	}

	user := &api.User{ID: 42, FirstName: "Neo"}
	chat := &api.Chat{ID: user.ID, Type: "private"}
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
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		"token":     {challenge.WebAppToken},
		"choice":    {challenge.SuccessUUID},
		"init_data": {signedWebAppInitData(t, botAPI.Token, "runtime-webapp-query", challenge.UserID)},
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

func TestJoinCaptchaWebAppSecurityHeadersDenyIndexingEmbeddingAndBrowserCapabilities(t *testing.T) {
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
	if got := header.Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("expected frame denial, got %q", got)
	}
	if got := header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("expected no-referrer, got %q", got)
	}
	if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected nosniff, got %q", got)
	}
	if got := header.Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Fatalf("expected same-origin resource policy, got %q", got)
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
		"frame-ancestors 'none'",
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
	if body := rr.Body.String(); !strings.Contains(body, `nonce="`) || !strings.Contains(body, `name="robots"`) || !strings.Contains(body, `data-countdown`) || !strings.Contains(body, `data-feedback`) || !strings.Contains(body, `is-bad`) {
		t.Fatalf("expected rendered page to carry nonce, robots meta tags, countdown, and visual feedback")
	}
}

func TestJoinCaptchaWebAppLocalizesAndObfuscatesChallengeText(t *testing.T) {
	t.Parallel()

	store := newGatekeeperFlowStore()
	optionsJSON, err := encodeWebAppCaptchaOptions("ru", []webAppCaptchaOption{
		{ID: "correct-choice", Symbol: "🐩"},
		{ID: "wrong-choice", Symbol: "🍎"},
	})
	if err != nil {
		t.Fatalf("encode options: %v", err)
	}
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	challenge.CaptchaPrompt = "пуделя"
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
	for _, want := range []string{"Контроль входа", "Проверка", "Выберите ", "секунд", "Жду выбор", "Проверяю ответ"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected localized page to contain %q, got %q", want, body)
		}
	}
	for _, leaked := range []string{"пуделя", "🐩", "🍎"} {
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
		"token":  {challenge.WebAppToken},
		"choice": {challenge.SuccessUUID},
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
		"token":  {challenge.WebAppToken},
		"choice": {challenge.SuccessUUID},
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
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		"token":     {challenge.WebAppToken},
		"choice":    {challenge.SuccessUUID},
		"init_data": {"bad=init"},
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
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		"token":     {challenge.WebAppToken},
		"choice":    {challenge.SuccessUUID},
		"init_data": {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, 99)},
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
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		"token":     {challenge.WebAppToken},
		"choice":    {"wrong-choice"},
		"init_data": {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
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
		{ID: "correct-choice", Symbol: "🐩"},
		{ID: "wrong-choice", Symbol: "🍎"},
	})
	if err != nil {
		t.Fatalf("encode options: %v", err)
	}
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	challenge.CaptchaPrompt = "пуделя"
	challenge.CaptchaOptionsJSON = optionsJSON
	if _, err := store.CreateChallenge(t.Context(), challenge); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	gatekeeper := &Gatekeeper{
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		"token":     {challenge.WebAppToken},
		"choice":    {"wrong-choice"},
		"init_data": {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
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
		case testTelegramMethodJoinRequestQuery:
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
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		"token":     {challenge.WebAppToken},
		"choice":    {"wrong-choice"},
		"init_data": {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
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
	if answers[0].form.Get("result") != "decline" {
		t.Fatalf("expected decline result, got %q", answers[0].form.Get("result"))
	}
	if len(store.challenges) != 0 {
		t.Fatalf("expected failed challenge to be deleted, got %d rows", len(store.challenges))
	}
}

func TestJoinCaptchaAnswerDeclinesExpiredChallenge(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		switch method {
		case testTelegramMethodJoinRequestQuery:
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
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		"token":     {challenge.WebAppToken},
		"choice":    {challenge.SuccessUUID},
		"init_data": {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
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
	answers := recorder.byMethod(testTelegramMethodJoinRequestQuery)
	if len(answers) != 1 {
		t.Fatalf("expected one query answer, got %d", len(answers))
	}
	if answers[0].form.Get("result") != "decline" {
		t.Fatalf("expected decline result, got %q", answers[0].form.Get("result"))
	}
	if len(store.challenges) != 0 {
		t.Fatalf("expected expired challenge to be deleted, got %d rows", len(store.challenges))
	}
}

func TestStartJoinRequestWebAppChallengeQueuesOnSendFailure(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPIWithErrors(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		switch method {
		case testTelegramMethodSendJoinWebApp:
			return nil
		case testTelegramMethodJoinRequestQuery:
			return true
		default:
			t.Fatalf("unexpected bot method: %s", method)
			return nil
		}
	}, map[string]int{
		testTelegramMethodSendJoinWebApp: 400,
	})

	store := newGatekeeperFlowStore()
	gatekeeper := &Gatekeeper{
		s: &gatekeeperTestService{
			testBotService: testBotService{botAPI: botAPI, language: "en"},
			settings:       webAppSettings(),
		},
		store:      store,
		config:     &config.Config{GatekeeperWebApp: config.GatekeeperWebApp{PublicURL: "https://guard.example"}},
		banChecker: &testGatekeeperBanChecker{},
	}

	req := &api.ChatJoinRequest{
		Chat:       api.Chat{ID: -100123, Type: "supergroup"},
		From:       api.User{ID: 42, FirstName: "Neo"},
		UserChatID: 9001,
		QueryID:    "join-query",
	}

	sendErr := gatekeeper.startJoinRequestWebAppChallenge(context.Background(), req, webAppSettings())
	if sendErr == nil {
		t.Fatal("expected non-nil error from startJoinRequestWebAppChallenge when send fails")
	}

	if len(store.challenges) != 0 {
		t.Fatalf("expected challenge row to be deleted after send failure, got %d rows", len(store.challenges))
	}

	queryAnswers := recorder.byMethod(testTelegramMethodJoinRequestQuery)
	if len(queryAnswers) != 1 {
		t.Fatalf("expected exactly one answerChatJoinRequestQuery call, got %d", len(queryAnswers))
	}
	if got := queryAnswers[0].form.Get("result"); got != "queue" {
		t.Fatalf("expected queue result on send failure, got %q", got)
	}
}

func newWebAppChallenge(expiresAt time.Time) *db.Challenge {
	return &db.Challenge{
		CommChatID:         9001,
		UserID:             42,
		ChatID:             -100123,
		Status:             db.ChallengeStatusPending,
		SuccessUUID:        "correct-choice",
		WebAppToken:        "join-token",
		JoinRequestQueryID: "join-query",
		CaptchaPrompt:      "poodle",
		CaptchaOptionsJSON: `[{"id":"correct-choice","symbol":"A"},{"id":"wrong-choice","symbol":"B"}]`,
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

	values := url.Values{
		"auth_date": {strconv.FormatInt(authDate.Unix(), 10)},
		"query_id":  {queryID},
		"user":      {`{"id":` + strconv.FormatInt(userID, 10) + `,"first_name":"Neo"}`},
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
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		"token":     {challenge.WebAppToken},
		"choice":    {challenge.SuccessUUID},
		"init_data": {staleSignedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID, time.Now().Add(-2*time.Hour))},
	}
	req := httptest.NewRequest(http.MethodPost, "/gatekeeper/join-captcha/answer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	gatekeeper.handleJoinCaptchaAnswer(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for stale init data, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleJoinCaptchaAnswerKeepsPendingWhenApproveFails(t *testing.T) {
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
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: &testGatekeeperBanChecker{},
	}

	form := url.Values{
		"token":     {challenge.WebAppToken},
		"choice":    {challenge.SuccessUUID},
		"init_data": {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
	}
	req := httptest.NewRequest(http.MethodPost, "/gatekeeper/join-captcha/answer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	gatekeeper.handleJoinCaptchaAnswer(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rr.Code, rr.Body.String())
	}
	got := store.onlyChallenge(t)
	if got.Status != db.ChallengeStatusPending {
		t.Fatalf("expected status to remain pending, got %q", got.Status)
	}
}

func TestHandleJoinCaptchaAnswerDeclinesKnownBannedUser(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		switch method {
		case testTelegramMethodJoinRequestQuery:
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
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{},
		banChecker: banChecker,
	}

	form := url.Values{
		"token":     {challenge.WebAppToken},
		"choice":    {challenge.SuccessUUID},
		"init_data": {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
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
	if got := answers[0].form.Get("result"); got != "decline" {
		t.Fatalf("expected decline result for banned user, got %q", got)
	}

	if len(store.challenges) != 0 {
		t.Fatalf("expected challenge to be deleted after ban decline, got %d rows", len(store.challenges))
	}
}

func TestHandleJoinCaptchaMarksOpened(t *testing.T) {
	t.Parallel()

	store := newGatekeeperFlowStore()
	expiresAt := time.Now().Add(3 * time.Minute)
	challenge := newWebAppChallenge(expiresAt)
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
	got := store.onlyChallenge(t)
	if !got.WebAppOpenedAt.Valid {
		t.Fatal("expected WebAppOpenedAt.Valid to be true after page load")
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
			Type:   "bot_command",
			Offset: 0,
			Length: len(text),
		}},
	}
}
