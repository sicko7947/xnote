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
	"strings"
	"time"
)

func Transcribe(ctx context.Context, c Config, path string) (string, error) {
	client := &http.Client{Timeout: 20 * time.Minute, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return transcribeWithClient(ctx, c, path, client)
}
func transcribeWithClient(ctx context.Context, c Config, path string, client *http.Client) (string, error) {
	r, e := requestTranscription(ctx, c, path, client)
	return r.Text, e
}
func requestTranscription(ctx context.Context, c Config, path string, client *http.Client) (TranscriptResult, error) {
	c = effectiveTranscriptionConfig(c)
	if c.Provider == "doway" {
		return TranscriptResult{}, errors.New("DOWAY transcription is pending authenticated protocol validation")
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
	f, e := os.Open(path)
	if e != nil {
		return TranscriptResult{}, e
	}
	defer f.Close()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, e := writer.CreateFormFile("file", filepath.Base(path))
	if e != nil {
		return TranscriptResult{}, e
	}
	if _, e = io.Copy(part, f); e != nil {
		return TranscriptResult{}, e
	}
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
	_ = writer.Close()
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint, &body)
	if e != nil {
		return TranscriptResult{}, e
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if c.Provider == "elevenlabs" {
		req.Header.Set("xi-api-key", key)
	} else if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, e := client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return TranscriptResult{}, ctx.Err()
		}
		return TranscriptResult{}, errors.New("transcription connection failed; check provider availability")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return TranscriptResult{}, fmt.Errorf("transcription HTTP %d; retry from menu after fixing provider", resp.StatusCode)
	}
	var result struct {
		Text     *string      `json:"text"`
		Segments []Segment    `json:"segments"`
		Words    []scribeWord `json:"words"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&result); e != nil {
		return TranscriptResult{}, errors.New("invalid transcription response")
	}
	if result.Text == nil && len(result.Segments) == 0 && !(c.Provider == "elevenlabs" && len(result.Words) > 0) {
		return TranscriptResult{}, errors.New("provider response has no text field")
	}
	out := TranscriptResult{Segments: result.Segments}
	if c.Provider == "elevenlabs" {
		out.Segments, e = scribeSegments(result.Words)
		if e != nil {
			return TranscriptResult{}, e
		}
	}
	if result.Text != nil {
		out.Text = strings.TrimSpace(*result.Text)
	}
	for i, segment := range out.Segments {
		if segment.Start < 0 || segment.End < segment.Start {
			return TranscriptResult{}, errors.New("invalid segment timestamps")
		}
		out.Segments[i].Timing = "provider"
		if result.Text == nil {
			out.Text += strings.TrimSpace(segment.Text) + "\n"
		}
	}
	return out, nil
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

func scribeSegments(words []scribeWord) ([]Segment, error) {
	var segments []Segment
	var current *Segment
	spacing := ""
	for _, word := range words {
		if word.Type == "spacing" {
			spacing += word.Text
			continue
		}
		if word.Type != "word" && word.Type != "" {
			continue
		}
		if strings.TrimSpace(word.Text) == "" {
			continue
		}
		if word.Start == nil || word.End == nil || *word.Start < 0 || *word.End < *word.Start {
			return nil, errors.New("invalid word timestamps")
		}
		if current == nil || current.Speaker != word.Speaker || *word.Start-current.End > 1.5 || *word.End-current.Start > 30 {
			segments = append(segments, Segment{Start: *word.Start, End: *word.End, Speaker: word.Speaker, Text: word.Text, Timing: "provider"})
			current = &segments[len(segments)-1]
		} else {
			current.Text += spacing + word.Text
			current.End = max(current.End, *word.End)
		}
		spacing = ""
	}
	return segments, nil
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
