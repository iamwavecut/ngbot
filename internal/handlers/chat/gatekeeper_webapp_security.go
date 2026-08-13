package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

type joinCaptchaRateWindowState struct {
	count    int
	lastSeen time.Time
	resetAt  time.Time
}

type joinCaptchaRateLimiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	clients map[string]joinCaptchaRateWindowState
}

type joinCaptchaStatusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *joinCaptchaStatusRecorder) WriteHeader(status int) {
	if r.status != 0 {
		return
	}
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *joinCaptchaStatusRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(data)
}

func newJoinCaptchaRateLimiter(limit int, window time.Duration, now func() time.Time) *joinCaptchaRateLimiter {
	return &joinCaptchaRateLimiter{
		limit:   limit,
		window:  window,
		now:     now,
		clients: make(map[string]joinCaptchaRateWindowState),
	}
}

func (l *joinCaptchaRateLimiter) allow(client string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	state, ok := l.clients[client]
	if ok && !now.Before(state.resetAt) {
		delete(l.clients, client)
		ok = false
	}
	if !ok && len(l.clients) >= joinCaptchaRateClientLimit {
		for key, candidate := range l.clients {
			if !now.Before(candidate.resetAt) {
				delete(l.clients, key)
			}
		}
		if len(l.clients) >= joinCaptchaRateClientLimit {
			var oldestClient string
			var oldestSeen time.Time
			for key, candidate := range l.clients {
				if oldestClient == "" || candidate.lastSeen.Before(oldestSeen) {
					oldestClient = key
					oldestSeen = candidate.lastSeen
				}
			}
			delete(l.clients, oldestClient)
		}
	}
	if !ok {
		l.clients[client] = joinCaptchaRateWindowState{count: 1, lastSeen: now, resetAt: now.Add(l.window)}
		return true
	}
	if state.count >= l.limit {
		return false
	}
	state.count++
	state.lastSeen = now
	l.clients[client] = state
	return true
}

func joinCaptchaRateLimitMiddleware(limiter *joinCaptchaRateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isJoinCaptchaHealthPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if !limiter.allow(joinCaptchaClientAddress(r)) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func joinCaptchaAdmissionMiddleware(limit int, next http.Handler) http.Handler {
	admission := make(chan struct{}, limit)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isJoinCaptchaHealthPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		select {
		case admission <- struct{}{}:
			defer func() { <-admission }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "service busy", http.StatusServiceUnavailable)
		}
	})
}

func joinCaptchaTelemetryMiddleware(logger *log.Entry, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &joinCaptchaStatusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		if recorder.status == 0 {
			recorder.status = http.StatusOK
		}
		entry := logger.WithFields(log.Fields{
			"duration_ms":  time.Since(started).Milliseconds(),
			"http_method":  r.Method,
			"http_route":   joinCaptchaTelemetryRoute(r.URL.Path),
			logFieldStatus: recorder.status,
		})
		if recorder.status >= http.StatusInternalServerError {
			entry.Warn("gatekeeper web app request")
			return
		}
		entry.Info("gatekeeper web app request")
	})
}

func joinCaptchaTelemetryRoute(path string) string {
	switch path {
	case joinCaptchaPath:
		return "join_captcha"
	case joinCaptchaAnswerPath:
		return "join_captcha_answer"
	case joinCaptchaReadyPath:
		return "join_captcha_ready"
	case joinCaptchaStatusPath:
		return "join_captcha_status"
	case joinCaptchaRobotsPath:
		return "robots"
	case joinCaptchaSitemapPath:
		return "sitemap"
	case joinCaptchaLivePath:
		return "liveness"
	case joinCaptchaReadyHealthPath:
		return "readiness"
	default:
		return "other"
	}
}

func joinCaptchaClientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remoteIP := net.ParseIP(strings.TrimSpace(host))
	if remoteIP != nil && remoteIP.IsLoopback() {
		forwarded, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ",")
		if forwardedIP := net.ParseIP(strings.TrimSpace(forwarded)); forwardedIP != nil {
			return forwardedIP.String()
		}
	}
	if remoteIP != nil {
		return remoteIP.String()
	}
	return "unknown"
}

func isJoinCaptchaHealthPath(path string) bool {
	return path == joinCaptchaLivePath || path == joinCaptchaReadyHealthPath
}

func handleJoinCaptchaLive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", strings.Join([]string{http.MethodGet, http.MethodHead}, ", "))
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
}

func (g *Gatekeeper) handleJoinCaptchaReadiness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", strings.Join([]string{http.MethodGet, http.MethodHead}, ", "))
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !g.webAppReady.Load() || g.runtimeReady == nil || !g.runtimeReady() {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func parseWebAppInitData(raw string) (webAppInitData, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return webAppInitData{}, err
	}
	var user struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal([]byte(values.Get(logFieldUser)), &user); err != nil {
		return webAppInitData{}, err
	}
	authDate, _ := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	return webAppInitData{
		QueryID:  values.Get("query_id"),
		UserID:   user.ID,
		Username: user.Username,
		AuthDate: authDate,
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
		"frame-ancestors https://web.telegram.org",
		"img-src data:",
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
