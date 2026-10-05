package xnote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hajimehoshi/go-mp3"
)

const (
	elevenLabsMaxFileBytes = int64(3_000_000_000)
	elevenLabsMaxSeconds   = 10 * 60 * 60
	// Ten hours at 300 words/minute can produce 360,000 word and spacing
	// records. Allow about 300 bytes/record plus text/metadata, but retain a
	// hard limit. Words are folded into segments as they arrive, not retained.
	elevenLabsMaxResponseBytes = int64(128 << 20)
)

// Share connections across recordings, including concurrent workers. Request
// bodies and credentials still belong to each request, never to the transport.
var transcriptionHTTPTransport = &http.Transport{
	Proxy:               nil,
	MaxIdleConns:        32,
	MaxIdleConnsPerHost: 16,
	IdleConnTimeout:     90 * time.Second,
	TLSHandshakeTimeout: 15 * time.Second,
}

func newTranscriptionHTTPClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Minute, Transport: transcriptionHTTPTransport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// A received 429 is an explicit rejection, unlike a lost response after a paid
// submission. Only this response is eligible for automatic queue backoff.
type TranscriptionRateLimitError struct{ RetryAfter time.Duration }

func (e *TranscriptionRateLimitError) Error() string {
	return "transcription provider rate limited the request; waiting before retry"
}

func transcriptionRetryAfter(value string, now time.Time) time.Duration {
	delay := 30 * time.Second
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32); err == nil && seconds >= 0 {
		delay = time.Duration(seconds) * time.Second
	} else if until, err := http.ParseTime(value); err == nil && until.After(now) {
		delay = until.Sub(now)
	}
	if delay < time.Second {
		return time.Second
	}
	return delay
}

func Transcribe(ctx context.Context, c Config, path string) (string, error) {
	client := newTranscriptionHTTPClient()
	return transcribeWithClient(ctx, c, path, client)
}
func transcribeWithClient(ctx context.Context, c Config, path string, client *http.Client) (string, error) {
	r, e := requestTranscription(ctx, c, path, client)
	return r.Text, e
}
func requestTranscription(ctx context.Context, c Config, path string, client *http.Client) (TranscriptResult, error) {
	if e := ctx.Err(); e != nil {
		return TranscriptResult{}, e
	}
	c = effectiveTranscriptionConfig(c)
	if c.Provider == "doway" {
		return TranscriptResult{}, errors.New("DOWAY transcription requires the library session and recording; use xnote transcribe RECORDING_ID or Store.TranscribeDOWAY")
	}
	if c.Provider == "offline" {
		text, e := offline(ctx, c, path)
		return TranscriptResult{Text: text}, e
	}
	if c.Provider != "codex" && c.Provider != "api" && c.Provider != "elevenlabs" {
		return TranscriptResult{}, errors.New("unsupported transcription provider")
	}
	endpoint := c.APIURL
	key := ""
	if c.Provider == "codex" {
		endpoint = "http://127.0.0.1:8377/v1/audio/transcriptions"
	} else {
		u, e := url.Parse(endpoint)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return TranscriptResult{}, errors.New("configure an HTTPS transcription endpoint")
		}
		key = os.Getenv(c.APIKeyEnv)
		if key == "" {
			return TranscriptResult{}, errors.New("API key environment variable is empty: " + c.APIKeyEnv)
		}
	}
	body, contentType, size, e := transcriptionUpload(ctx, c, path)
	if e != nil {
		return TranscriptResult{}, e
	}
	defer body.Close()
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint, body)
	if e != nil {
		return TranscriptResult{}, e
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", contentType)
	if c.Provider == "elevenlabs" {
		req.Header.Set("xi-api-key", key)
	} else if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	// Enforce this even for injected clients: a redirect must never forward
	// credentials/audio, replay a paid request, or silently change its method.
	requestClient := *client
	requestClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := requestClient.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return TranscriptResult{}, ctx.Err()
		}
		return TranscriptResult{}, errors.New("transcription connection failed; check provider availability")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return TranscriptResult{}, &TranscriptionRateLimitError{RetryAfter: transcriptionRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	}
	if resp.StatusCode != 200 {
		return TranscriptResult{}, fmt.Errorf("transcription HTTP %d; retry from menu after fixing provider", resp.StatusCode)
	}
	limit := int64(16 << 20)
	if c.Provider == "elevenlabs" {
		limit = elevenLabsMaxResponseBytes
	}
	if resp.ContentLength > limit {
		return TranscriptResult{}, fmt.Errorf("transcription response exceeds %d MiB limit", limit>>20)
	}
	limited := &io.LimitedReader{R: resp.Body, N: limit + 1}
	decoder := json.NewDecoder(limited)
	var result transcriptionResponse
	if c.Provider == "elevenlabs" {
		result, e = decodeScribeResponse(decoder)
	} else {
		e = decoder.Decode(&result)
	}
	if e == nil {
		var extra json.RawMessage
		if decoder.Decode(&extra) != io.EOF {
			e = errors.New("unexpected trailing response data")
		}
	}
	if ctx.Err() != nil {
		return TranscriptResult{}, ctx.Err()
	}
	if limited.N == 0 {
		return TranscriptResult{}, fmt.Errorf("transcription response exceeds %d MiB limit", limit>>20)
	}
	if e != nil {
		return TranscriptResult{}, errors.New("invalid transcription response")
	}
	if result.Text == nil && len(result.Segments) == 0 {
		return TranscriptResult{}, errors.New("provider response has no text field")
	}
	out := TranscriptResult{Segments: result.Segments}
	if result.Text != nil {
		out.Text = strings.TrimSpace(*result.Text)
	}
	var text strings.Builder
	for i, segment := range out.Segments {
		if segment.Start < 0 || segment.End < segment.Start {
			return TranscriptResult{}, errors.New("invalid segment timestamps")
		}
		out.Segments[i].Timing = "provider"
		if result.Text == nil {
			text.WriteString(strings.TrimSpace(segment.Text))
			text.WriteByte('\n')
		}
	}
	if result.Text == nil {
		out.Text = text.String()
	}
	return out, nil
}

// Only multipart framing lives in memory. The request reads the original file
// directly, has an exact Content-Length, and owns the file for every exit path.
// No pipe producer or upload goroutine can remain blocked after cancellation.
func transcriptionUpload(ctx context.Context, c Config, path string) (*transcriptionBody, string, int64, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, "", 0, e
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
		}
	}()
	info, e := f.Stat()
	if e != nil {
		return nil, "", 0, e
	}
	if !info.Mode().IsRegular() {
		return nil, "", 0, errors.New("transcription audio must be a regular file")
	}
	if c.Provider == "elevenlabs" && info.Size() > elevenLabsMaxFileBytes {
		return nil, "", 0, errors.New("ElevenLabs audio exceeds 3 GB; split the recording before transcription")
	}
	if c.Provider == "elevenlabs" {
		// Scan MP3 frame headers for duration; do not decode the recording to
		// PCM. Other formats and unsupported MP3 variants are validated by the
		// provider, without automatic retries or conversion after a failure.
		decoder, e := mp3.NewDecoder(contextAudioFile{ctx: ctx, File: f})
		if ctx.Err() != nil {
			return nil, "", 0, ctx.Err()
		}
		if e == nil && decoder.Length() > int64(decoder.SampleRate()*4)*elevenLabsMaxSeconds {
			return nil, "", 0, errors.New("ElevenLabs audio exceeds 10 hours; split the recording before transcription")
		}
		if _, e = f.Seek(0, io.SeekStart); e != nil {
			return nil, "", 0, e
		}
	}
	var framing bytes.Buffer
	writer := multipart.NewWriter(&framing)
	// bytes.Buffer writes cannot fail; fields precede the streaming file part.
	if c.Provider == "api" {
		_ = writer.WriteField("model", c.Model)
		if c.Model == "gpt-4o-transcribe-diarize" {
			_ = writer.WriteField("response_format", "diarized_json")
			_ = writer.WriteField("chunking_strategy", "auto")
		} else if c.Model == "whisper-1" {
			_ = writer.WriteField("response_format", "verbose_json")
			_ = writer.WriteField("timestamp_granularities[]", "segment")
		}
	}
	if c.Provider == "elevenlabs" {
		_ = writer.WriteField("model_id", c.Model)
		_ = writer.WriteField("diarize", "true")
		_ = writer.WriteField("timestamps_granularity", "word")
		_ = writer.WriteField("tag_audio_events", "false")
		if c.Language != "" {
			_ = writer.WriteField("language_code", c.Language)
		}
	} else if c.Language != "" {
		_ = writer.WriteField("language", c.Language)
	}
	_, e = writer.CreateFormFile("file", filepath.Base(path))
	if e != nil {
		return nil, "", 0, e
	}
	headerLen := framing.Len()
	_ = writer.Close()
	encoded := framing.Bytes()
	body := &transcriptionBody{
		ctx: ctx, file: f,
		reader: io.MultiReader(bytes.NewReader(encoded[:headerLen]), io.LimitReader(f, info.Size()), bytes.NewReader(encoded[headerLen:])),
	}
	ok = true
	return body, writer.FormDataContentType(), int64(len(encoded)) + info.Size(), nil
}

type transcriptionBody struct {
	ctx      context.Context
	reader   io.Reader
	file     *os.File
	close    sync.Once
	closeErr error
}

func (b *transcriptionBody) Read(p []byte) (int, error) {
	if e := b.ctx.Err(); e != nil {
		return 0, e
	}
	return b.reader.Read(p)
}

func (b *transcriptionBody) Close() error {
	b.close.Do(func() { b.closeErr = b.file.Close() })
	return b.closeErr
}

// Resolve defaults at use time so choosing ElevenLabs also works with older
// configs that still contain the original OpenAI model and key variable.
func effectiveTranscriptionConfig(c Config) Config {
	if strings.EqualFold(strings.TrimSpace(c.Language), "auto") {
		c.Language = ""
	}
	if c.Provider == "elevenlabs" {
		if c.APIURL == "" {
			c.APIURL = "https://api.elevenlabs.io/v1/speech-to-text"
		}
		if c.Model == "" || c.Model == "whisper-1" {
			c.Model = "scribe_v2"
		}
		if c.APIKeyEnv == "" || c.APIKeyEnv == "XNOTE_API_KEY" {
			c.APIKeyEnv = "ELEVENLABS_API_KEY"
		}
	}
	return c
}

type scribeWord struct {
	Text    string   `json:"text"`
	Type    string   `json:"type"`
	Start   *float64 `json:"start"`
	End     *float64 `json:"end"`
	Speaker string   `json:"speaker_id"`
}

type transcriptionResponse struct {
	Text     *string   `json:"text"`
	Segments []Segment `json:"segments"`
}

func decodeScribeResponse(decoder *json.Decoder) (transcriptionResponse, error) {
	var result transcriptionResponse
	start, e := decoder.Token()
	if e != nil || start != json.Delim('{') {
		return result, errors.New("expected transcription object")
	}
	for decoder.More() {
		key, e := decoder.Token()
		if e != nil {
			return result, e
		}
		switch key {
		case "text":
			if e = decoder.Decode(&result.Text); e != nil {
				return result, e
			}
		case "words":
			start, e := decoder.Token()
			if e != nil {
				return result, e
			}
			if start == nil { // The API may omit/null words for empty speech.
				continue
			}
			if start != json.Delim('[') {
				return result, errors.New("expected transcription words")
			}
			var builder scribeSegmentBuilder
			for decoder.More() {
				var word scribeWord
				if e = decoder.Decode(&word); e != nil {
					return result, e
				}
				if e = builder.append(word); e != nil {
					return result, e
				}
			}
			if _, e = decoder.Token(); e != nil {
				return result, e
			}
			result.Segments = builder.segments
		default:
			var ignored json.RawMessage
			if e = decoder.Decode(&ignored); e != nil {
				return result, e
			}
		}
	}
	_, e = decoder.Token()
	return result, e
}

type scribeSegmentBuilder struct {
	segments []Segment
	spacing  string
}

func scribeSegments(words []scribeWord) ([]Segment, error) {
	var builder scribeSegmentBuilder
	for _, word := range words {
		if e := builder.append(word); e != nil {
			return nil, e
		}
	}
	return builder.segments, nil
}

func (b *scribeSegmentBuilder) append(word scribeWord) error {
	if word.Type == "spacing" {
		b.spacing += word.Text
		return nil
	}
	if word.Type != "word" && word.Type != "" || strings.TrimSpace(word.Text) == "" {
		return nil
	}
	if word.Start == nil || word.End == nil || *word.Start < 0 || *word.End < *word.Start {
		return errors.New("invalid word timestamps")
	}
	var current *Segment
	if len(b.segments) > 0 {
		current = &b.segments[len(b.segments)-1]
	}
	if current == nil || current.Speaker != word.Speaker || *word.Start-current.End > 1.5 || *word.End-current.Start > 30 {
		b.segments = append(b.segments, Segment{Start: *word.Start, End: *word.End, Speaker: word.Speaker, Text: word.Text, Timing: "provider"})
	} else {
		current.Text += b.spacing + word.Text
		current.End = max(current.End, *word.End)
	}
	b.spacing = ""
	return nil
}
func offline(ctx context.Context, c Config, path string) (string, error) {
	if c.OfflineModel == "" {
		return "", errors.New("offline mode needs a whisper.cpp model path in settings")
	}
	temp, e := os.MkdirTemp("", "xnote-whisper-*")
	if e != nil {
		return "", e
	}
	defer os.RemoveAll(temp)
	wav := filepath.Join(temp, "audio.wav")
	if e = exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-i", path, "-ar", "16000", "-ac", "1", wav).Run(); e != nil {
		return "", errors.New("offline conversion needs ffmpeg")
	}
	out := filepath.Join(temp, "transcript")
	args := []string{"-m", c.OfflineModel, "-f", wav, "-otxt", "-of", out}
	if c.Language != "" {
		args = append(args, "-l", c.Language)
	} else {
		args = append(args, "-l", "auto")
	}
	if e = exec.CommandContext(ctx, "whisper-cli", args...).Run(); e != nil {
		return "", errors.New("offline transcription needs whisper-cli and a compatible model")
	}
	b, e := os.ReadFile(out + ".txt")
	return strings.TrimSpace(string(b)), e
}
