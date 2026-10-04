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
	if c.Provider == "doway" {
		return TranscriptResult{}, errors.New("DOWAY transcription is pending authenticated protocol validation")
	}
	if c.Provider == "offline" {
		text, e := offline(ctx, c, path)
		return TranscriptResult{Text: text}, e
	}
	endpoint := c.APIURL
	key := ""
	if c.Provider == "codex" {
		endpoint = "http://127.0.0.1:8377/v1/audio/transcriptions"
	} else {
		u, e := url.Parse(endpoint)
		if e != nil || u.Scheme != "https" || u.Host == "" {
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
	if c.Language != "" {
		_ = writer.WriteField("language", c.Language)
	}
	_ = writer.Close()
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint, &body)
	if e != nil {
		return TranscriptResult{}, e
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, e := client.Do(req)
	if e != nil {
		return TranscriptResult{}, fmt.Errorf("transcription connection failed; check provider availability: %w", e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return TranscriptResult{}, fmt.Errorf("transcription HTTP %d; retry from menu after fixing provider", resp.StatusCode)
	}
	var result struct {
		Text     *string   `json:"text"`
		Segments []Segment `json:"segments"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&result); e != nil {
		return TranscriptResult{}, errors.New("invalid transcription response")
	}
	if result.Text == nil && len(result.Segments) == 0 {
		return TranscriptResult{}, errors.New("provider response has no text field")
	}
	out := TranscriptResult{Segments: result.Segments}
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
