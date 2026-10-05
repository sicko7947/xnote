package xnote

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"
)

// TranscriptionReady checks local prerequisites without submitting audio or
// consuming transcription credits. Remote credentials are not authenticated.
func TranscriptionReady(ctx context.Context, c Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c = effectiveTranscriptionConfig(c)
	switch c.Provider {
	case "codex":
		client := http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, err := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:8377/", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		if err != nil || resp.StatusCode != http.StatusOK {
			return errors.New("Codex transcription proxy is unavailable")
		}
	case "api", "elevenlabs":
		u, err := url.Parse(c.APIURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return errors.New("configure an HTTPS transcription endpoint")
		}
		if os.Getenv(c.APIKeyEnv) == "" {
			return errors.New("API key environment variable is empty: " + c.APIKeyEnv)
		}
	case "offline":
		for _, binary := range []string{"whisper-cli", "ffmpeg"} {
			if _, err := exec.LookPath(binary); err != nil {
				return errors.New("offline transcription needs " + binary)
			}
		}
		if info, err := os.Stat(c.OfflineModel); err != nil || info.IsDir() {
			return errors.New("offline transcription needs a whisper.cpp model file")
		}
	case "doway":
		return errors.New("DOWAY transcription is pending authenticated protocol validation")
	default:
		return errors.New("unsupported transcription provider")
	}
	return nil
}

func Doctor(s *Store) map[string]any {
	c := effectiveTranscriptionConfig(s.Config())
	result := map[string]any{"library": s.Root, "provider": c.Provider, "automatic": c.Auto, "sync": s.Status().Phase}
	readyErr := TranscriptionReady(context.Background(), c)
	result["ready"] = readyErr == nil
	if readyErr != nil {
		result["reason"] = readyErr.Error()
	}
	if c.FallbackProvider != "" {
		fallback := fallbackConfig(s.Config())
		err := TranscriptionReady(context.Background(), fallback)
		result["fallback_provider"] = fallback.Provider
		result["fallback_ready"] = err == nil
		if err != nil {
			result["fallback_reason"] = err.Error()
		}
	}
	switch c.Provider {
	case "codex":
		result["codex_proxy_ready"] = readyErr == nil
	case "api", "elevenlabs":
		result["api_key_available"] = os.Getenv(c.APIKeyEnv) != ""
		if u, err := url.Parse(c.APIURL); err == nil {
			u.User, u.RawQuery, u.Fragment = nil, "", ""
			result["api_url"] = u.String()
		}
		result["api_model"] = c.Model
		result["api_key_env"] = c.APIKeyEnv
	case "offline":
		_, e := exec.LookPath("whisper-cli")
		result["whisper_cli"] = e == nil
		_, e = exec.LookPath("ffmpeg")
		result["ffmpeg"] = e == nil
		_, e = os.Stat(c.OfflineModel)
		result["model_exists"] = e == nil
	case "doway":
		result["ready"] = false
		result["reason"] = "authenticated transcription protocol pending"
	}
	result["transcription"] = s.TranscriptionStatus()
	result["background_installed"] = ServiceInstalled()
	return result
}
