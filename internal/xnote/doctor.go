package xnote

import (
	"net/http"
	"os"
	"os/exec"
	"time"
)

func Doctor(s *Store) map[string]any {
	c := s.Config()
	result := map[string]any{"library": s.Root, "provider": c.Provider, "automatic": c.Auto, "sync": s.Status().Phase}
	switch c.Provider {
	case "codex":
		client := http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
		resp, e := client.Get("http://127.0.0.1:8377/")
		result["codex_proxy_ready"] = e == nil && resp.StatusCode == 200
		if resp != nil {
			resp.Body.Close()
		}
	case "api":
		result["api_key_available"] = os.Getenv(c.APIKeyEnv) != ""
		result["api_url"] = c.APIURL
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
	return result
}
