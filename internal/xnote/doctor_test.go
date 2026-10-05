package xnote

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestDOWAYReadinessChecksLibrarySessionLocally(t *testing.T) {
	_, keyErr := cloudSigningKey(appProfiles)
	for _, tc := range []struct {
		name    string
		session string
		valid   bool
	}{
		{name: "missing"},
		{name: "invalid JSON", session: `not-json`},
		{name: "empty token", session: `{"playerId":123,"token":""}`},
		{name: "invalid player", session: `{"playerId":0,"token":"fixture-token"}`},
		{name: "stored session", session: `{"playerId":123,"token":"fixture-token"}`, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			c := s.Config()
			c.Language, c.DOWAYPublicUpload = "en", true
			if err := s.SaveConfig(c); err != nil {
				t.Fatal(err)
			}
			if tc.session != "" {
				if err := os.WriteFile(s.path(".work/doway-session.json"), []byte(tc.session), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ready := s.TranscriptionReady(context.Background(), s.Config())
			wantReady := keyErr == nil && tc.valid
			if (ready == nil) != wantReady {
				t.Fatalf("readiness = %v, want ready %v", ready, wantReady)
			}
			result := Doctor(s)
			if result["ready"] != wantReady || result["doway_session_available"] != tc.valid || result["doway_signing_key_available"] != (keyErr == nil) {
				t.Fatalf("doctor misreported local prerequisites: %+v", result)
			}
			if !strings.Contains(result["readiness_check"].(string), "local prerequisites only") {
				t.Fatal("doctor implied live authentication or account credits were verified")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := s.TranscriptionReady(ctx, s.Config()); !errors.Is(err, context.Canceled) {
				t.Fatalf("preflight did not honor cancellation: %v", err)
			}
		})
	}
}

func TestDOWAYReadinessExplainsExplicitLanguage(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, language := range []string{"", "auto", "unsupported"} {
		c := s.Config()
		c.Language = language
		err := s.TranscriptionReady(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "Settings > General > Audio language") || !strings.Contains(err.Error(), "xnote config transcription_language") {
			t.Fatalf("missing actionable DOWAY language error for %q: %v", language, err)
		}
	}
	t.Setenv("ELEVENLABS_API_KEY", "fixture-key")
	c := s.Config()
	c.Provider, c.Language = "elevenlabs", ""
	if err := s.TranscriptionReady(context.Background(), c); err != nil {
		t.Fatalf("ElevenLabs Auto was blocked by DOWAY language requirements: %v", err)
	}
}

func TestDOWAYReadinessRequiresPublicUploadOptIn(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := s.Config()
	if c.DOWAYPublicUpload {
		t.Fatal("public DOWAY uploads were enabled by default")
	}
	c.Language = "en"
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path(".work/doway-session.json"), []byte(`{"playerId":123,"token":"fixture-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	readyErr := s.TranscriptionReady(context.Background(), c)
	if readyErr == nil {
		t.Fatal("DOWAY was ready without explicit permission for publicly readable audio")
	}
	_, keyErr := cloudSigningKey(appProfiles)
	if keyErr == nil && !strings.Contains(readyErr.Error(), "Settings > Transcription provider") {
		t.Fatalf("opt-in error did not identify the setting: %v", readyErr)
	}
	result := Doctor(s)
	if result["ready"] != false || result["doway_public_upload_allowed"] != false {
		t.Fatalf("doctor omitted the public-upload prerequisite: %+v", result)
	}
}
