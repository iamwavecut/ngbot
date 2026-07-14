package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func parseWebAppInitData(raw string) (webAppInitData, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return webAppInitData{}, err
	}
	var user struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(values.Get(logFieldUser)), &user); err != nil {
		return webAppInitData{}, err
	}
	authDate, _ := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	return webAppInitData{
		QueryID:  values.Get("query_id"),
		UserID:   user.ID,
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
