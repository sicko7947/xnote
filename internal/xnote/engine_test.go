package xnote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSelectTranscriptionProvider(t *testing.T) {
	primaryErr := errors.New("primary unavailable")
	fallbackErr := errors.New("fallback unavailable")
	for _, tt := range []struct {
		name       string
		fallback   string
		primaryErr error
		backupErr  error
		want       string
		calls      []string
	}{
		{"primary ready", "elevenlabs", nil, nil, "api", []string{"api"}},
		{"fallback ready", "elevenlabs", primaryErr, nil, "elevenlabs", []string{"api", "elevenlabs"}},
		{"both unavailable", "elevenlabs", primaryErr, fallbackErr, "api", []string{"api", "elevenlabs"}},
		{"fallback disabled", "", primaryErr, nil, "api", []string{"api"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{Provider: "api", FallbackProvider: tt.fallback, Language: "zh", APIURL: "https://primary.invalid/transcriptions", Model: "primary-model", APIKeyEnv: "PRIMARY_KEY"}
			var calls []string
			got, err := selectTranscriptionProvider(context.Background(), c, func(_ context.Context, candidate Config) error {
				calls = append(calls, candidate.Provider)
				if candidate.Provider == c.Provider {
					return tt.primaryErr
				}
				return tt.backupErr
			})
			if got.Provider != tt.want || !reflect.DeepEqual(calls, tt.calls) {
				t.Fatalf("selected %q with readiness calls %v; want %q, %v", got.Provider, calls, tt.want, tt.calls)
			}
			if tt.primaryErr == nil || tt.fallback != "" && tt.backupErr == nil {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if got != c || err == nil || !strings.Contains(err.Error(), primaryErr.Error()) {
					t.Fatalf("unavailable provider lost original config/error: %+v, %v", got, err)
				}
				if tt.backupErr != nil && !strings.Contains(err.Error(), fallbackErr.Error()) {
					t.Fatalf("missing fallback failure: %v", err)
				}
				if tt.fallback == "" && !errors.Is(err, primaryErr) {
					t.Fatalf("primary error was replaced: %v", err)
				}
			}
		})
	}
}

func TestSelectTranscriptionProviderCancellationDoesNotFallback(t *testing.T) {
	for _, cancelBefore := range []bool{true, false} {
		t.Run(map[bool]string{true: "already canceled", false: "canceled during preflight"}[cancelBefore], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelBefore {
				cancel()
			}
			c := Config{Provider: "offline", FallbackProvider: "elevenlabs"}
			calls := 0
			got, err := selectTranscriptionProvider(ctx, c, func(ctx context.Context, candidate Config) error {
				calls++
				if candidate.Provider != c.Provider {
					t.Error("cancellation selected a fallback")
				}
				cancel()
				return ctx.Err()
			})
			if got != c || !errors.Is(err, context.Canceled) || calls > 1 {
				t.Fatalf("config=%+v error=%v readiness calls=%d", got, err, calls)
			}
		})
	}
}

func TestFallbackConfigResetsBuiltInAPISettings(t *testing.T) {
	for _, provider := range []string{"elevenlabs", "codex"} {
		t.Run(provider, func(t *testing.T) {
			c := Config{Provider: "api", FallbackProvider: provider, Language: "zh", Auto: true, OfflineModel: "local-model", APIURL: "https://primary.invalid", Model: "primary-model", APIKeyEnv: "PRIMARY_KEY"}
			want := c
			want.Provider = provider
			want.APIURL, want.Model, want.APIKeyEnv = "", "", ""
			got := fallbackConfig(c)
			if got != want {
				t.Fatalf("fallback inherited primary API settings or lost shared settings: %+v", got)
			}
		})
	}
}

func TestTranscriptionLoopUnavailablePreservesPendingRecord(t *testing.T) {
	// An empty PATH deterministically makes local transcription unavailable,
	// regardless of installed tools, credentials, or network connectivity.
	t.Setenv("PATH", t.TempDir())
	for _, state := range []string{"downloaded", "queued"} {
		t.Run(state, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			c := s.Config()
			c.Provider, c.FallbackProvider, c.Auto = "offline", "", state == "downloaded"
			if err := s.SaveConfig(c); err != nil {
				t.Fatal(err)
			}
			if err := s.Catalog(c.Serial, []DeviceFile{{"20260709220314", 10}}); err != nil {
				t.Fatal(err)
			}
			id := c.Serial + "-20260709220314"
			audio := filepath.Join(s.Root, "fixture.mp3")
			if err := os.WriteFile(audio, []byte("must not be submitted"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := s.Update(id, func(r *Record) { r.Audio, r.State, r.Error = audio, state, "" }); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			s.SetStatus(Status{Phase: "connected", Battery: -1})
			done := make(chan struct{})
			go func() { defer close(done); transcriptionLoop(ctx, s) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("transcription loop did not stop after cancellation")
				}
			})
			deadline := time.Now().Add(3 * time.Second)
			for {
				status := s.TranscriptionStatus()
				if status.Phase == "waiting" {
					if status.Provider != "offline" || status.Current != id || !strings.Contains(status.Detail, "whisper-cli") {
						t.Fatalf("waiting status did not explain local prerequisite: %+v", status)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("never reached waiting status: %+v", status)
				}
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("cancellation did not interrupt readiness retry delay")
			}
			r, err := s.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if r.State != state || r.Error != "" || r.Audio != audio || r.Transcript != "" || r.Provider != "" {
				t.Fatalf("unavailable local provider changed pending recording: %+v", r)
			}
		})
	}
}
