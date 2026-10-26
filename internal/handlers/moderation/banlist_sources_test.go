package handlers

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

const (
	testETag         = `"v1"`
	testLastModified = "Sun, 25 Oct 2026 23:21:58 GMT"
)

func TestFetchBanlistSourceNotModified(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != testETag || r.Header.Get("If-Modified-Since") != testLastModified {
			http.Error(w, "missing validators", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	t.Cleanup(server.Close)

	fetch, err := fetchBanlistSource(t.Context(), server.Client(), server.URL, testETag, testLastModified, 0)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !fetch.NotModified || fetch.IDs != nil {
		t.Fatalf("fetch = %+v, want not modified without IDs", fetch)
	}
}

func TestFetchBanlistSourceOmitsMissingValidators(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasETag := r.Header["If-None-Match"]
		_, hasLastModified := r.Header["If-Modified-Since"]
		if hasETag || hasLastModified {
			http.Error(w, "unexpected validators", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "1\n2\n")
	}))
	t.Cleanup(server.Close)

	fetch, err := fetchBanlistSource(t.Context(), server.Client(), server.URL, "", "", 0)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if fetch.NotModified || fetch.ETag != "" || fetch.LastModified != "" || !slices.Equal(fetch.IDs, []int64{1, 2}) {
		t.Fatalf("fetch = %+v, want IDs [1 2] without validators", fetch)
	}
}

func TestFetchBanlistSourceParsesNoisyFeed(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"v2"`)
		w.Header().Set("Last-Modified", "Mon, 26 Oct 2026 06:00:00 GMT")
		_, _ = io.WriteString(w, "id\r\n5\r\n\r\n-1001\r\n5\r\n3,extra\r\n")
	}))
	t.Cleanup(server.Close)

	fetch, err := fetchBanlistSource(t.Context(), server.Client(), server.URL, "", "", 0)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !slices.Equal(fetch.IDs, []int64{-1001, 3, 5}) || fetch.ETag != `"v2"` || fetch.LastModified != "Mon, 26 Oct 2026 06:00:00 GMT" {
		t.Fatalf("fetch = %+v, want normalized IDs and response validators", fetch)
	}
}

func TestFetchBanlistSourceFailsOnGarbage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "5\nnot-a-number\n")
	}))
	t.Cleanup(server.Close)

	fetch, err := fetchBanlistSource(t.Context(), server.Client(), server.URL, "", "", 0)
	if err == nil || fetch.IDs != nil {
		t.Fatalf("fetch = %+v, %v; want error without IDs", fetch, err)
	}
}

func TestFetchBanlistSourceFailsOnTruncatedBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = buffered.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 100\r\n\r\n1\n2\n3\n4\n5\n")
		_ = buffered.Flush()
	}))
	t.Cleanup(server.Close)

	fetch, err := fetchBanlistSource(t.Context(), server.Client(), server.URL, "", "", 0)
	if err == nil || fetch.IDs != nil {
		t.Fatalf("fetch = %+v, %v; want error without IDs", fetch, err)
	}
}

func TestFetchBanlistSourceSizesIDsFromHint(t *testing.T) {
	t.Parallel()

	const count = 10_000
	var body strings.Builder
	for id := range count {
		fmt.Fprintf(&body, "%d\n", id+1)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body.String())
	}))
	t.Cleanup(server.Close)

	fetch, err := fetchBanlistSource(t.Context(), server.Client(), server.URL, "", "", count)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(fetch.IDs) != count || cap(fetch.IDs) >= count+count/32 {
		t.Fatalf("IDs len/cap = %d/%d, want %d IDs within 3 %% spare capacity", len(fetch.IDs), cap(fetch.IDs), count)
	}
}
