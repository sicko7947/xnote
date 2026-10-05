package xnote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"tinygo.org/x/bluetooth"
)

type Command struct {
	Action string `json:"action"`
	ID     string `json:"id"`
}

func (s *Store) Queue(action, id string) error {
	if action != "download" && action != "device-delete" {
		return errors.New("unknown action")
	}
	if _, e := s.Get(id); e != nil {
		return e
	}
	return atomicJSON(s.path(filepath.Join(".work/commands", fmt.Sprintf("%d.json", time.Now().UnixNano()))), Command{action, id})
}
func wait(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
func Run(ctx context.Context, s *Store) error {
	ctx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	owner, e := s.Owner()
	if e != nil {
		return e
	}
	defer owner.Close()
	go runServiceWatchdog(ctx, s)
	defer s.SetStatus(Status{Phase: "stopped", Battery: -1})
	rows, e := s.Records()
	if e != nil {
		return e
	}
	for _, r := range rows {
		if r.State == "transcribing" {
			_ = s.Update(r.ID, func(r *Record) { r.State = "queued"; r.Error = "" })
		}
	}
	transCtx, stop := context.WithCancel(ctx)
	defer stop()
	transDone := make(chan struct{})
	go func() { defer close(transDone); transcriptionLoop(transCtx, s) }()
	defer func() { stop(); <-transDone }()
	cloudDone := make(chan struct{})
	go func() {
		defer close(cloudDone)
		for transCtx.Err() == nil {
			if _, e := os.Stat(s.path(".work/doway-session.json")); e == nil {
				_, _ = s.SyncCloud(transCtx)
			}
			if !wait(transCtx, 5*time.Minute) {
				return
			}
		}
	}()
	defer func() { stop(); <-cloudDone }()
	adapter := bluetooth.DefaultAdapter
	adapterReady := false
	for ctx.Err() == nil {
		// BlueZ or the adapter may not be ready yet at login or after resume.
		if !adapterReady {
			e = adapter.Enable()
		}
		if e != nil {
			s.SetStatus(Status{Phase: "waiting", Battery: -1, Detail: "Bluetooth unavailable: " + e.Error()})
			if !wait(ctx, 10*time.Second) {
				break
			}
			continue
		}
		adapterReady = true
		c := s.Config()
		s.SetStatus(Status{Phase: "searching", Battery: -1})
		device, err := Connect(ctx, adapter, c, func(detail string) {
			s.SetStatus(Status{Phase: "connecting", Battery: -1, Detail: detail})
		})
		if err != nil {
			adapterReady = false
			if errors.Is(err, context.DeadlineExceeded) && s.Status().Phase == "searching" {
				err = fmt.Errorf("Xnote %s not found; keep it awake nearby and disconnect DOWAY on the phone", c.Serial)
			}
			s.SetStatus(Status{Phase: "waiting", Battery: -1, Detail: err.Error()})
			if !wait(ctx, 5*time.Second) {
				break
			}
			continue
		}
		c = s.Config()
		c.DeviceID = device.device.Address.String()
		_ = s.SaveConfig(c)
		err = connected(ctx, s, device)
		device.Close()
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			s.SetStatus(Status{Phase: "waiting", Battery: -1, Detail: err.Error()})
		}
		if !wait(ctx, 3*time.Second) {
			break
		}
	}
	return nil
}
func connected(ctx context.Context, s *Store, d *Device) error {
	lastList := time.Time{}
	for ctx.Err() == nil {
		if time.Since(lastList) > 30*time.Second {
			files, e := d.List(ctx)
			if e != nil {
				return e
			}
			if e = s.Catalog(s.Config().Serial, files); e != nil {
				return e
			}
			lastList = time.Now()
		}
		s.SetStatus(Status{Phase: "connected", Battery: -1})
		paths, _ := filepath.Glob(s.path(".work/commands/*.json"))
		for _, path := range paths {
			var cmd Command
			if e := readJSON(path, &cmd); e != nil {
				return e
			}
			r, e := s.Get(cmd.ID)
			if e != nil {
				_ = os.Remove(path)
				continue
			}
			// Claim before issuing commands: a crash must never replay a deletion.
			claimed := path + ".processing"
			if e = os.Rename(path, claimed); e != nil {
				return e
			}
			switch cmd.Action {
			case "device-delete":
				e = d.Delete(ctx, r.DeviceName)
				if e == nil {
					e = s.Update(r.ID, func(r *Record) { r.OnDevice = false })
				}
				lastList = time.Time{}
			case "download":
				e = download(ctx, s, d, r)
			}
			// Consume once; never repeat a destructive command after reconnect.
			_ = os.Remove(claimed)
			if e != nil {
				return e
			}
		}
		didDownload := false
		if s.Config().Auto {
			rows, e := s.Records()
			if e != nil {
				return e
			}
			sort.SliceStable(rows, func(i, j int) bool { return rows[i].Size < rows[j].Size })
			for _, r := range rows {
				if r.Serial == s.Config().Serial && r.OnDevice && !r.Trashed && r.Audio == "" && r.Size > 0 && r.State != "download_error" {
					if e = download(ctx, s, d, r); e != nil {
						return e
					}
					didDownload = true
					break
				}
			}
		}
		if didDownload {
			continue
		}
		if !wait(ctx, time.Second) {
			break
		}
	}
	return nil
}
func download(ctx context.Context, s *Store, d *Device, r Record) error {
	if r.Audio != "" {
		return nil
	}
	dir := s.Dir(r)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	partial := filepath.Join(dir, "audio.mp3.partial")
	last := time.Time{}
	started := time.Now()
	startBytes := int64(0)
	if stat, e := os.Stat(partial); e == nil && stat.Size() < r.Size {
		startBytes = stat.Size()
	}
	err := d.Download(ctx, r, partial, func(received int64) {
		if time.Since(last) > 500*time.Millisecond {
			last = time.Now()
			s.SetStatus(Status{Phase: "downloading", Current: r.ID, Progress: int(received * 100 / r.Size), Battery: -1, BytesPerSecond: float64(received-startBytes) / time.Since(started).Seconds()})
		}
	})
	if err != nil {
		return err
	} // Resume partials after link loss, never submit them.
	if err = ValidateMP3(partial); err != nil {
		_ = s.Update(r.ID, func(r *Record) { r.State = "download_error"; r.Error = "MP3 validation: " + err.Error() })
		return err
	}
	final := filepath.Join(dir, "audio.mp3")
	if err = os.Rename(partial, final); err != nil {
		return err
	}
	duration, _ := AudioDuration(final)
	return s.Update(r.ID, func(r *Record) {
		r.Duration = duration
		r.Audio = final
		r.State = "downloaded"
		r.Error = ""
		r.DownloadSeconds = time.Since(started).Seconds()
		r.DownloadBytes = r.Size - startBytes
	})
}
func transcriptionLoop(ctx context.Context, s *Store) {
	for ctx.Err() == nil {
		rows, e := s.Records()
		if e == nil {
			for _, r := range rows {
				if r.Trashed || r.Audio == "" || !(r.State == "queued" || s.Config().Auto && r.State == "downloaded") {
					continue
				}
				config, readyErr := selectTranscriptionProvider(ctx, s.Config(), TranscriptionReady)
				if readyErr != nil {
					s.setTranscriptionStatus("waiting", config.Provider, r.ID, readyErr.Error())
					if !wait(ctx, 15*time.Second) {
						return
					}
					break
				}
				if e := s.Update(r.ID, func(r *Record) { r.State = "transcribing"; r.Error = "" }); e != nil {
					continue
				}
				s.setTranscriptionStatus("transcribing", config.Provider, r.ID, "")
				result, err := TranscribeRecording(ctx, config, r.Audio)
				if err != nil {
					if ctx.Err() != nil {
						_ = s.Update(r.ID, func(r *Record) { r.State = "queued"; r.Error = "" })
						s.setTranscriptionStatus("stopped", config.Provider, r.ID, "")
						return
					}
					_ = s.Update(r.ID, func(r *Record) { r.State = "error"; r.Error = err.Error() })
					s.setTranscriptionStatus("error", config.Provider, r.ID, err.Error())
				} else {
					provider := config.Provider
					_ = s.Update(r.ID, func(r *Record) {
						r.Transcript = result.Text
						r.Segments = result.Segments
						r.Provider = provider
						r.State = "done"
						if result.Text == "" {
							r.State = "no_speech"
						}
						r.Error = ""
					})
					s.setTranscriptionStatus("ready", config.Provider, "", "")
				}
				break
			}
		}
		if !wait(ctx, 2*time.Second) {
			return
		}
	}
}

// Fallback is selected before submitting audio. Never retry a failed paid
// request with another provider automatically.
func selectTranscriptionProvider(ctx context.Context, c Config, ready func(context.Context, Config) error) (Config, error) {
	err := ready(ctx, c)
	if err == nil || ctx.Err() != nil || c.FallbackProvider == "" {
		return c, err
	}
	fallback := fallbackConfig(c)
	if fallbackErr := ready(ctx, fallback); fallbackErr != nil {
		return c, fmt.Errorf("%s: %v; fallback %s: %v", c.Provider, err, fallback.Provider, fallbackErr)
	}
	return fallback, nil
}

func fallbackConfig(c Config) Config {
	fallback := c
	fallback.Provider = c.FallbackProvider
	// API fields belong to the primary provider. Built-in providers have their
	// own defaults, so do not forward an unrelated endpoint or model.
	if fallback.Provider == "elevenlabs" || fallback.Provider == "codex" {
		fallback.APIURL, fallback.Model, fallback.APIKeyEnv = "", "", ""
	}
	return fallback
}

type TranscriptionStatus struct {
	Phase     string `json:"phase"`
	Provider  string `json:"provider,omitempty"`
	Current   string `json:"current,omitempty"`
	Detail    string `json:"detail,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func (s *Store) setTranscriptionStatus(phase, provider, current, detail string) {
	_ = atomicJSON(s.path(".work/transcription-status.json"), TranscriptionStatus{phase, provider, current, detail, time.Now().UTC().Format(time.RFC3339)})
}

func (s *Store) TranscriptionStatus() (st TranscriptionStatus) {
	if readJSON(s.path(".work/transcription-status.json"), &st) != nil {
		st.Phase = "idle"
	}
	if s.Status().Phase == "stopped" {
		st.Phase = "stopped"
	}
	return
}
