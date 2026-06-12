package handlers

import (
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
	if body := rr.Body.String(); !strings.Contains(body, `nonce="`) || !strings.Contains(body, `name="robots"`) {
		t.Fatalf("expected rendered page to carry nonce and robots meta tags")
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

func newWebAppChallenge(expiresAt time.Time) *db.Challenge {
	return &db.Challenge{
		CommChatID:         0,
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

func signedWebAppInitData(t *testing.T, token string, queryID string, userID int64) string {
	t.Helper()

	values := url.Values{
		"auth_date": {strconv.FormatInt(time.Now().Unix(), 10)},
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
