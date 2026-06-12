package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/db"
	handlersbase "github.com/iamwavecut/ngbot/internal/handlers/base"
	"github.com/pborman/uuid"
	"github.com/pkg/errors"
)

const (
	joinCaptchaPath                = "/gatekeeper/join-captcha"
	joinCaptchaAnswerPath          = "/gatekeeper/join-captcha/answer"
	joinCaptchaRobotsPath          = "/robots.txt"
	joinCaptchaSitemapPath         = "/sitemap.xml"
	joinCaptchaCSPNonceBytes       = 16
	joinCaptchaMaxRequestBodyBytes = 16 << 10
)

type webAppCaptchaOption struct {
	ID     string `json:"id"`
	Symbol string `json:"symbol"`
}

type joinCaptchaPageData struct {
	CSPNonce string
	Token    string
	Prompt   string
	Options  []webAppCaptchaOption
}

type joinCaptchaAnswerResponse struct {
	OK      bool   `json:"ok"`
	Done    bool   `json:"done"`
	Message string `json:"message"`
}

type webAppInitData struct {
	QueryID string
	UserID  int64
}

var joinCaptchaBlockedCrawlerUserAgents = []string{
	"ahrefsbot",
	"amazonbot",
	"anthropic-ai",
	"applebot",
	"applebot-extended",
	"baiduspider",
	"bingbot",
	"blexbot",
	"bytespider",
	"ccbot",
	"chatgpt-user",
	"claude-web",
	"claudebot",
	"diffbot",
	"dotbot",
	"facebookbot",
	"google-extended",
	"googlebot",
	"googleother",
	"gptbot",
	"meta-externalagent",
	"mj12bot",
	"oai-searchbot",
	"perplexity-user",
	"perplexitybot",
	"petalbot",
	"semrushbot",
	"yandexbot",
}

var joinCaptchaPermissionsPolicy = strings.Join([]string{
	"accelerometer=()",
	"ambient-light-sensor=()",
	"autoplay=()",
	"battery=()",
	"camera=()",
	"clipboard-read=()",
	"clipboard-write=()",
	"display-capture=()",
	"document-domain=()",
	"encrypted-media=()",
	"fullscreen=()",
	"gamepad=()",
	"geolocation=()",
	"gyroscope=()",
	"hid=()",
	"identity-credentials-get=()",
	"interest-cohort=()",
	"local-fonts=()",
	"magnetometer=()",
	"microphone=()",
	"midi=()",
	"otp-credentials=()",
	"payment=()",
	"picture-in-picture=()",
	"publickey-credentials-create=()",
	"publickey-credentials-get=()",
	"screen-wake-lock=()",
	"serial=()",
	"storage-access=()",
	"usb=()",
	"web-share=()",
	"xr-spatial-tracking=()",
}, ", ")

var joinCaptchaTemplate = template.Must(template.New("join-captcha").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="color-scheme" content="light dark">
<meta name="robots" content="noindex,nofollow,noarchive,nosnippet,noimageindex,notranslate">
<meta name="googlebot" content="noindex,nofollow,noarchive,nosnippet,noimageindex">
<script nonce="{{.CSPNonce}}" src="https://telegram.org/js/telegram-web-app.js?62"></script>
<title>Human check</title>
<style nonce="{{.CSPNonce}}">
:root {
	color-scheme: light dark;
	--bg: var(--tg-theme-bg-color, #f8fafc);
	--text: var(--tg-theme-text-color, #18181b);
	--muted: var(--tg-theme-hint-color, #71717a);
	--surface: var(--tg-theme-secondary-bg-color, #ffffff);
	--line: rgba(113, 113, 122, 0.24);
	--accent: var(--tg-theme-button-color, #0f766e);
	--accent-text: var(--tg-theme-button-text-color, #ffffff);
}
* { box-sizing: border-box; }
body {
	margin: 0;
	min-height: 100dvh;
	font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
	background: var(--bg);
	color: var(--text);
}
main {
	width: min(100%, 560px);
	min-height: 100dvh;
	margin: 0 auto;
	padding: max(28px, env(safe-area-inset-top)) 20px max(28px, env(safe-area-inset-bottom));
	display: grid;
	align-content: center;
	gap: 22px;
}
.kicker {
	margin: 0;
	font-size: 12px;
	font-weight: 700;
	letter-spacing: .12em;
	text-transform: uppercase;
	color: var(--muted);
}
h1 {
	margin: 0;
	max-width: 11ch;
	font-size: clamp(42px, 14vw, 72px);
	line-height: .9;
	letter-spacing: 0;
}
.prompt {
	margin: 0;
	max-width: 34ch;
	font-size: 18px;
	line-height: 1.45;
	color: var(--muted);
}
.target {
	color: var(--text);
	font-weight: 750;
}
.options {
	display: grid;
	grid-template-columns: repeat(2, minmax(0, 1fr));
	gap: 10px;
	margin-top: 8px;
}
button {
	appearance: none;
	border: 1px solid var(--line);
	border-radius: 8px;
	min-height: 76px;
	background: var(--surface);
	color: var(--text);
	font-size: 34px;
	box-shadow: inset 0 1px 0 rgba(255,255,255,.12);
	transition: transform .22s cubic-bezier(.16, 1, .3, 1), border-color .22s cubic-bezier(.16, 1, .3, 1), opacity .22s cubic-bezier(.16, 1, .3, 1);
}
button:active { transform: translateY(1px) scale(.98); }
button:focus-visible { outline: 2px solid var(--accent); outline-offset: 3px; }
button[disabled] { opacity: .52; }
.status {
	min-height: 24px;
	font-size: 14px;
	line-height: 1.5;
	color: var(--muted);
}
.status[data-tone="good"] { color: var(--accent); }
.status[data-tone="bad"] { color: #b45309; }
.bar {
	width: 42px;
	height: 3px;
	border-radius: 999px;
	background: var(--accent);
	transform-origin: left center;
	animation: pulse 1.2s cubic-bezier(.16, 1, .3, 1) infinite;
}
@keyframes pulse {
	0%, 100% { transform: scaleX(.45); opacity: .55; }
	50% { transform: scaleX(1); opacity: 1; }
}
@media (min-width: 560px) {
	main { padding-inline: 32px; }
	.options { grid-template-columns: repeat(3, minmax(0, 1fr)); }
}
</style>
</head>
<body data-token="{{.Token}}">
<main>
<div class="bar" aria-hidden="true"></div>
<p class="kicker">Gatekeeper</p>
<h1>Human check</h1>
<p class="prompt">Select <span class="target">{{.Prompt}}</span> to continue into the chat.</p>
<section class="options" aria-label="CAPTCHA options">
{{range .Options}}<button type="button" data-choice="{{.ID}}" aria-label="Choose {{.Symbol}}">{{.Symbol}}</button>{{end}}
</section>
<div class="status" data-status>Waiting for your choice.</div>
</main>
<script nonce="{{.CSPNonce}}">
(() => {
	const app = window.Telegram && window.Telegram.WebApp;
	if (app) {
		app.ready();
		app.expand();
	}
	const token = document.body.dataset.token;
	const status = document.querySelector("[data-status]");
	const buttons = Array.from(document.querySelectorAll("[data-choice]"));
	const setStatus = (message, tone) => {
		status.textContent = message;
		status.dataset.tone = tone || "";
	};
	const setDisabled = value => buttons.forEach(button => { button.disabled = value; });
	const submit = async choice => {
		setDisabled(true);
		setStatus("Checking your answer...", "");
		const body = new URLSearchParams();
		body.set("token", token);
		body.set("choice", choice);
		body.set("init_data", app ? app.initData : "");
		try {
			const response = await fetch("` + joinCaptchaAnswerPath + `", {
				method: "POST",
				cache: "no-store",
				credentials: "same-origin",
				redirect: "error",
				headers: { "Content-Type": "application/x-www-form-urlencoded" },
				body
			});
			const data = await response.json().catch(() => ({}));
			if (!response.ok) {
				throw new Error(data.message || "Verification failed.");
			}
			if (data.ok && data.done) {
				setStatus(data.message || "Done. You can return to Telegram.", "good");
				if (app) {
					setTimeout(() => app.close(), 700);
				}
				return;
			}
			setStatus(data.message || "Try another option.", "bad");
			setDisabled(false);
		} catch (error) {
			setStatus(error.message || "Verification failed.", "bad");
			setDisabled(false);
		}
	};
	buttons.forEach(button => button.addEventListener("click", () => submit(button.dataset.choice)));
})();
</script>
</body>
</html>`))

func (g *Gatekeeper) startWebAppServer(context.Context) error {
	listenAddr := g.joinCaptchaListenAddr()
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen gatekeeper web app: %w", err)
	}

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           g.joinCaptchaWebAppHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	g.webAppServer = server

	g.workerWG.Go(func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			g.getLogEntry().WithField("error", err.Error()).Error("gatekeeper web app server stopped")
		}
	})
	return nil
}

func (g *Gatekeeper) joinCaptchaWebAppHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(joinCaptchaRobotsPath, handleJoinCaptchaRobots)
	mux.HandleFunc(joinCaptchaSitemapPath, handleJoinCaptchaSitemap)
	mux.HandleFunc(joinCaptchaPath, g.handleJoinCaptcha)
	mux.HandleFunc(joinCaptchaAnswerPath, g.handleJoinCaptchaAnswer)
	return joinCaptchaSecurityMiddleware(mux)
}

func (g *Gatekeeper) stopWebAppServer(ctx context.Context) error {
	if g.webAppServer == nil {
		return nil
	}
	server := g.webAppServer
	g.webAppServer = nil
	return server.Shutdown(ctx)
}

func (g *Gatekeeper) joinCaptchaPublicURL() string {
	if g == nil || g.config == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(g.config.GatekeeperWebApp.PublicURL), "/")
}

func (g *Gatekeeper) joinCaptchaListenAddr() string {
	if g == nil || g.config == nil || strings.TrimSpace(g.config.GatekeeperWebApp.ListenAddr) == "" {
		return ":8080"
	}
	return strings.TrimSpace(g.config.GatekeeperWebApp.ListenAddr)
}

func (g *Gatekeeper) joinCaptchaURL(token string) (string, error) {
	publicURL := g.joinCaptchaPublicURL()
	if publicURL == "" {
		return "", errors.New("gatekeeper web app public url is empty")
	}
	return publicURL + joinCaptchaPath + "?token=" + url.QueryEscape(token), nil
}

func (g *Gatekeeper) startJoinRequestWebAppChallenge(ctx context.Context, request *api.ChatJoinRequest, settings *db.Settings) error {
	entry := g.getLogEntry().WithField("method", "startJoinRequestWebAppChallenge")
	if request == nil {
		return nil
	}
	if request.QueryID == "" {
		return errors.New("join request query id is empty")
	}
	if settings == nil {
		return errors.New("settings are nil")
	}
	if request.From.IsBot {
		return nil
	}

	now := time.Now()
	successUUID := uuid.New()
	language := g.s.GetLanguage(ctx, request.Chat.ID, &request.From)
	options, correctVariant, err := g.createWebAppCaptchaOptions(language, settings.GatekeeperCaptchaOptionsCount, successUUID)
	if err != nil {
		return err
	}
	optionsJSON, err := json.Marshal(options)
	if err != nil {
		return fmt.Errorf("marshal captcha options: %w", err)
	}

	webAppToken := uuid.New()
	webAppURL, err := g.joinCaptchaURL(webAppToken)
	if err != nil {
		return err
	}
	challenge := &db.Challenge{
		CommChatID:         0,
		UserID:             request.From.ID,
		ChatID:             request.Chat.ID,
		Status:             db.ChallengeStatusPending,
		SuccessUUID:        successUUID,
		WebAppToken:        webAppToken,
		JoinRequestQueryID: request.QueryID,
		CaptchaPrompt:      correctVariant[1],
		CaptchaOptionsJSON: string(optionsJSON),
		CreatedAt:          now,
		ExpiresAt:          now.Add(settings.GetChallengeTimeout()),
	}
	if _, err := g.store.CreateChallenge(ctx, challenge); err != nil {
		entry.WithField("error", err.Error()).Error("failed to create web app challenge")
		return err
	}
	if err := handlersbase.IncrementDailyStat(ctx, g.s.GetDB(), request.Chat.ID, handlersbase.StatChallengeStarted); err != nil {
		entry.WithField("error", err.Error()).Warn("failed to increment started challenge stat")
	}
	if err := bot.SendJoinRequestWebApp(ctx, g.s.GetBot(), request.QueryID, webAppURL); err != nil {
		if deleteErr := g.store.DeleteChallenge(ctx, challenge.CommChatID, challenge.UserID, challenge.ChatID); deleteErr != nil {
			entry.WithField("error", deleteErr.Error()).Error("failed to delete unsent web app challenge")
		}
		return err
	}
	return nil
}

func (g *Gatekeeper) createWebAppCaptchaOptions(lang string, optionsCount int, successUUID string) ([]webAppCaptchaOption, [2]string, error) {
	captchaIndex := g.createCaptchaIndex(lang)
	if len(captchaIndex) == 0 {
		captchaIndex = g.createCaptchaIndex("en")
	}
	if len(captchaIndex) == 0 {
		return []webAppCaptchaOption{{ID: successUUID, Symbol: "A"}}, [2]string{"A", "apple"}, nil
	}

	targetSize := min(len(captchaIndex), normalizeCaptchaOptionsCount(optionsCount))
	captchaRandomSet := make([][2]string, 0, targetSize)
	usedIDs := make(map[int]struct{}, targetSize)
	for len(captchaRandomSet) < targetSize {
		ID := mathrand.Intn(len(captchaIndex))
		if _, ok := usedIDs[ID]; ok {
			continue
		}
		captchaRandomSet = append(captchaRandomSet, captchaIndex[ID])
		usedIDs[ID] = struct{}{}
	}
	correctVariant := captchaRandomSet[mathrand.Intn(len(captchaRandomSet))]

	options := make([]webAppCaptchaOption, 0, len(captchaRandomSet))
	for _, variant := range captchaRandomSet {
		optionID := uuid.New()
		if variant[0] == correctVariant[0] {
			optionID = successUUID
		}
		options = append(options, webAppCaptchaOption{
			ID:     optionID,
			Symbol: variant[0],
		})
	}
	sort.Slice(options, func(i, j int) bool {
		return options[i].Symbol < options[j].Symbol
	})
	return options, correctVariant, nil
}

func (g *Gatekeeper) handleJoinCaptcha(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	challenge, ok := g.webAppChallengeFromRequest(w, r)
	if !ok {
		return
	}
	options, err := decodeWebAppCaptchaOptions(challenge.CaptchaOptionsJSON)
	if err != nil {
		http.Error(w, "challenge is unavailable", http.StatusInternalServerError)
		return
	}
	nonce, err := newJoinCaptchaCSPNonce()
	if err != nil {
		http.Error(w, "challenge is unavailable", http.StatusInternalServerError)
		return
	}

	setJoinCaptchaSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", joinCaptchaPageCSP(nonce))
	if err := joinCaptchaTemplate.Execute(w, joinCaptchaPageData{
		CSPNonce: nonce,
		Token:    challenge.WebAppToken,
		Prompt:   challenge.CaptchaPrompt,
		Options:  options,
	}); err != nil {
		g.getLogEntry().WithField("error", err.Error()).Error("failed to render join captcha")
	}
}

func (g *Gatekeeper) handleJoinCaptchaAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJoinCaptchaJSON(w, http.StatusMethodNotAllowed, joinCaptchaAnswerResponse{Message: "Method not allowed."})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, joinCaptchaMaxRequestBodyBytes)
	if err := r.ParseForm(); err != nil {
		writeJoinCaptchaJSON(w, http.StatusBadRequest, joinCaptchaAnswerResponse{Message: "Invalid request."})
		return
	}

	challenge, ok := g.webAppChallengeFromRequest(w, r)
	if !ok {
		return
	}
	initDataRaw := r.Form.Get("init_data")
	valid, err := api.ValidateWebAppData(g.s.GetBot().Token, initDataRaw)
	if err != nil || !valid {
		writeJoinCaptchaJSON(w, http.StatusUnauthorized, joinCaptchaAnswerResponse{Message: "Telegram check failed."})
		return
	}
	initData, err := parseWebAppInitData(initDataRaw)
	if err != nil {
		writeJoinCaptchaJSON(w, http.StatusUnauthorized, joinCaptchaAnswerResponse{Message: "Telegram check failed."})
		return
	}
	if initData.UserID != challenge.UserID || initData.QueryID != challenge.JoinRequestQueryID {
		writeJoinCaptchaJSON(w, http.StatusForbidden, joinCaptchaAnswerResponse{Message: "This challenge belongs to another request."})
		return
	}
	if time.Now().After(challenge.ExpiresAt) {
		if err := g.declineWebAppChallenge(r.Context(), challenge); err != nil {
			g.getLogEntry().WithField("error", err.Error()).Error("failed to decline expired web app challenge")
		}
		writeJoinCaptchaJSON(w, http.StatusGone, joinCaptchaAnswerResponse{Message: "This check expired."})
		return
	}

	if r.Form.Get("choice") != challenge.SuccessUUID {
		challenge.Attempts++
		if challenge.Attempts >= maxChallengeAttempts {
			if err := g.declineWebAppChallenge(r.Context(), challenge); err != nil {
				g.getLogEntry().WithField("error", err.Error()).Error("failed to decline failed web app challenge")
			}
			writeJoinCaptchaJSON(w, http.StatusForbidden, joinCaptchaAnswerResponse{Message: "Too many wrong answers."})
			return
		}
		if err := g.store.UpdateChallenge(r.Context(), challenge); err != nil {
			writeJoinCaptchaJSON(w, http.StatusInternalServerError, joinCaptchaAnswerResponse{Message: "Could not save the answer."})
			return
		}
		writeJoinCaptchaJSON(w, http.StatusOK, joinCaptchaAnswerResponse{OK: false, Done: false, Message: "Wrong option. Try again."})
		return
	}

	challenge.Status = db.ChallengeStatusPassedWaitingMemberJoin
	challenge.ExpiresAt = time.Now().Add(approvedJoinRequestChallengeTTL)
	if err := g.store.UpdateChallenge(r.Context(), challenge); err != nil {
		writeJoinCaptchaJSON(w, http.StatusInternalServerError, joinCaptchaAnswerResponse{Message: "Could not save the result."})
		return
	}
	if err := bot.AnswerJoinRequestQuery(r.Context(), g.s.GetBot(), challenge.JoinRequestQueryID, bot.JoinRequestQueryResultApprove); err != nil {
		writeJoinCaptchaJSON(w, http.StatusBadGateway, joinCaptchaAnswerResponse{Message: "Could not approve the request."})
		return
	}
	if err := handlersbase.IncrementDailyStat(r.Context(), g.s.GetDB(), challenge.ChatID, handlersbase.StatChallengePassed); err != nil {
		g.getLogEntry().WithField("error", err.Error()).Warn("failed to increment passed challenge stat")
	}
	writeJoinCaptchaJSON(w, http.StatusOK, joinCaptchaAnswerResponse{OK: true, Done: true, Message: "Done. You can return to Telegram."})
}

func (g *Gatekeeper) webAppChallengeFromRequest(w http.ResponseWriter, r *http.Request) (*db.Challenge, bool) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		token = strings.TrimSpace(r.Form.Get("token"))
	}
	if token == "" {
		writeJoinCaptchaJSON(w, http.StatusNotFound, joinCaptchaAnswerResponse{Message: "Challenge not found."})
		return nil, false
	}
	challenge, err := g.store.GetChallengeByWebAppToken(r.Context(), token)
	if err != nil {
		g.getLogEntry().WithField("error", err.Error()).Error("failed to load web app challenge")
		writeJoinCaptchaJSON(w, http.StatusInternalServerError, joinCaptchaAnswerResponse{Message: "Challenge is unavailable."})
		return nil, false
	}
	if challenge == nil || challenge.Status != db.ChallengeStatusPending {
		writeJoinCaptchaJSON(w, http.StatusNotFound, joinCaptchaAnswerResponse{Message: "Challenge not found."})
		return nil, false
	}
	return challenge, true
}

func (g *Gatekeeper) declineWebAppChallenge(ctx context.Context, challenge *db.Challenge) error {
	var result error
	if err := handlersbase.IncrementDailyStat(ctx, g.s.GetDB(), challenge.ChatID, handlersbase.StatChallengeFailed); err != nil {
		g.getLogEntry().WithField("error", err.Error()).Warn("failed to increment failed challenge stat")
	}
	if challenge.JoinRequestQueryID != "" {
		result = bot.AnswerJoinRequestQuery(ctx, g.s.GetBot(), challenge.JoinRequestQueryID, bot.JoinRequestQueryResultDecline)
	}
	if err := g.store.DeleteChallenge(ctx, challenge.CommChatID, challenge.UserID, challenge.ChatID); err != nil {
		result = errors.WithMessage(err, "delete declined web app challenge")
	}
	return result
}

func decodeWebAppCaptchaOptions(raw string) ([]webAppCaptchaOption, error) {
	var options []webAppCaptchaOption
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		return nil, fmt.Errorf("decode captcha options: %w", err)
	}
	if len(options) == 0 {
		return nil, errors.New("captcha options are empty")
	}
	return options, nil
}

func parseWebAppInitData(raw string) (webAppInitData, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return webAppInitData{}, err
	}
	var user struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil {
		return webAppInitData{}, err
	}
	return webAppInitData{
		QueryID: values.Get("query_id"),
		UserID:  user.ID,
	}, nil
}

func writeJoinCaptchaJSON(w http.ResponseWriter, status int, response joinCaptchaAnswerResponse) {
	setJoinCaptchaSecurityHeaders(w.Header())
	setJoinCaptchaDefaultCSP(w.Header())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func handleJoinCaptchaRobots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", strings.Join([]string{http.MethodGet, http.MethodHead}, ", "))
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	setJoinCaptchaSecurityHeaders(w.Header())
	setJoinCaptchaDefaultCSP(w.Header())
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}

	var body strings.Builder
	body.WriteString("User-agent: *\n")
	body.WriteString("Disallow: /\n")
	body.WriteString("Noindex: /\n")
	body.WriteString("\n")
	for _, userAgent := range joinCaptchaBlockedCrawlerUserAgents {
		body.WriteString("User-agent: ")
		body.WriteString(userAgent)
		body.WriteString("\nDisallow: /\nNoindex: /\n\n")
	}
	_, _ = w.Write([]byte(body.String()))
}

func handleJoinCaptchaSitemap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", strings.Join([]string{http.MethodGet, http.MethodHead}, ", "))
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	setJoinCaptchaSecurityHeaders(w.Header())
	setJoinCaptchaDefaultCSP(w.Header())
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"></urlset>
`))
}

func joinCaptchaSecurityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setJoinCaptchaSecurityHeaders(w.Header())
		setJoinCaptchaDefaultCSP(w.Header())
		if isJoinCaptchaBlockedCrawler(r.UserAgent()) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if isJoinCaptchaCrossSiteMutation(r) {
			writeJoinCaptchaJSON(w, http.StatusForbidden, joinCaptchaAnswerResponse{Message: "Cross-site request blocked."})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func setJoinCaptchaSecurityHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store, no-cache, must-revalidate, private, max-age=0")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Expires", "0")
	header.Set("Origin-Agent-Cluster", "?1")
	header.Set("Permissions-Policy", joinCaptchaPermissionsPolicy)
	header.Set("Pragma", "no-cache")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Strict-Transport-Security", "max-age=31536000")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("X-Permitted-Cross-Domain-Policies", "none")
	header.Set("X-Robots-Tag", "noindex, nofollow, noarchive, nosnippet, noimageindex, notranslate, noai, noimageai")
}

func setJoinCaptchaDefaultCSP(header http.Header) {
	header.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'; img-src 'none'; manifest-src 'none'; media-src 'none'; worker-src 'none'")
}

func joinCaptchaPageCSP(nonce string) string {
	return strings.Join([]string{
		"default-src 'none'",
		"base-uri 'none'",
		"connect-src 'self'",
		"form-action 'none'",
		"frame-ancestors 'none'",
		"img-src 'none'",
		"manifest-src 'none'",
		"media-src 'none'",
		"object-src 'none'",
		"script-src 'nonce-" + nonce + "' https://telegram.org",
		"style-src 'nonce-" + nonce + "'",
		"worker-src 'none'",
	}, "; ")
}

func newJoinCaptchaCSPNonce() (string, error) {
	nonce := make([]byte, joinCaptchaCSPNonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("read csp nonce: %w", err)
	}
	return base64.RawStdEncoding.EncodeToString(nonce), nil
}

func isJoinCaptchaBlockedCrawler(userAgent string) bool {
	normalized := strings.ToLower(userAgent)
	for _, blocked := range joinCaptchaBlockedCrawlerUserAgents {
		if strings.Contains(normalized, blocked) {
			return true
		}
	}
	return false
}

func isJoinCaptchaCrossSiteMutation(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return false
	}
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	originURL, err := url.Parse(origin)
	if err != nil || originURL.Host == "" {
		return true
	}
	return !strings.EqualFold(originURL.Host, r.Host)
}
