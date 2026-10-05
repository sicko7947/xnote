package xnote

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestElevenLabsWireAndSpeakerTiming(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chunk.wav")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ELEVENLABS_API_KEY", "test-only-key")
	for _, language := range []string{"auto", "", "zh"} {
		t.Run("language-"+language, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("xi-api-key") != "test-only-key" || r.Header.Get("Authorization") != "" {
					t.Error("wrong authentication headers")
				}
				f, header, err := r.FormFile("file")
				if err != nil {
					t.Error(err)
					return
				}
				defer f.Close()
				body, _ := io.ReadAll(f)
				if string(body) != "fixture" || header.Filename != "chunk.wav" {
					t.Error("invalid file upload")
				}
				for key, value := range map[string]string{"model_id": "scribe_v2", "diarize": "true", "timestamps_granularity": "word", "tag_audio_events": "false"} {
					if r.FormValue(key) != value {
						t.Errorf("%s = %q", key, r.FormValue(key))
					}
				}
				wantLanguage := language
				if wantLanguage == "auto" {
					wantLanguage = ""
				}
				if r.FormValue("language_code") != wantLanguage {
					t.Error("language_code")
				}
				for _, key := range []string{"language", "model", "response_format"} {
					if _, exists := r.MultipartForm.Value[key]; exists {
						t.Errorf("unexpected %s", key)
					}
				}
				io.WriteString(w, `{"text":"Hello world. 你好","words":[{"text":"Hello","type":"word","start":0.2,"end":0.5,"speaker_id":"speaker_0"},{"text":" ","type":"spacing","start":0.5,"end":0.6},{"text":"world.","type":"word","start":0.6,"end":1,"speaker_id":"speaker_0"},{"text":"你好","type":"word","start":1.2,"end":2,"speaker_id":"speaker_1"}]}`)
			}))
			defer server.Close()
			result, err := requestTranscription(context.Background(), Config{Provider: "elevenlabs", APIURL: server.URL, Model: "whisper-1", APIKeyEnv: "XNOTE_API_KEY", Language: language}, path, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if result.Text != "Hello world. 你好" || len(result.Segments) != 2 {
				t.Fatal(result)
			}
			a, b := result.Segments[0], result.Segments[1]
			if a.Text != "Hello world." || a.Start != .2 || a.End != 1 || a.Speaker != "speaker_0" || a.Timing != "provider" {
				t.Fatal(a)
			}
			if b.Text != "你好" || b.Start != 1.2 || b.End != 2 || b.Speaker != "speaker_1" {
				t.Fatal(b)
			}
		})
	}
}

func TestElevenLabsValidationAndErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio.mp3")
	os.WriteFile(path, []byte("fixture"), 0600)
	t.Setenv("ELEVENLABS_API_KEY", "secret-fixture")
	for _, tc := range []struct {
		name, body string
		status     int
		wantError  bool
	}{
		{"empty", `{"text":"","words":[]}`, 200, false},
		{"missing", `{"other":"value"}`, 200, true},
		{"words only", `{"words":[{"text":"hello","start":1,"end":2,"type":"word"}]}`, 200, false},
		{"missing timestamps", `{"text":"hello","words":[{"text":"hello","type":"word"}]}`, 200, true},
		{"negative timestamps", `{"text":"hello","words":[{"text":"hello","start":-1,"end":2,"type":"word"}]}`, 200, true},
		{"reversed timestamps", `{"text":"hello","words":[{"text":"hello","start":3,"end":2,"type":"word"}]}`, 200, true},
		{"auth", `sensitive transcript secret-fixture`, 401, true},
		{"limited", `sensitive transcript secret-fixture`, 429, true},
		{"malformed", `sensitive transcript secret-fixture`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) }))
			defer server.Close()
			_, err := requestTranscription(context.Background(), Config{Provider: "elevenlabs", APIURL: server.URL}, path, server.Client())
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v", err)
			}
			if err != nil && (strings.Contains(err.Error(), "secret-fixture") || strings.Contains(err.Error(), "sensitive transcript")) {
				t.Fatal("response leaked")
			}
		})
	}
}

func TestElevenLabsDefaultsAndReadiness(t *testing.T) {
	c := effectiveTranscriptionConfig(Config{Provider: "elevenlabs", Model: "whisper-1", APIKeyEnv: "XNOTE_API_KEY"})
	if c.APIURL != "https://api.elevenlabs.io/v1/speech-to-text" || c.Model != "scribe_v2" || c.APIKeyEnv != "ELEVENLABS_API_KEY" {
		t.Fatal(c)
	}
	custom := Config{Provider: "elevenlabs", APIURL: "https://example.test/stt", Model: "scribe_v1", APIKeyEnv: "CUSTOM_SCRIBE_KEY"}
	if effectiveTranscriptionConfig(custom) != custom {
		t.Fatal("custom config overwritten")
	}
	t.Setenv("ELEVENLABS_API_KEY", "")
	if TranscriptionReady(context.Background(), c) == nil {
		t.Fatal("missing key was ready")
	}
	t.Setenv("ELEVENLABS_API_KEY", "test-only")
	if err := TranscriptionReady(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	c.APIURL = "http://example.test/stt"
	if TranscriptionReady(context.Background(), c) == nil {
		t.Fatal("insecure endpoint was ready")
	}
}

func TestElevenLabsConfigAndDoctor(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ELEVENLABS_API_KEY", "fixture-secret")
	result := Doctor(s)
	if result["provider"] != "doway" || result["ready"] != false {
		t.Fatal(result)
	}
	if _, ok := result["fallback_provider"]; ok {
		t.Fatal("doctor exposed an automatic fallback", result)
	}
	c := s.Config()
	c.Provider = "elevenlabs"
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	result = Doctor(s)
	if result["api_key_env"] != "ELEVENLABS_API_KEY" || result["api_model"] != "scribe_v2" || result["ready"] != true {
		t.Fatal(result)
	}
}
