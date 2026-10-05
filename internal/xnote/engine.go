package xnote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
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
	owner, e := s.Owner()
	if e != nil {
		return e
	}
	defer owner.Close()
	defer s.SetStatus(Status{Phase: "stopped", Battery: -1})
	rows, e := s.Records()
	if e != nil {
		return e
	}
	for _, r := range rows {
		if r.State == "transcribing" {
			_ = s.Update(r.ID, func(r *Record) {
				r.State = "error"
				r.Error = "Transcription interrupted; retry manually to avoid duplicate requests"
			})
		}
		if r.SummaryState == "running" {
			_ = s.Update(r.ID, func(r *Record) {
				r.SummaryState = "error"
				r.SummaryError = "AI processing interrupted; retry manually to avoid duplicate requests"
			})
		}
	}
	transCtx, stop := context.WithCancel(ctx)
	defer stop()
	transDone := make(chan struct{})
	go func() { defer close(transDone); transcriptionLoop(transCtx, s) }()
	defer func() { stop(); <-transDone }()
	summaryDone := make(chan struct{})
	go func() {
		defer close(summaryDone)
		RunSummaryLoop(transCtx, s, s.GenerateDOWAYSummary)
	}()
	defer func() { stop(); <-summaryDone }()
	cloudDone := make(chan struct{})
	go func() {
		defer close(cloudDone)
		for transCtx.Err() == nil {
			if c := s.Config(); c.AutoTranscribe && c.Provider == "doway" {
				if _, e := os.Stat(s.path(".work/doway-session.json")); e == nil {
					_, _ = s.SyncCloud(transCtx)
				}
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

// The scheduler owns its active set and status file; workers only own the
// recording they atomically claimed. Provider calls always use a config snapshot.
type transcriptionLoopDeps struct {
	Ready          func(context.Context, Config) error
	Transcribe     func(context.Context, Config, Record) (TranscriptResult, error)
	PollInterval   time.Duration
	ReadinessRetry time.Duration
}

type transcriptionFinished struct {
	ID, Provider string
	Err          error
}

func transcriptionLoop(ctx context.Context, s *Store) {
	transcriptionLoopWithDeps(ctx, s, transcriptionLoopDeps{
		Ready: s.TranscriptionReady,
		Transcribe: func(ctx context.Context, c Config, r Record) (TranscriptResult, error) {
			if c.Provider == "doway" {
				return s.TranscribeDOWAY(ctx, c, r)
			}
			return TranscribeRecording(ctx, c, r.Audio)
		},
	})
}

func eligibleTranscription(r Record, c Config) bool {
	return !r.Trashed && r.Audio != "" && (r.State == "queued" || c.AutoTranscribe && r.State == "downloaded")
}

func transcriptionLoopWithDeps(ctx context.Context, s *Store, d transcriptionLoopDeps) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if d.PollInterval <= 0 {
		d.PollInterval = 500 * time.Millisecond
	}
	if d.ReadinessRetry <= 0 {
		d.ReadinessRetry = 15 * time.Second
	}
	ticker := time.NewTicker(d.PollInterval)
	defer ticker.Stop()
	active := map[string]TranscriptionActive{}
	finished := make(chan transcriptionFinished, 16)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		workers.Wait()
		c := s.Config()
		queued := 0
		if rows, err := s.Records(); err == nil {
			for _, r := range rows {
				if eligibleTranscription(r, c) {
					queued++
				}
			}
		}
		s.writeTranscriptionStatus(TranscriptionStatus{Phase: "stopped", Provider: c.Provider, Queued: queued, Concurrency: EffectiveTranscriptionConcurrency(c)})
	}()
	last := transcriptionFinished{}
	lastPhase := "idle"
	var blockedConfig Config
	var blockedUntil time.Time
	blockedDetail, blockedID := "", ""
	rateLimitedUntil := map[string]time.Time{}
	rateLimitRejections := map[string]int{}
	handleFinished := func(result transcriptionFinished) {
		delete(active, result.ID)
		last, lastPhase = result, "ready"
		if result.Err != nil {
			lastPhase = "error"
		}
		var limited *TranscriptionRateLimitError
		if result.Err == nil || !errors.As(result.Err, &limited) || ctx.Err() != nil {
			return
		}
		delay := limited.RetryAfter
		if delay <= 0 {
			delay = 30 * time.Second
		}
		until := time.Now().Add(delay)
		if until.After(rateLimitedUntil[result.Provider]) {
			rateLimitedUntil[result.Provider] = until
		}
		rateLimitRejections[result.ID]++
		if rateLimitRejections[result.ID] >= 3 {
			return
		}
		// A typed 429 explicitly rejected this request. Only this case may
		// return to the queue; transport errors and 5xx are never replayed.
		if err := s.Update(result.ID, func(r *Record) {
			if r.State == "error" && r.Error == result.Err.Error() && !r.Trashed {
				r.State = "queued"
				r.Error = "Provider rate limit; waiting before retry"
			}
		}); err != nil {
			last.Err = err
		}
	}
	for ctx.Err() == nil {
		// Apply all completed requests (including provider backoff) before
		// filling newly available slots.
		draining := true
		for draining {
			select {
			case result := <-finished:
				handleFinished(result)
			default:
				draining = false
			}
		}
		config := s.Config()
		limit := EffectiveTranscriptionConcurrency(config)
		rows, err := s.Records()
		pending := make([]Record, 0)
		if err == nil {
			for _, r := range rows {
				if _, running := active[r.ID]; !running && eligibleTranscription(r, config) {
					pending = append(pending, r)
				}
			}
			// Explicit requests take priority while preserving the library order.
			sort.SliceStable(pending, func(i, j int) bool { return pending[i].State == "queued" && pending[j].State != "queued" })
		} else {
			lastPhase, last.Err = "error", err
		}
		if config != blockedConfig {
			blockedUntil, blockedDetail, blockedID = time.Time{}, "", ""
		}
		queued := len(pending)
		providerLimited := time.Now().Before(rateLimitedUntil[config.Provider])
		if !config.TranscriptionPaused && !providerLimited && len(active) < limit && len(pending) > 0 && !time.Now().Before(blockedUntil) {
			if readyErr := d.Ready(ctx, config); readyErr != nil {
				blockedConfig, blockedUntil = config, time.Now().Add(d.ReadinessRetry)
				blockedDetail, blockedID = readyErr.Error(), pending[0].ID
			} else {
				blockedUntil, blockedDetail, blockedID = time.Time{}, "", ""
				for _, candidate := range pending {
					if len(active) >= limit || ctx.Err() != nil {
						break
					}
					r := candidate
					claimed := false
					claimErr := s.Update(r.ID, func(current *Record) {
						// Settings or a manual edit can change while readiness runs.
						// Recheck under the store lock before starting any paid work.
						if ctx.Err() != nil || s.Config() != config || !eligibleTranscription(*current, config) {
							return
						}
						current.State, current.Error = "transcribing", ""
						r, claimed = *current, true
					})
					if claimErr != nil {
						lastPhase, last.Err = "error", claimErr
						continue
					}
					if !claimed {
						continue
					}
					active[r.ID] = TranscriptionActive{ID: r.ID, Title: r.Title, Provider: config.Provider, Phase: "transcribing"}
					queued--
					lastPhase, last = "ready", transcriptionFinished{}
					workers.Add(1)
					go func(r Record, config Config) {
						defer workers.Done()
						result, requestErr := d.Transcribe(ctx, config, r)
						interrupted := requestErr != nil && ctx.Err() != nil
						if interrupted {
							requestErr = errors.New("Transcription interrupted; retry manually to avoid duplicate requests")
						}
						saveErr := s.Update(r.ID, func(current *Record) {
							if current.Audio != r.Audio {
								return
							}
							if requestErr != nil {
								current.State, current.Error = "error", requestErr.Error()
								return
							}
							current.Transcript, current.Segments, current.Provider = result.Text, result.Segments, config.Provider
							current.State, current.Error = "done", ""
							if result.Text == "" {
								current.State = "no_speech"
							}
							queueAutomaticSummary(current, s.Config())
						})
						if saveErr != nil {
							requestErr = saveErr
						}
						// Capacity equals the hard upper bound, so shutdown can join
						// every worker even after the scheduler stops receiving.
						finished <- transcriptionFinished{ID: r.ID, Provider: config.Provider, Err: requestErr}
					}(r, config)
				}
			}
		}
		st := TranscriptionStatus{Phase: lastPhase, Provider: config.Provider, Running: len(active), Queued: queued, Concurrency: limit}
		if last.Err != nil {
			st.Current, st.Provider, st.Detail = last.ID, last.Provider, last.Err.Error()
		}
		for _, item := range active {
			st.Active = append(st.Active, item)
		}
		sort.Slice(st.Active, func(i, j int) bool { return st.Active[i].ID < st.Active[j].ID })
		if len(st.Active) > 0 {
			st.Phase, st.Provider, st.Current, st.Detail = "transcribing", st.Active[0].Provider, st.Active[0].ID, ""
		}
		if blockedDetail != "" && queued > 0 {
			st.Detail = blockedDetail
			if len(st.Active) == 0 {
				st.Phase, st.Provider, st.Current = "waiting", config.Provider, blockedID
			}
		}
		if providerLimited && queued > 0 {
			st.Detail = "Provider rate limit; queued work resumes after " + rateLimitedUntil[config.Provider].UTC().Format(time.RFC3339)
			if len(st.Active) == 0 {
				st.Phase, st.Provider, st.Current = "waiting", config.Provider, pending[0].ID
			}
		}
		if config.TranscriptionPaused {
			st.Phase = "paused"
		}
		s.writeTranscriptionStatus(st)
		select {
		case <-ctx.Done():
			return
		case result := <-finished:
			handleFinished(result)
		case <-ticker.C:
		}
	}
}

type TranscriptionActive struct {
	ID       string `json:"id"`
	Title    string `json:"title,omitempty"`
	Provider string `json:"provider"`
	Phase    string `json:"phase"`
	Detail   string `json:"detail,omitempty"`
	Progress int    `json:"progress,omitempty"`
}

type TranscriptionStatus struct {
	Phase       string                `json:"phase"`
	Provider    string                `json:"provider,omitempty"`
	Current     string                `json:"current,omitempty"`
	Detail      string                `json:"detail,omitempty"`
	UpdatedAt   string                `json:"updated_at,omitempty"`
	Running     int                   `json:"running"`
	Queued      int                   `json:"queued"`
	Concurrency int                   `json:"concurrency"`
	Active      []TranscriptionActive `json:"active,omitempty"`
}

func (s *Store) writeTranscriptionStatus(st TranscriptionStatus) {
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	_ = atomicJSON(s.path(".work/transcription-status.json"), st)
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
