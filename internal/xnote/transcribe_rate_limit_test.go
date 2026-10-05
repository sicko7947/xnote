package xnote

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTranscriptionRateLimitIsAnExplicitSingleRequestRejection(t *testing.T) {
	t.Setenv("ELEVENLABS_API_KEY", "test-only-key")
	path := filepath.Join(t.TempDir(), "fixture.mp3")
	if err := os.WriteFile(path, []byte("test fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Retry-After", "17")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "private-provider-response test-only-key")
			}))
			defer server.Close()
			_, err := requestTranscription(context.Background(), Config{Provider: "elevenlabs", APIURL: server.URL}, path, server.Client())
			var limited *TranscriptionRateLimitError
			if got := errors.As(err, &limited); got != (status == http.StatusTooManyRequests) {
				t.Fatalf("status %d incorrectly classified: %v", status, err)
			}
			if limited != nil && limited.RetryAfter != 17*time.Second {
				t.Fatalf("Retry-After lost: %v", limited.RetryAfter)
			}
			if err == nil || strings.Contains(err.Error(), "private-provider-response") || strings.Contains(err.Error(), "test-only-key") {
				t.Fatal("provider failure missing or private response exposed")
			}
			if calls.Load() != 1 {
				t.Fatalf("HTTP layer replayed request %d times", calls.Load())
			}
		})
	}
}

func TestTranscriptionRetryAfterHonorsProviderDelay(t *testing.T) {
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{
		{"", 30 * time.Second}, {"invalid", 30 * time.Second}, {"-10", 30 * time.Second},
		{"0", time.Second}, {"25", 25 * time.Second}, {"900", 15 * time.Minute},
		{now.Add(20 * time.Minute).Format(http.TimeFormat), 20 * time.Minute},
		{now.Add(-time.Hour).Format(http.TimeFormat), 30 * time.Second},
	} {
		if got := transcriptionRetryAfter(tc.header, now); got != tc.want {
			t.Errorf("header %q: %v, want %v", tc.header, got, tc.want)
		}
	}
}
