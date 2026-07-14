package handlers

import (
	"context"
	"crypto/rand"
	"encoding/json"
	stderrors "errors"
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
	joinCaptchaObfuscationKeyBytes = 8
	joinCaptchaMaxRequestBodyBytes = 16 << 10
	joinCaptchaTestQueryPrefix     = "test:"
	joinCaptchaInitDataTTL         = time.Hour
)

type webAppCaptchaOption struct {
	ID     string `json:"id"`
	Symbol string `json:"symbol"`
}

type webAppCaptchaData struct {
	Locale  string                `json:"locale,omitempty"`
	Options []webAppCaptchaOption `json:"options"`
}

type joinCaptchaPageOption struct {
	ID     string
	Number int
}

type joinCaptchaPageData struct {
	CSPNonce      string
	Token         string
	Kicker        string
	Title         string
	Message       string
	Hint          string
	State         string
	PromptBefore  string
	PromptAfter   string
	SecondsLabel  string
	Waiting       string
	OptionLabel   string
	Options       []joinCaptchaPageOption
	ChallengeJSON template.JS
	LabelsJSON    template.JS
}

type joinCaptchaAnswerResponse struct {
	OK      bool   `json:"ok"`
	Done    bool   `json:"done"`
	Message string `json:"message"`
}

type webAppInitData struct {
	QueryID  string
	UserID   int64
	AuthDate int64
}

type joinCaptchaCopy struct {
	Kicker                  string
	Title                   string
	PromptTemplate          string
	SecondsLabel            string
	Waiting                 string
	MakeChoiceNow           string
	Checking                string
	PassedTitle             string
	BlockedTitle            string
	Done                    string
	TestDone                string
	Blocked                 string
	TryAnother              string
	VerificationFailed      string
	WrongOption             string
	ExpiredBlocked          string
	TooManyBlocked          string
	MethodNotAllowed        string
	InvalidRequest          string
	ChallengeNotFound       string
	ChallengeUnavailable    string
	TelegramCheckFailed     string
	OtherRequest            string
	CouldNotSaveAnswer      string
	CouldNotSaveResult      string
	CouldNotApprove         string
	MissingTokenMessage     string
	MissingChallengeMessage string
	ExpiredPageMessage      string
	OpenFresh               string
	UnavailableTitle        string
	UnavailableMessage      string
	TryAgainFromTelegram    string
	OptionLabel             string
}

type joinCaptchaClientLabels struct {
	MakeChoiceNow      string `json:"make_choice_now"`
	Checking           string `json:"checking"`
	PassedTitle        string `json:"passed_title"`
	BlockedTitle       string `json:"blocked_title"`
	Done               string `json:"done"`
	Blocked            string `json:"blocked"`
	TryAnother         string `json:"try_another"`
	VerificationFailed string `json:"verification_failed"`
}

type joinCaptchaObfuscatedText struct {
	Key  []int `json:"k"`
	Data []int `json:"d"`
}

type joinCaptchaClientPayload struct {
	Prompt  joinCaptchaObfuscatedText        `json:"prompt"`
	Options []joinCaptchaClientPayloadOption `json:"options"`
}

type joinCaptchaClientPayloadOption struct {
	ID   string                    `json:"id"`
	Text joinCaptchaObfuscatedText `json:"text"`
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

var joinCaptchaCopies = map[string]joinCaptchaCopy{
	"en": {
		Kicker:                  "Gatekeeper",
		Title:                   "Human check",
		PromptTemplate:          "Select {target} to continue into the chat.",
		SecondsLabel:            "seconds",
		Waiting:                 "Waiting for your choice.",
		MakeChoiceNow:           "Make your choice now.",
		Checking:                "Checking your answer...",
		PassedTitle:             "Passed",
		BlockedTitle:            "Blocked",
		Done:                    "Done. You can return to Telegram.",
		TestDone:                "Test passed. You can return to Telegram.",
		Blocked:                 "Request blocked.",
		TryAnother:              "Try another option.",
		VerificationFailed:      "Verification failed.",
		WrongOption:             "Wrong option. Try again.",
		ExpiredBlocked:          "This check expired. The request was blocked.",
		TooManyBlocked:          "Too many wrong answers. The request was blocked.",
		MethodNotAllowed:        "Method not allowed.",
		InvalidRequest:          "Invalid request.",
		ChallengeNotFound:       "Challenge not found.",
		ChallengeUnavailable:    "Challenge is unavailable.",
		TelegramCheckFailed:     "Telegram check failed.",
		OtherRequest:            "This challenge belongs to another request.",
		CouldNotSaveAnswer:      "Could not save the answer.",
		CouldNotSaveResult:      "Could not save the result.",
		CouldNotApprove:         "Could not approve the request.",
		MissingTokenMessage:     "This CAPTCHA link is missing its token.",
		MissingChallengeMessage: "This CAPTCHA link is missing, already used, or no longer active.",
		ExpiredPageMessage:      "This CAPTCHA link has expired.",
		OpenFresh:               "Open a fresh CAPTCHA from Telegram.",
		UnavailableTitle:        "Unavailable",
		UnavailableMessage:      "This CAPTCHA cannot be loaded right now.",
		TryAgainFromTelegram:    "Please try again from Telegram.",
		OptionLabel:             "Option",
	},
	"ru": {
		Kicker:                  "Контроль входа",
		Title:                   "Проверка",
		PromptTemplate:          "Выберите {target}, чтобы войти в чат.",
		SecondsLabel:            "секунд",
		Waiting:                 "Жду выбор.",
		MakeChoiceNow:           "Пора выбирать.",
		Checking:                "Проверяю ответ...",
		PassedTitle:             "Пройдено",
		BlockedTitle:            "Заблокировано",
		Done:                    "Готово. Вернитесь в Telegram.",
		TestDone:                "Тест пройден. Вернитесь в Telegram.",
		Blocked:                 "Заявка заблокирована.",
		TryAnother:              "Попробуйте другой вариант.",
		VerificationFailed:      "Проверка не прошла.",
		WrongOption:             "Не тот вариант. Попробуйте ещё раз.",
		ExpiredBlocked:          "Проверка истекла. Заявка заблокирована.",
		TooManyBlocked:          "Слишком много неверных ответов. Заявка заблокирована.",
		MethodNotAllowed:        "Метод не поддерживается.",
		InvalidRequest:          "Некорректный запрос.",
		ChallengeNotFound:       "Проверка не найдена.",
		ChallengeUnavailable:    "Проверка сейчас недоступна.",
		TelegramCheckFailed:     "Не удалось проверить данные Telegram.",
		OtherRequest:            "Эта проверка относится к другой заявке.",
		CouldNotSaveAnswer:      "Не удалось сохранить ответ.",
		CouldNotSaveResult:      "Не удалось сохранить результат.",
		CouldNotApprove:         "Не удалось одобрить заявку.",
		MissingTokenMessage:     "В ссылке на CAPTCHA нет токена.",
		MissingChallengeMessage: "Эта CAPTCHA не найдена, уже использована или больше не активна.",
		ExpiredPageMessage:      "Ссылка на CAPTCHA устарела.",
		OpenFresh:               "Откройте новую CAPTCHA из Telegram.",
		UnavailableTitle:        "Недоступно",
		UnavailableMessage:      "CAPTCHA сейчас не загружается.",
		TryAgainFromTelegram:    "Попробуйте ещё раз из Telegram.",
		OptionLabel:             "Вариант",
	},
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
button.is-checking { border-color: var(--accent); filter: brightness(1.08) saturate(1.18); }
button.is-good {
	border-color: #059669;
	filter: brightness(1.08) saturate(1.25);
	box-shadow: 0 0 0 3px rgba(5, 150, 105, .18);
}
button.is-bad {
	border-color: #b91c1c;
	filter: sepia(.45) saturate(1.7) hue-rotate(-18deg);
	box-shadow: 0 0 0 3px rgba(185, 28, 28, .16);
}
.status {
	min-height: 24px;
	font-size: 14px;
	line-height: 1.5;
	color: var(--muted);
}
.status[data-tone="good"] { color: var(--accent); }
.status[data-tone="bad"] { color: #b45309; }
.timer {
	display: inline-flex;
	align-items: baseline;
	gap: 6px;
	width: fit-content;
	padding: 7px 10px;
	border: 1px solid var(--line);
	border-radius: 8px;
	color: var(--muted);
	font-size: 13px;
	line-height: 1;
}
.timer strong {
	min-width: 2ch;
	color: var(--text);
	font-size: 22px;
	font-variant-numeric: tabular-nums;
	text-align: center;
}
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
@keyframes shake {
	0%, 100% { transform: translateX(0); }
	25% { transform: translateX(-5px); }
	75% { transform: translateX(5px); }
}
main[data-feedback="bad"] .options { animation: shake .22s ease-out; }
main[data-feedback="good"] .options { filter: brightness(1.04) saturate(1.22); }
main[data-state="blocked"] .options,
main[data-state="done"] .options { display: none; }
main[data-state="blocked"] .bar {
	background: #b91c1c;
	animation: none;
	transform: scaleX(1);
}
@media (min-width: 560px) {
	main { padding-inline: 32px; }
	.options { grid-template-columns: repeat(3, minmax(0, 1fr)); }
}
</style>
</head>
<body data-token="{{.Token}}">
<main{{if .State}} data-state="{{.State}}"{{end}}>
<div class="bar" aria-hidden="true"></div>
<p class="kicker">{{.Kicker}}</p>
<h1 data-title>{{.Title}}</h1>
{{if .Options}}
<p class="prompt">{{.PromptBefore}}<span class="target" data-prompt></span>{{.PromptAfter}}</p>
<div class="timer" aria-live="polite"><strong data-countdown>10</strong><span>{{.SecondsLabel}}</span></div>
<section class="options" aria-label="CAPTCHA options">
{{range .Options}}<button type="button" data-choice="{{.ID}}" aria-label="{{$.OptionLabel}} {{.Number}}"></button>{{end}}
</section>
<div class=logFieldStatus data-status>{{.Waiting}}</div>
{{else}}
<p class="prompt">{{.Message}}</p>
<div class=logFieldStatus data-tone="bad" data-status>{{.Hint}}</div>
{{end}}
</main>
<script nonce="{{.CSPNonce}}">
(() => {
	const app = window.Telegram && window.Telegram.WebApp;
	if (app) {
		app.ready();
		app.expand();
	}
	const token = document.body.dataset.token;
	const root = document.querySelector("main");
	const title = document.querySelector("[data-title]");
	const status = document.querySelector("[data-status]");
	const countdown = document.querySelector("[data-countdown]");
	const buttons = Array.from(document.querySelectorAll("[data-choice]"));
	const labels = {{.LabelsJSON}};
	const challenge = {{.ChallengeJSON}};
	if (!countdown || buttons.length === 0) {
		return;
	}
	const decodeText = encoded => {
		const bytes = new Uint8Array(encoded.d.map((value, index) => value ^ encoded.k[index % encoded.k.length]));
		return new TextDecoder().decode(bytes);
	};
	document.querySelector("[data-prompt]").textContent = decodeText(challenge.prompt);
	buttons.forEach((button, index) => {
		const option = challenge.options[index];
		button.textContent = decodeText(option.text);
	});
	const setStatus = (message, tone) => {
		status.textContent = message;
		status.dataset.tone = tone || "";
	};
	const setDisabled = value => buttons.forEach(button => { button.disabled = value; });
	let feedbackTimer;
	const setFeedback = (tone, button) => {
		window.clearTimeout(feedbackTimer);
		root.dataset.feedback = tone || "";
		buttons.forEach(item => item.classList.remove("is-checking", "is-good", "is-bad"));
		if (button && tone) {
			button.classList.add("is-" + tone);
		}
		if (tone === "good" || tone === "bad") {
			feedbackTimer = window.setTimeout(() => {
				if (!root.dataset.state) {
					root.dataset.feedback = "";
				}
				if (button) {
					button.classList.remove("is-" + tone);
				}
			}, 650);
		}
	};
	let seconds = 10;
	const tick = () => {
		countdown.textContent = String(seconds);
		if (seconds === 0) {
			window.clearInterval(timer);
			setStatus(labels.make_choice_now, "");
			return;
		}
		seconds -= 1;
	};
	const timer = window.setInterval(tick, 1000);
	tick();
	const finish = (message, ok, button) => {
		window.clearInterval(timer);
		setDisabled(true);
		root.dataset.state = ok ? "done" : "blocked";
		title.textContent = ok ? labels.passed_title : labels.blocked_title;
		countdown.textContent = "0";
		setFeedback(ok ? "good" : "bad", button);
		setStatus(message || (ok ? labels.done : labels.blocked), ok ? "good" : "bad");
		if (ok && app) {
			setTimeout(() => app.close(), 700);
		}
	};
	const submit = async button => {
		const choice = button.dataset.choice;
		setDisabled(true);
		setFeedback("checking", button);
		setStatus(labels.checking, "");
		const body = new URLSearchParams();
		body.set("token", token);
		body.set("choice", choice);
		body.set("init_data", app ? app.initData : "");
		try {
			const response = await fetch("` + joinCaptchaAnswerPath + `", {
				method: "POST",
				cache: "no-store",
				credentials: "same-origin",
				redirect: logFieldError,
				headers: { "Content-Type": "application/x-www-form-urlencoded" },
				body
			});
			const data = await response.json().catch(() => ({}));
			if (data.done) {
				finish(data.message, Boolean(data.ok), button);
				return;
			}
			if (!response.ok) {
				throw new Error(data.message || labels.verification_failed);
			}
			setFeedback("bad", button);
			setStatus(data.message || labels.try_another, "bad");
			setDisabled(false);
		} catch (error) {
			setFeedback("bad", button);
			setStatus(error.message || labels.verification_failed, "bad");
			setDisabled(false);
		}
	};
	buttons.forEach(button => button.addEventListener("click", () => submit(button)));
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
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	g.webAppServer = server

	g.workerWG.Go(func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			g.getLogEntry().WithField(logFieldError, err.Error()).Error("gatekeeper web app server stopped")
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
	entry := g.getLogEntry().WithField(logFieldMethod, "startJoinRequestWebAppChallenge")
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
	language := g.webAppChallengeLanguage(ctx, request.Chat.ID, &request.From)
	options, correctVariant := g.createWebAppCaptchaOptions(language, settings.GatekeeperCaptchaOptionsCount, successUUID)
	optionsJSON, err := encodeWebAppCaptchaOptions(language, options)
	if err != nil {
		return fmt.Errorf("marshal captcha options: %w", err)
	}

	webAppToken := uuid.New()
	webAppURL, err := g.joinCaptchaURL(webAppToken)
	if err != nil {
		return err
	}
	challenge := &db.Challenge{
		CommChatID:         request.UserChatID,
		UserID:             request.From.ID,
		ChatID:             request.Chat.ID,
		Status:             db.ChallengeStatusPending,
		SuccessUUID:        successUUID,
		WebAppToken:        webAppToken,
		JoinRequestQueryID: request.QueryID,
		CaptchaPrompt:      correctVariant[1],
		CaptchaOptionsJSON: string(optionsJSON),
		UserLanguage:       strings.TrimSpace(request.From.LanguageCode),
		CreatedAt:          now,
		ExpiresAt:          now.Add(settings.GetChallengeTimeout()),
	}
	if _, err := g.store.CreateChallenge(ctx, challenge); err != nil {
		entry.WithField(logFieldError, err.Error()).Error("failed to create web app challenge")
		return err
	}
	if err := handlersbase.IncrementDailyStat(ctx, g.stats, request.Chat.ID, handlersbase.StatChallengeStarted); err != nil {
		entry.WithField(logFieldError, err.Error()).Warn("failed to increment started challenge stat")
	}
	if err := bot.SendJoinRequestWebApp(ctx, g.bot, request.QueryID, webAppURL); err != nil {
		claimed, claimErr := g.store.BeginDMFallback(ctx, challenge.ChallengeID)
		if claimErr != nil {
			return stderrors.Join(fmt.Errorf("send web app challenge: %w", err), claimErr)
		}
		if claimed {
			challenge.Status = db.ChallengeStatusWebAppFallbackPending
			fallbackErr := g.processChallengeAction(ctx, challenge)
			return stderrors.Join(fmt.Errorf("send web app challenge: %w", err), fallbackErr)
		}
		return fmt.Errorf("send web app challenge: %w", err)
	}
	return nil
}

func (g *Gatekeeper) handleTestJoinCaptchaCommand(ctx context.Context, msg *api.Message, chat *api.Chat, user *api.User) error {
	if msg == nil || chat == nil || user == nil {
		return nil
	}
	if chat.Type != telegramChatTypePrivate {
		reply := api.NewMessage(chat.ID, "Run /"+testJoinCaptchaCommand+" in private chat with the bot.")
		reply.DisableNotification = true
		_, _ = bot.Send(ctx, g.bot, reply)
		return nil
	}

	webAppToken := uuid.New()
	webAppURL, err := g.joinCaptchaURL(webAppToken)
	if err != nil {
		reply := api.NewMessage(chat.ID, "Gatekeeper Web App public URL is not configured.")
		reply.DisableNotification = true
		_, _ = bot.Send(ctx, g.bot, reply)
		return nil
	}

	successUUID := uuid.New()
	language := g.webAppChallengeLanguage(ctx, chat.ID, user)
	settings := db.DefaultSettings(chat.ID)
	options, correctVariant := g.createWebAppCaptchaOptions(language, settings.GatekeeperCaptchaOptionsCount, successUUID)
	optionsJSON, err := encodeWebAppCaptchaOptions(language, options)
	if err != nil {
		return fmt.Errorf("marshal test captcha options: %w", err)
	}

	now := time.Now()
	challenge := &db.Challenge{
		CommChatID:         chat.ID,
		UserID:             user.ID,
		ChatID:             chat.ID,
		Status:             db.ChallengeStatusPending,
		SuccessUUID:        successUUID,
		WebAppToken:        webAppToken,
		JoinRequestQueryID: joinCaptchaTestQueryPrefix + uuid.New(),
		CaptchaPrompt:      correctVariant[1],
		CaptchaOptionsJSON: string(optionsJSON),
		UserLanguage:       strings.TrimSpace(user.LanguageCode),
		CreatedAt:          now,
		ExpiresAt:          now.Add(settings.GetChallengeTimeout()),
	}
	if _, err := g.store.CreateChallenge(ctx, challenge); err != nil {
		return err
	}

	reply := api.NewMessage(chat.ID, "Test join-request CAPTCHA. Open it in Telegram and try the choices.")
	markup := api.NewInlineKeyboardMarkup(api.NewInlineKeyboardRow(
		api.NewInlineKeyboardButtonWebApp("Open CAPTCHA", api.WebAppInfo{URL: webAppURL}),
	))
	reply.ReplyMarkup = markup
	reply.DisableNotification = true
	_, err = bot.Send(ctx, g.bot, reply)
	return err
}

func (g *Gatekeeper) createWebAppCaptchaOptions(lang string, optionsCount int, successUUID string) ([]webAppCaptchaOption, [2]string) {
	captchaIndex := g.createCaptchaIndex(lang)
	if len(captchaIndex) == 0 {
		captchaIndex = g.createCaptchaIndex("en")
	}
	if len(captchaIndex) == 0 {
		return []webAppCaptchaOption{{ID: successUUID, Symbol: "A"}}, [2]string{"A", captchaFallbackWord}
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
	return options, correctVariant
}

func (g *Gatekeeper) handleJoinCaptcha(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	challenge, ok := g.webAppChallengeFromRequest(w, r, true)
	if !ok {
		return
	}
	if time.Now().After(challenge.ExpiresAt) {
		copy := joinCaptchaCopyForLocale(joinCaptchaChallengeLocale(challenge))
		g.renderJoinCaptchaPage(w, http.StatusNotFound, joinCaptchaErrorPageData(copy, "404", copy.ExpiredPageMessage, copy.OpenFresh))
		return
	}
	locale, options, err := decodeWebAppCaptchaData(challenge.CaptchaOptionsJSON)
	if err != nil {
		copy := joinCaptchaCopyForLocale(locale)
		g.renderJoinCaptchaPage(w, http.StatusInternalServerError, joinCaptchaErrorPageData(copy, copy.UnavailableTitle, copy.UnavailableMessage, copy.TryAgainFromTelegram))
		return
	}
	if !challenge.WebAppOpenedAt.Valid {
		if err := g.store.MarkWebAppChallengeOpened(r.Context(), challenge.WebAppToken, time.Now()); err != nil {
			g.getLogEntry().WithField(logFieldError, err.Error()).Warn("failed to mark web app challenge opened")
		}
	}
	copy := joinCaptchaCopyForLocale(locale)
	pageOptions, payload, err := newJoinCaptchaPageChallenge(challenge.CaptchaPrompt, options)
	if err != nil {
		g.renderJoinCaptchaPage(w, http.StatusInternalServerError, joinCaptchaErrorPageData(copy, copy.UnavailableTitle, copy.UnavailableMessage, copy.TryAgainFromTelegram))
		return
	}
	labelsJSON, err := joinCaptchaLabelsJSON(copy)
	if err != nil {
		g.renderJoinCaptchaPage(w, http.StatusInternalServerError, joinCaptchaErrorPageData(copy, copy.UnavailableTitle, copy.UnavailableMessage, copy.TryAgainFromTelegram))
		return
	}
	before, after := splitJoinCaptchaPrompt(copy.PromptTemplate)
	g.renderJoinCaptchaPage(w, http.StatusOK, joinCaptchaPageData{
		Token:         challenge.WebAppToken,
		Kicker:        copy.Kicker,
		Title:         copy.Title,
		PromptBefore:  before,
		PromptAfter:   after,
		SecondsLabel:  copy.SecondsLabel,
		Waiting:       copy.Waiting,
		OptionLabel:   copy.OptionLabel,
		Options:       pageOptions,
		ChallengeJSON: payload,
		LabelsJSON:    labelsJSON,
	})
}

func (g *Gatekeeper) handleJoinCaptchaAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		copy := joinCaptchaCopyForRequest(r)
		writeJoinCaptchaJSON(w, http.StatusMethodNotAllowed, joinCaptchaAnswerResponse{Message: copy.MethodNotAllowed})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, joinCaptchaMaxRequestBodyBytes)
	if err := r.ParseForm(); err != nil {
		copy := joinCaptchaCopyForRequest(r)
		writeJoinCaptchaJSON(w, http.StatusBadRequest, joinCaptchaAnswerResponse{Message: copy.InvalidRequest})
		return
	}

	challenge, ok := g.webAppChallengeFromRequest(w, r, false)
	if !ok {
		return
	}
	copy := joinCaptchaCopyForLocale(joinCaptchaChallengeLocale(challenge))
	initDataRaw := r.Form.Get("init_data")
	valid, err := api.ValidateWebAppData(g.bot.Token, initDataRaw)
	if err != nil || !valid {
		writeJoinCaptchaJSON(w, http.StatusUnauthorized, joinCaptchaAnswerResponse{Message: copy.TelegramCheckFailed})
		return
	}
	initData, err := parseWebAppInitData(initDataRaw)
	if err != nil {
		writeJoinCaptchaJSON(w, http.StatusUnauthorized, joinCaptchaAnswerResponse{Message: copy.TelegramCheckFailed})
		return
	}
	if initData.AuthDate == 0 || time.Since(time.Unix(initData.AuthDate, 0)) > joinCaptchaInitDataTTL {
		writeJoinCaptchaJSON(w, http.StatusUnauthorized, joinCaptchaAnswerResponse{Message: copy.TelegramCheckFailed})
		return
	}
	if initData.UserID != challenge.UserID || (!isTestWebAppChallenge(challenge) && initData.QueryID != challenge.JoinRequestQueryID) {
		writeJoinCaptchaJSON(w, http.StatusForbidden, joinCaptchaAnswerResponse{Message: copy.OtherRequest})
		return
	}
	if time.Now().After(challenge.ExpiresAt) {
		if err := g.declineWebAppChallenge(r.Context(), challenge); err != nil {
			g.getLogEntry().WithField(logFieldError, err.Error()).Error("failed to decline expired web app challenge")
		}
		writeJoinCaptchaJSON(w, http.StatusGone, joinCaptchaAnswerResponse{OK: false, Done: true, Message: copy.ExpiredBlocked})
		return
	}

	if r.Form.Get("choice") != challenge.SuccessUUID {
		attempts, status, updated, err := g.store.RecordWrongAttempt(r.Context(), challenge.ChallengeID, maxChallengeAttempts)
		if err != nil {
			writeJoinCaptchaJSON(w, http.StatusInternalServerError, joinCaptchaAnswerResponse{Message: copy.CouldNotSaveAnswer})
			return
		}
		if !updated {
			writeJoinCaptchaJSON(w, http.StatusConflict, joinCaptchaAnswerResponse{OK: false, Done: true, Message: copy.ExpiredBlocked})
			return
		}
		challenge.Attempts = attempts
		challenge.Status = status
		if status == db.ChallengeStatusRejectPending {
			if err := g.processChallengeAction(r.Context(), challenge); err != nil {
				g.getLogEntry().WithField(logFieldError, err.Error()).Error("failed to decline failed web app challenge")
			}
			writeJoinCaptchaJSON(w, http.StatusForbidden, joinCaptchaAnswerResponse{OK: false, Done: true, Message: copy.TooManyBlocked})
			return
		}
		writeJoinCaptchaJSON(w, http.StatusOK, joinCaptchaAnswerResponse{OK: false, Done: false, Message: copy.WrongOption})
		return
	}

	if isTestWebAppChallenge(challenge) {
		deleted, err := g.store.DeleteChallengeInstance(r.Context(), challenge.ChallengeID, db.ChallengeStatusPending)
		if err != nil || !deleted {
			writeJoinCaptchaJSON(w, http.StatusInternalServerError, joinCaptchaAnswerResponse{Message: copy.CouldNotSaveResult})
			return
		}
		writeJoinCaptchaJSON(w, http.StatusOK, joinCaptchaAnswerResponse{OK: true, Done: true, Message: copy.TestDone})
		return
	}

	if g.banChecker != nil && g.banChecker.IsKnownBanned(challenge.UserID) {
		if err := g.declineWebAppChallenge(r.Context(), challenge); err != nil {
			g.getLogEntry().WithField(logFieldError, err.Error()).Error("failed to decline banned web app challenge")
		}
		writeJoinCaptchaJSON(w, http.StatusForbidden, joinCaptchaAnswerResponse{OK: false, Done: true, Message: copy.Blocked})
		return
	}

	claimed, err := g.store.ClaimForApproval(r.Context(), challenge.ChallengeID)
	if err != nil {
		g.getLogEntry().WithField(logFieldError, err.Error()).Error("failed to claim join request challenge for approval")
		writeJoinCaptchaJSON(w, http.StatusInternalServerError, joinCaptchaAnswerResponse{Message: copy.CouldNotSaveResult})
		return
	}
	if !claimed {
		writeJoinCaptchaJSON(w, http.StatusConflict, joinCaptchaAnswerResponse{OK: false, Done: true, Message: copy.ExpiredBlocked})
		return
	}
	challenge.Status = db.ChallengeStatusApproveQueryPending
	if err := g.processChallengeAction(r.Context(), challenge); err != nil {
		g.getLogEntry().WithField(logFieldError, err.Error()).Error("failed to approve join request query; durable retry scheduled")
		writeJoinCaptchaJSON(w, http.StatusBadGateway, joinCaptchaAnswerResponse{Message: copy.CouldNotApprove})
		return
	}
	writeJoinCaptchaJSON(w, http.StatusOK, joinCaptchaAnswerResponse{OK: true, Done: true, Message: copy.Done})
}

func (g *Gatekeeper) renderJoinCaptchaPage(w http.ResponseWriter, status int, data joinCaptchaPageData) {
	nonce, err := newJoinCaptchaCSPNonce()
	if err != nil {
		http.Error(w, "challenge is unavailable", http.StatusInternalServerError)
		return
	}

	data.CSPNonce = nonce
	if data.Kicker == "" || data.Title == "" {
		copy := joinCaptchaCopies["en"]
		if data.Kicker == "" {
			data.Kicker = copy.Kicker
		}
		if data.Title == "" {
			data.Title = copy.Title
		}
	}
	if data.ChallengeJSON == "" {
		data.ChallengeJSON = template.JS("{}")
	}
	if data.LabelsJSON == "" {
		labelsJSON, err := joinCaptchaLabelsJSON(joinCaptchaCopies["en"])
		if err != nil {
			http.Error(w, "challenge is unavailable", http.StatusInternalServerError)
			return
		}
		data.LabelsJSON = labelsJSON
	}
	setJoinCaptchaSecurityHeaders(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", joinCaptchaPageCSP(nonce))
	w.WriteHeader(status)
	if err := joinCaptchaTemplate.Execute(w, data); err != nil {
		g.getLogEntry().WithField(logFieldError, err.Error()).Error("failed to render join captcha")
	}
}

func (g *Gatekeeper) webAppChallengeFromRequest(w http.ResponseWriter, r *http.Request, renderPage bool) (*db.Challenge, bool) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		token = strings.TrimSpace(r.Form.Get("token"))
	}
	if token == "" {
		if renderPage {
			copy := joinCaptchaCopyForRequest(r)
			g.renderJoinCaptchaPage(w, http.StatusNotFound, joinCaptchaErrorPageData(copy, "404", copy.MissingTokenMessage, copy.OpenFresh))
			return nil, false
		}
		copy := joinCaptchaCopyForRequest(r)
		writeJoinCaptchaJSON(w, http.StatusNotFound, joinCaptchaAnswerResponse{Message: copy.ChallengeNotFound})
		return nil, false
	}
	challenge, err := g.store.GetChallengeByWebAppToken(r.Context(), token)
	if err != nil {
		g.getLogEntry().WithField(logFieldError, err.Error()).Error("failed to load web app challenge")
		if renderPage {
			copy := joinCaptchaCopyForRequest(r)
			g.renderJoinCaptchaPage(w, http.StatusInternalServerError, joinCaptchaErrorPageData(copy, copy.UnavailableTitle, copy.UnavailableMessage, copy.TryAgainFromTelegram))
			return nil, false
		}
		copy := joinCaptchaCopyForRequest(r)
		writeJoinCaptchaJSON(w, http.StatusInternalServerError, joinCaptchaAnswerResponse{Message: copy.ChallengeUnavailable})
		return nil, false
	}
	if challenge == nil || challenge.Status != db.ChallengeStatusPending {
		if renderPage {
			copy := joinCaptchaCopyForRequest(r)
			g.renderJoinCaptchaPage(w, http.StatusNotFound, joinCaptchaErrorPageData(copy, "404", copy.MissingChallengeMessage, copy.OpenFresh))
			return nil, false
		}
		copy := joinCaptchaCopyForRequest(r)
		writeJoinCaptchaJSON(w, http.StatusNotFound, joinCaptchaAnswerResponse{Message: copy.ChallengeNotFound})
		return nil, false
	}
	return challenge, true
}

func (g *Gatekeeper) declineWebAppChallenge(ctx context.Context, challenge *db.Challenge) error {
	if isTestWebAppChallenge(challenge) {
		_, err := g.store.DeleteChallengeInstance(ctx, challenge.ChallengeID, challenge.Status)
		return err
	}
	if challenge.Status != db.ChallengeStatusRejectPending {
		claimed, err := g.store.CompleteExternalAction(ctx, challenge.ChallengeID, challenge.Status, db.ChallengeStatusRejectPending, time.Time{})
		if err != nil || !claimed {
			return err
		}
		challenge.Status = db.ChallengeStatusRejectPending
	}
	return g.processChallengeAction(ctx, challenge)
}

func isTestWebAppChallenge(challenge *db.Challenge) bool {
	return challenge != nil && strings.HasPrefix(challenge.JoinRequestQueryID, joinCaptchaTestQueryPrefix)
}

func (g *Gatekeeper) webAppChallengeLanguage(ctx context.Context, chatID int64, user *api.User) string {
	if user != nil && strings.TrimSpace(user.LanguageCode) != "" {
		return normalizeJoinCaptchaLocale(user.LanguageCode)
	}
	if g != nil && g.s != nil {
		return normalizeJoinCaptchaLocale(g.s.GetLanguage(ctx, chatID, user))
	}
	return "en"
}

func encodeWebAppCaptchaOptions(locale string, options []webAppCaptchaOption) (string, error) {
	data, err := json.Marshal(webAppCaptchaData{
		Locale:  normalizeJoinCaptchaLocale(locale),
		Options: options,
	})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeWebAppCaptchaData(raw string) (string, []webAppCaptchaOption, error) {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "[") {
		var options []webAppCaptchaOption
		if err := json.Unmarshal([]byte(trimmed), &options); err != nil {
			return "", nil, fmt.Errorf("decode captcha options: %w", err)
		}
		if len(options) == 0 {
			return "", nil, errors.New("captcha options are empty")
		}
		return "en", options, nil
	}

	var data webAppCaptchaData
	if err := json.Unmarshal([]byte(trimmed), &data); err != nil {
		return "", nil, fmt.Errorf("decode captcha data: %w", err)
	}
	if len(data.Options) == 0 {
		return "", nil, errors.New("captcha options are empty")
	}
	return normalizeJoinCaptchaLocale(data.Locale), data.Options, nil
}

func joinCaptchaChallengeLocale(challenge *db.Challenge) string {
	if challenge == nil {
		return "en"
	}
	locale, _, err := decodeWebAppCaptchaData(challenge.CaptchaOptionsJSON)
	if err != nil {
		return "en"
	}
	return locale
}

func joinCaptchaCopyForRequest(r *http.Request) joinCaptchaCopy {
	if r == nil {
		return joinCaptchaCopies["en"]
	}
	accepted := strings.Split(r.Header.Get("Accept-Language"), ",")
	for _, item := range accepted {
		locale := normalizeJoinCaptchaLocale(strings.TrimSpace(strings.Split(item, ";")[0]))
		if copy, ok := joinCaptchaCopies[locale]; ok {
			return copy
		}
	}
	return joinCaptchaCopies["en"]
}

func joinCaptchaCopyForLocale(locale string) joinCaptchaCopy {
	if copy, ok := joinCaptchaCopies[normalizeJoinCaptchaLocale(locale)]; ok {
		return copy
	}
	return joinCaptchaCopies["en"]
}

func normalizeJoinCaptchaLocale(locale string) string {
	normalized := strings.ToLower(strings.TrimSpace(locale))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	normalized, _, _ = strings.Cut(normalized, "-")
	if normalized == "" {
		return "en"
	}
	return normalized
}

func splitJoinCaptchaPrompt(prompt string) (string, string) {
	before, after, ok := strings.Cut(prompt, "{target}")
	if !ok {
		return prompt + " ", ""
	}
	return before, after
}

func joinCaptchaErrorPageData(copy joinCaptchaCopy, title, message, hint string) joinCaptchaPageData {
	return joinCaptchaPageData{
		Kicker:  copy.Kicker,
		Title:   title,
		Message: message,
		Hint:    hint,
		State:   "blocked",
	}
}

func joinCaptchaClientLabelsForCopy(copy joinCaptchaCopy) joinCaptchaClientLabels {
	return joinCaptchaClientLabels{
		MakeChoiceNow:      copy.MakeChoiceNow,
		Checking:           copy.Checking,
		PassedTitle:        copy.PassedTitle,
		BlockedTitle:       copy.BlockedTitle,
		Done:               copy.Done,
		Blocked:            copy.Blocked,
		TryAnother:         copy.TryAnother,
		VerificationFailed: copy.VerificationFailed,
	}
}

func joinCaptchaLabelsJSON(copy joinCaptchaCopy) (template.JS, error) {
	labels, err := json.Marshal(joinCaptchaClientLabelsForCopy(copy))
	if err != nil {
		return "", fmt.Errorf("marshal captcha labels: %w", err)
	}
	return template.JS(labels), nil
}

func newJoinCaptchaPageChallenge(prompt string, options []webAppCaptchaOption) ([]joinCaptchaPageOption, template.JS, error) {
	mathrand.Shuffle(len(options), func(i, j int) {
		options[i], options[j] = options[j], options[i]
	})
	pageOptions := make([]joinCaptchaPageOption, 0, len(options))
	payload := joinCaptchaClientPayload{
		Options: make([]joinCaptchaClientPayloadOption, 0, len(options)),
	}

	obfuscatedPrompt, err := obfuscateJoinCaptchaText(prompt)
	if err != nil {
		return nil, "", err
	}
	payload.Prompt = obfuscatedPrompt

	for index, option := range options {
		obfuscatedSymbol, err := obfuscateJoinCaptchaText(option.Symbol)
		if err != nil {
			return nil, "", err
		}
		pageOptions = append(pageOptions, joinCaptchaPageOption{ID: option.ID, Number: index + 1})
		payload.Options = append(payload.Options, joinCaptchaClientPayloadOption{ID: option.ID, Text: obfuscatedSymbol})
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("marshal captcha payload: %w", err)
	}
	return pageOptions, template.JS(payloadJSON), nil
}

func obfuscateJoinCaptchaText(text string) (joinCaptchaObfuscatedText, error) {
	keyBytes := make([]byte, joinCaptchaObfuscationKeyBytes)
	if _, err := rand.Read(keyBytes); err != nil {
		return joinCaptchaObfuscatedText{}, fmt.Errorf("read captcha obfuscation key: %w", err)
	}

	source := []byte(text)
	key := make([]int, len(keyBytes))
	data := make([]int, len(source))
	for i, value := range keyBytes {
		key[i] = int(value)
	}
	for i, value := range source {
		data[i] = int(value ^ keyBytes[i%len(keyBytes)])
	}
	return joinCaptchaObfuscatedText{Key: key, Data: data}, nil
}
