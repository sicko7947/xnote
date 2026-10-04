package xnote

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestMultipartAndEmptySpeech(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio.mp3")
	_ = os.WriteFile(path, []byte("fixture audio"), 0600)
	t.Setenv("XNOTE_TEST_KEY", "test-only-key")
	for _, test := range []struct {
		body  string
		fails bool
	}{{`{"text":"meeting words"}`, false}, {`{"text":""}`, false}, {`{"other":"value"}`, true}} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer test-only-key" {
				t.Error("missing key")
			}
			f, _, e := r.FormFile("file")
			if e != nil {
				t.Error(e)
				return
			}
			defer f.Close()
			b, _ := io.ReadAll(f)
			if string(b) != "fixture audio" {
				t.Error("bad upload")
			}
			if r.FormValue("model") != "test-model" {
				t.Error("model")
			}
			_, _ = io.WriteString(w, test.body)
		}))
		text, e := transcribeWithClient(context.Background(), Config{Provider: "api", APIURL: server.URL, APIKeyEnv: "XNOTE_TEST_KEY", Model: "test-model"}, path, server.Client())
		server.Close()
		if (e != nil) != test.fails {
			t.Fatal(text, e)
		}
	}
}

func TestSpeakerSegmentsAndWireOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio.mp3")
	_ = os.WriteFile(path, []byte("fixture"), 0600)
	t.Setenv("XNOTE_TEST_KEY", "test-key")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		if r.FormValue("response_format") != "diarized_json" || r.FormValue("chunking_strategy") != "auto" {
			t.Error("missing diarization options")
		}
		_, _ = io.WriteString(w, `{"text":"hello","segments":[{"start":1.2,"end":3.4,"speaker":"A","text":"hello"}]}`)
	}))
	defer server.Close()
	result, e := requestTranscription(context.Background(), Config{Provider: "api", APIURL: server.URL, APIKeyEnv: "XNOTE_TEST_KEY", Model: "gpt-4o-transcribe-diarize"}, path, server.Client())
	if e != nil || len(result.Segments) != 1 {
		t.Fatal(result, e)
	}
	segment := result.Segments[0]
	if segment.Speaker != "A" || segment.Start != 1.2 || segment.Timing != "provider" {
		t.Fatal(segment)
	}
}
func TestQuietChunkBoundaryAndWAV(t *testing.T) {
	// 20 seconds of loud stereo PCM with one silent 100 ms window near the end.
	rate := 100
	pcm := make([]byte, rate*4*20)
	for i := range pcm {
		pcm[i] = 0x20
	}
	quiet := len(pcm) - rate*4*2
	for i := quiet; i < quiet+rate*4/10; i++ {
		pcm[i] = 0
	}
	cut := quietCut(pcm, rate*4)
	if cut != quiet+rate*4/10 {
		t.Fatal("did not choose silence", cut)
	}
	path := filepath.Join(t.TempDir(), "chunk.wav")
	if e := writeWAV(path, pcm, rate); e != nil {
		t.Fatal(e)
	}
	data, _ := os.ReadFile(path)
	if string(data[:4]) != "RIFF" || len(data) != len(pcm)+44 {
		t.Fatal("invalid WAV")
	}
}
