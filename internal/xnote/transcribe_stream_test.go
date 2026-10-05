package xnote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type transcriptionRoundTripFunc func(*http.Request) (*http.Response, error)

func (f transcriptionRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func silentMP3Fixture(t *testing.T, seconds int) string {
	t.Helper()
	// MPEG-2 layer III, 8 kbps, 16 kHz mono: 576 samples per 36-byte frame.
	// Zero side information/main data describes silence and needs no encoder.
	frame := make([]byte, 36)
	copy(frame, []byte{0xff, 0xf3, 0x18, 0xc0})
	frames := (seconds*16000 + 575) / 576
	path := filepath.Join(t.TempDir(), "recording.mp3")
	if e := os.WriteFile(path, bytes.Repeat(frame, frames), 0600); e != nil {
		t.Fatal(e)
	}
	duration, e := AudioDuration(path)
	if e != nil || duration < float64(seconds) || duration > float64(seconds)+.1 {
		t.Fatalf("invalid fixture duration: %g, %v", duration, e)
	}
	return path
}

func TestElevenLabsLongMP3UploadsOriginalOnce(t *testing.T) {
	path := silentMP3Fixture(t, 600)
	t.Setenv("ELEVENLABS_API_KEY", "test-only-key")
	original, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	wantHash := sha256.Sum256(original)
	calls := 0
	client := &http.Client{Transport: transcriptionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.ContentLength <= int64(len(original)) || r.ContentLength > int64(len(original))+4096 {
			t.Errorf("unexpected Content-Length: %d", r.ContentLength)
		}
		if r.GetBody != nil {
			t.Error("audio request must not be automatically replayable")
		}
		form, e := r.MultipartReader()
		if e != nil {
			t.Fatal(e)
		}
		files := 0
		for {
			part, e := form.NextPart()
			if e == io.EOF {
				break
			}
			if e != nil {
				t.Fatal(e)
			}
			if part.FormName() == "file" {
				files++
				hash := sha256.New()
				n, e := io.Copy(hash, part)
				if e != nil || n != int64(len(original)) || !bytes.Equal(hash.Sum(nil), wantHash[:]) || part.FileName() != "recording.mp3" {
					t.Error("upload changed or converted the original MP3")
				}
			}
			part.Close()
		}
		if files != 1 {
			t.Errorf("file parts: %d", files)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"text":"Hello. Again.","words":[{"text":"Hello.","type":"word","start":1,"end":2,"speaker_id":"speaker_0"},{"text":"Again.","type":"word","start":599,"end":600,"speaker_id":"speaker_0"}]}`))}, nil
	})}
	result, e := transcribeRecordingWithClient(context.Background(), Config{Provider: "elevenlabs"}, path, client)
	if e != nil {
		t.Fatal(e)
	}
	if calls != 1 || result.Text != "Hello. Again." || len(result.Segments) != 2 {
		t.Fatalf("requests=%d, segment count=%d", calls, len(result.Segments))
	}
	for _, segment := range result.Segments {
		if segment.Speaker != "speaker_0" || segment.Timing != "provider" {
			t.Error("speaker identity or timing source changed across the recording")
		}
	}
	if result.Segments[1].Start != 599 || result.Segments[1].End != 600 {
		t.Error("original timestamps were shifted")
	}
	if _, e = os.Stat(filepath.Join(filepath.Dir(path), ".transcription")); !errors.Is(e, os.ErrNotExist) {
		t.Error("whole-file transcription generated a chunk cache")
	}
}

func TestTranscriptionUploadStreamsAndClosesOnEveryOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recording.mp3")
	if e := os.WriteFile(path, make([]byte, 1<<20), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("ELEVENLABS_API_KEY", "test-only-key")
	for _, outcome := range []string{"success", "cancel", "connection failure", "HTTP failure", "invalid JSON"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var upload *transcriptionBody
			calls := 0
			client := &http.Client{Transport: transcriptionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				var ok bool
				upload, ok = r.Body.(*transcriptionBody)
				if !ok {
					t.Fatal("upload is not backed by an owned streaming file")
				}
				if offset, e := upload.file.Seek(0, io.SeekCurrent); e != nil || offset != 0 {
					t.Error("file was read into memory before the request started")
				}
				if _, e := io.CopyN(io.Discard, r.Body, 4096); e != nil {
					t.Fatal(e)
				}
				switch outcome {
				case "cancel":
					cancel()
					if n, e := r.Body.Read(make([]byte, 1)); n != 0 || !errors.Is(e, context.Canceled) {
						t.Error("upload continued reading after cancellation")
					}
					return nil, ctx.Err()
				case "connection failure":
					return nil, errors.New("test connection failure")
				case "HTTP failure":
					return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(""))}, nil
				case "invalid JSON":
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("invalid"))}, nil
				default:
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"text":""}`))}, nil
				}
			})}
			_, e := requestTranscription(ctx, Config{Provider: "elevenlabs"}, path, client)
			if (e == nil) != (outcome == "success") {
				t.Errorf("unexpected request result: %v", e)
			}
			if outcome == "cancel" && !errors.Is(e, context.Canceled) {
				t.Errorf("cancellation not propagated: %v", e)
			}
			if calls != 1 {
				t.Errorf("request was retried: %d", calls)
			}
			if _, e := upload.file.Stat(); !errors.Is(e, os.ErrClosed) {
				t.Error("upload file remained open after request returned")
			}
			if e := upload.Close(); e != nil {
				t.Errorf("closing request body again failed: %v", e)
			}
		})
	}
}

func TestElevenLabsRejectsOversizedRecordingsBeforeUpload(t *testing.T) {
	t.Setenv("ELEVENLABS_API_KEY", "test-only-key")
	client := &http.Client{Transport: transcriptionRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("unsupported recording was sent to the provider")
		return nil, errors.New("unexpected upload")
	})}
	t.Run("file size", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "oversized.mp3")
		f, e := os.Create(path)
		if e != nil {
			t.Fatal(e)
		}
		e = f.Truncate(elevenLabsMaxFileBytes + 1) // Sparse file, no 3 GB allocation.
		f.Close()
		if e != nil {
			t.Fatal(e)
		}
		_, e = transcribeRecordingWithClient(context.Background(), Config{Provider: "elevenlabs"}, path, client)
		if e == nil || !strings.Contains(e.Error(), "exceeds 3 GB") {
			t.Fatalf("wrong size rejection: %v", e)
		}
	})
	t.Run("duration", func(t *testing.T) {
		path := silentMP3Fixture(t, elevenLabsMaxSeconds+1)
		_, e := transcribeRecordingWithClient(context.Background(), Config{Provider: "elevenlabs"}, path, client)
		if e == nil || !strings.Contains(e.Error(), "exceeds 10 hours") {
			t.Fatalf("wrong duration rejection: %v", e)
		}
	})
}

func TestTranscriptionDoesNotFollowRedirects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio.mp3")
	if e := os.WriteFile(path, []byte("fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("ELEVENLABS_API_KEY", "test-only-key")
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path == "/redirect-target" {
					t.Error("followed redirect with provider credentials")
					io.WriteString(w, `{"text":""}`)
					return
				}
				io.Copy(io.Discard, r.Body)
				http.Redirect(w, r, "/redirect-target", status)
			}))
			defer server.Close()
			_, e := requestTranscription(context.Background(), Config{Provider: "elevenlabs", APIURL: server.URL}, path, server.Client())
			if e == nil || calls != 1 || !strings.Contains(e.Error(), fmt.Sprint(status)) {
				t.Errorf("redirect result: calls=%d, error=%v", calls, e)
			}
		})
	}
}

type generatedScribeWords struct {
	index, total int
	pending      string
	bytesRead    int
}

func (r *generatedScribeWords) Read(p []byte) (int, error) {
	if r.pending == "" {
		if r.index == r.total {
			return 0, io.EOF
		}
		prefix := ","
		if r.index == 0 {
			prefix = ""
		}
		start := float64(r.index) * .2
		r.pending = fmt.Sprintf(`%s{"text":"word","type":"word","start":%.1f,"end":%.1f,"speaker_id":"speaker_0"},{"text":" ","type":"spacing"}`, prefix, start, start+.2)
		r.index++
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	r.bytesRead += n
	return n, nil
}

func TestElevenLabsLongWordResponseExceedsOldLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio.mp3")
	if e := os.WriteFile(path, []byte("fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("ELEVENLABS_API_KEY", "test-only-key")
	words := &generatedScribeWords{total: 150000}
	client := &http.Client{Transport: transcriptionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := io.MultiReader(strings.NewReader(`{"text":"fixture long transcript","words":[`), words, strings.NewReader(`]}`))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(body)}, nil
	})}
	result, e := requestTranscription(context.Background(), Config{Provider: "elevenlabs"}, path, client)
	if e != nil {
		t.Fatal(e)
	}
	if words.bytesRead <= 16<<20 {
		t.Fatal("fixture did not exercise the former response limit")
	}
	if result.Text != "fixture long transcript" || len(result.Segments) == 0 {
		t.Fatal("long response lost transcript or segments")
	}
	last := result.Segments[len(result.Segments)-1]
	if last.End != 30000 || last.Speaker != "speaker_0" {
		t.Error("long response lost final timing or speaker")
	}
}

type transcriptionResponseBody struct {
	reader io.Reader
	read   bool
	closed bool
}

func (r *transcriptionResponseBody) Read(p []byte) (int, error) {
	r.read = true
	return r.reader.Read(p)
}

func (r *transcriptionResponseBody) Close() error {
	r.closed = true
	return nil
}

func TestTranscriptionResponseLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio.mp3")
	if e := os.WriteFile(path, []byte("fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("XNOTE_TEST_KEY", "test-only-key")
	for _, tc := range []struct {
		name, provider string
		length         int64
		reader         io.Reader
		wantRead       bool
		wantLimit      string
	}{
		{"declared ElevenLabs response", "elevenlabs", elevenLabsMaxResponseBytes + 1, strings.NewReader(""), false, "128 MiB"},
		{"declared API response", "api", (16 << 20) + 1, strings.NewReader(""), false, "16 MiB"},
		{"undeclared API response", "api", -1, io.MultiReader(strings.NewReader(`{"text":"`), strings.NewReader(strings.Repeat("x", 16<<20)), strings.NewReader(`"}`)), true, "16 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &transcriptionResponseBody{reader: tc.reader}
			client := &http.Client{Transport: transcriptionRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: body, ContentLength: tc.length}, nil
			})}
			_, e := requestTranscription(context.Background(), Config{Provider: tc.provider, APIURL: "https://transcription.invalid", APIKeyEnv: "XNOTE_TEST_KEY"}, path, client)
			if e == nil || !strings.Contains(e.Error(), tc.wantLimit) {
				t.Errorf("wrong response size error: %v", e)
			}
			if !body.closed || body.read != tc.wantRead {
				t.Errorf("response lifecycle: read=%v, closed=%v", body.read, body.closed)
			}
		})
	}
}
