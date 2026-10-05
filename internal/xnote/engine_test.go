package xnote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type poolCall struct {
	Record  Record
	Config  Config
	Release chan struct{}
}

type fakeTranscriptionPool struct {
	Started       chan poolCall
	mu            sync.Mutex
	running, peak int
	calls         map[string]int
}

func (p *fakeTranscriptionPool) transcribe(ctx context.Context, c Config, r Record) (TranscriptResult, error) {
	p.mu.Lock()
	p.running++
	if p.running > p.peak {
		p.peak = p.running
	}
	p.calls[r.ID]++
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.running--; p.mu.Unlock() }()
	call := poolCall{Record: r, Config: c, Release: make(chan struct{})}
	p.Started <- call
	select {
	case <-ctx.Done():
		return TranscriptResult{}, ctx.Err()
	case <-call.Release:
		return TranscriptResult{Text: "transcribed " + r.ID}, nil
	}
}

func transcriptionPoolFixture(t *testing.T, queued, downloaded, limit int) (*Store, *fakeTranscriptionPool) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := s.Config()
	c.Provider, c.Model, c.AutoTranscribe, c.TranscriptionConcurrency = "api", "original-model", false, limit
	if err = s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	files := make([]DeviceFile, queued+downloaded)
	for i := range files {
		files[i] = DeviceFile{fmt.Sprintf("202610050000%02d", i), 10}
	}
	if err = s.Catalog(c.Serial, files); err != nil {
		t.Fatal(err)
	}
	for i, file := range files {
		state := "queued"
		if i >= queued {
			state = "downloaded"
		}
		if err = s.Update(c.Serial+"-"+file.Name, func(r *Record) { r.Audio, r.State = "fixture.mp3", state }); err != nil {
			t.Fatal(err)
		}
	}
	s.SetStatus(Status{Phase: "connected", Battery: -1})
	return s, &fakeTranscriptionPool{Started: make(chan poolCall, 32), calls: map[string]int{}}
}

func runFakeTranscriptionPool(t *testing.T, s *Store, p *fakeTranscriptionPool) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		transcriptionLoopWithDeps(ctx, s, transcriptionLoopDeps{Ready: func(context.Context, Config) error { return nil }, Transcribe: p.transcribe, PollInterval: 5 * time.Millisecond})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("pool did not cancel and join")
		}
	})
	return cancel, done
}

func nextPoolCall(t *testing.T, p *fakeTranscriptionPool) poolCall {
	t.Helper()
	select {
	case call := <-p.Started:
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("transcription did not start")
		return poolCall{}
	}
}

func awaitPoolStatus(t *testing.T, s *Store, match func(TranscriptionStatus) bool) TranscriptionStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		st := s.TranscriptionStatus()
		if match(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("pool status did not converge: %+v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTranscriptionPoolOverlapsWithinLimitAndPreservesDownloadOnly(t *testing.T) {
	s, p := transcriptionPoolFixture(t, 5, 2, 2)
	runFakeTranscriptionPool(t, s, p)
	first, second := nextPoolCall(t, p), nextPoolCall(t, p)
	st := awaitPoolStatus(t, s, func(st TranscriptionStatus) bool { return st.Running == 2 && st.Queued == 3 })
	if len(st.Active) != 2 || st.Concurrency != 2 || st.Phase != "transcribing" {
		t.Fatalf("incomplete aggregate status: %+v", st)
	}
	close(first.Release)
	third := nextPoolCall(t, p)
	close(second.Release)
	fourth := nextPoolCall(t, p)
	close(third.Release)
	fifth := nextPoolCall(t, p)
	close(fourth.Release)
	close(fifth.Release)
	awaitPoolStatus(t, s, func(st TranscriptionStatus) bool { return st.Running == 0 && st.Queued == 0 && st.Phase == "ready" })
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.peak != 2 || len(p.calls) != 5 {
		t.Fatalf("pool peak/calls = %d/%d", p.peak, len(p.calls))
	}
	for id, n := range p.calls {
		if n != 1 {
			t.Fatalf("duplicate request for %s", id)
		}
	}
	rows, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, ran := p.calls[r.ID]; !ran && r.State != "downloaded" {
			t.Fatalf("download-only record changed: %+v", r)
		}
	}
}

func TestTranscriptionPoolDynamicLimitAndSettingsDoNotRestartActive(t *testing.T) {
	s, p := transcriptionPoolFixture(t, 5, 0, 1)
	runFakeTranscriptionPool(t, s, p)
	first := nextPoolCall(t, p)
	c := s.Config()
	c.TranscriptionConcurrency = 3
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	second, third := nextPoolCall(t, p), nextPoolCall(t, p)
	c.TranscriptionConcurrency, c.Model = 1, "new-model"
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	awaitPoolStatus(t, s, func(st TranscriptionStatus) bool { return st.Concurrency == 1 && st.Running == 3 })
	close(first.Release)
	awaitPoolStatus(t, s, func(st TranscriptionStatus) bool { return st.Running == 2 })
	select {
	case <-p.Started:
		t.Fatal("started above decreased limit")
	default:
	}
	close(second.Release)
	awaitPoolStatus(t, s, func(st TranscriptionStatus) bool { return st.Running == 1 })
	select {
	case <-p.Started:
		t.Fatal("started while active equaled decreased limit")
	default:
	}
	close(third.Release)
	fourth := nextPoolCall(t, p)
	if first.Config.Model != "original-model" || second.Config.Model != "original-model" || third.Config.Model != "original-model" || fourth.Config.Model != "new-model" {
		t.Fatal("active config snapshots were replaced or new config ignored")
	}
	close(fourth.Release)
	fifth := nextPoolCall(t, p)
	close(fifth.Release)
	awaitPoolStatus(t, s, func(st TranscriptionStatus) bool { return st.Running == 0 && st.Queued == 0 })
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.peak != 3 || len(p.calls) != 5 {
		t.Fatal("dynamic pool did not fill or restarted existing work")
	}
}

func TestTranscriptionPoolPauseDrainsAndResumeKeepsManualQueue(t *testing.T) {
	s, p := transcriptionPoolFixture(t, 3, 1, 1)
	runFakeTranscriptionPool(t, s, p)
	first := nextPoolCall(t, p)
	c := s.Config()
	c.TranscriptionPaused = true
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	awaitPoolStatus(t, s, func(st TranscriptionStatus) bool { return st.Phase == "paused" && st.Running == 1 && st.Queued == 2 })
	close(first.Release)
	awaitPoolStatus(t, s, func(st TranscriptionStatus) bool { return st.Phase == "paused" && st.Running == 0 && st.Queued == 2 })
	select {
	case <-p.Started:
		t.Fatal("paused queue started more work")
	default:
	}
	c.TranscriptionPaused = false
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	second := nextPoolCall(t, p)
	close(second.Release)
	third := nextPoolCall(t, p)
	close(third.Release)
	awaitPoolStatus(t, s, func(st TranscriptionStatus) bool { return st.Running == 0 && st.Queued == 0 })
	if s.Config().AutoTranscribe {
		t.Fatal("pause toggling enabled automatic transcription")
	}
}

func TestTranscriptionPoolAtomicClaimAndCancelJoinsWorkers(t *testing.T) {
	s, p := transcriptionPoolFixture(t, 1, 0, 4)
	c := s.Config()
	c.AutoTranscribe = true
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	cancel1, done1 := runFakeTranscriptionPool(t, s, p)
	cancel2, done2 := runFakeTranscriptionPool(t, s, p)
	call := nextPoolCall(t, p)
	cancel1()
	cancel2()
	for _, done := range []<-chan struct{}{done1, done2} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("scheduler exited without joining canceled workers")
		}
	}
	r, err := s.Get(call.Record.ID)
	if err != nil || r.State != "error" || !strings.Contains(r.Error, "retry manually") {
		t.Fatalf("interrupted work can be automatically retried: %+v %v", r, err)
	}
	p.mu.Lock()
	running, calls := p.running, p.calls[call.Record.ID]
	p.mu.Unlock()
	if running != 0 || calls != 1 {
		t.Fatalf("workers leaked or duplicate claim: running=%d calls=%d", running, calls)
	}
	// A fresh scheduler sees error, not downloaded/queued, and cannot replay it.
	cancel3, done3 := runFakeTranscriptionPool(t, s, p)
	awaitPoolStatus(t, s, func(st TranscriptionStatus) bool { return st.Phase == "idle" && st.Running == 0 && st.Queued == 0 })
	cancel3()
	<-done3
	select {
	case <-p.Started:
		t.Fatal("interrupted request was replayed")
	default:
	}
}

func TestTranscriptionPoolEmptyQueueDoesNotCheckOrSubmitProvider(t *testing.T) {
	s, _ := transcriptionPoolFixture(t, 0, 2, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		transcriptionLoopWithDeps(ctx, s, transcriptionLoopDeps{
			Ready: func(context.Context, Config) error { t.Error("checked provider for empty eligible queue"); return nil },
			Transcribe: func(context.Context, Config, Record) (TranscriptResult, error) {
				t.Error("submitted download-only recording")
				return TranscriptResult{}, nil
			},
			PollInterval: 5 * time.Millisecond,
		})
	}()
	awaitPoolStatus(t, s, func(st TranscriptionStatus) bool { return st.Phase == "idle" && st.Concurrency == 4 })
	cancel()
	<-done
}

func TestTranscriptionPoolExplicitRateLimitStopsAfterThreeRejections(t *testing.T) {
	s, _ := transcriptionPoolFixture(t, 1, 0, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var mu sync.Mutex
	var calls []time.Time
	go func() {
		defer close(done)
		transcriptionLoopWithDeps(ctx, s, transcriptionLoopDeps{
			Ready: func(context.Context, Config) error { return nil },
			Transcribe: func(context.Context, Config, Record) (TranscriptResult, error) {
				mu.Lock()
				calls = append(calls, time.Now())
				mu.Unlock()
				return TranscriptResult{}, &TranscriptionRateLimitError{RetryAfter: 40 * time.Millisecond}
			},
			PollInterval: 5 * time.Millisecond,
		})
	}()
	t.Cleanup(func() { cancel(); <-done })
	awaitPoolStatus(t, s, func(st TranscriptionStatus) bool { return st.Phase == "error" && st.Running == 0 && st.Queued == 0 })
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("429 attempts=%d", len(calls))
	}
	for i := 1; i < len(calls); i++ {
		if calls[i].Sub(calls[i-1]) < 40*time.Millisecond {
			t.Fatal("retried before Retry-After")
		}
	}
	rows, err := s.Records()
	if err != nil || len(rows) != 1 || rows[0].State != "error" {
		t.Fatalf("rate-limit exhaustion not terminal: %+v %v", rows, err)
	}
}

func TestTranscriptionPoolRateLimitPausesProviderWithoutCancelingActive(t *testing.T) {
	s, _ := transcriptionPoolFixture(t, 3, 0, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	started := make(chan int, 8)
	reject := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	go func() {
		defer close(done)
		transcriptionLoopWithDeps(ctx, s, transcriptionLoopDeps{
			Ready: func(context.Context, Config) error { return nil },
			Transcribe: func(ctx context.Context, _ Config, _ Record) (TranscriptResult, error) {
				mu.Lock()
				calls++
				n := calls
				mu.Unlock()
				started <- n
				if n == 1 {
					select {
					case <-reject:
						return TranscriptResult{}, &TranscriptionRateLimitError{RetryAfter: 100 * time.Millisecond}
					case <-ctx.Done():
						return TranscriptResult{}, ctx.Err()
					}
				}
				<-ctx.Done()
				return TranscriptResult{}, ctx.Err()
			}, PollInterval: 5 * time.Millisecond,
		})
	}()
	t.Cleanup(func() { cancel(); <-done })
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("initial pool not full")
		}
	}
	rejectedAt := time.Now()
	close(reject)
	awaitPoolStatus(t, s, func(st TranscriptionStatus) bool {
		return st.Running == 1 && st.Queued == 2 && strings.Contains(st.Detail, "rate limit")
	})
	select {
	case <-started:
		if time.Since(rejectedAt) < 100*time.Millisecond {
			t.Fatal("provider queue continued during Retry-After")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("provider queue did not resume after Retry-After")
	}
	awaitPoolStatus(t, s, func(st TranscriptionStatus) bool { return st.Running == 2 })
}

func TestTranscriptionPoolDoesNotRetryAmbiguousProviderErrors(t *testing.T) {
	s, _ := transcriptionPoolFixture(t, 1, 0, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	go func() {
		defer close(done)
		transcriptionLoopWithDeps(ctx, s, transcriptionLoopDeps{
			Ready: func(context.Context, Config) error { return nil },
			Transcribe: func(context.Context, Config, Record) (TranscriptResult, error) {
				mu.Lock()
				calls++
				mu.Unlock()
				return TranscriptResult{}, errors.New("connection closed after upload")
			},
			PollInterval: 5 * time.Millisecond,
		})
	}()
	awaitPoolStatus(t, s, func(st TranscriptionStatus) bool { return st.Phase == "error" && st.Running == 0 && st.Queued == 0 })
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatal("ambiguous request was automatically repeated")
	}
}

func TestTranscriptionPoolKeepsSuccessfulResponseWhenQuitRacesCompletion(t *testing.T) {
	s, _ := transcriptionPoolFixture(t, 1, 0, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done, started := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		transcriptionLoopWithDeps(ctx, s, transcriptionLoopDeps{
			Ready: func(context.Context, Config) error { return nil },
			Transcribe: func(ctx context.Context, _ Config, _ Record) (TranscriptResult, error) {
				close(started)
				<-ctx.Done()
				return TranscriptResult{Text: "already received paid response"}, nil
			},
			PollInterval: 5 * time.Millisecond,
		})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("worker did not start")
	}
	cancel()
	<-done
	rows, err := s.Records()
	if err != nil || len(rows) != 1 || rows[0].State != "done" || rows[0].Transcript != "already received paid response" {
		t.Fatalf("successful paid response lost on quit: %+v %v", rows, err)
	}
}

func TestTranscriptionPoolEnablingAutomaticStartsExistingDownloads(t *testing.T) {
	s, p := transcriptionPoolFixture(t, 0, 2, 2)
	runFakeTranscriptionPool(t, s, p)
	awaitPoolStatus(t, s, func(st TranscriptionStatus) bool { return st.Phase == "idle" && st.Queued == 0 })
	c := s.Config()
	c.AutoTranscribe = true
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	first, second := nextPoolCall(t, p), nextPoolCall(t, p)
	close(first.Release)
	close(second.Release)
	awaitPoolStatus(t, s, func(st TranscriptionStatus) bool { return st.Running == 0 && st.Queued == 0 && st.Phase == "ready" })
	if !s.Config().AutoTranscribe {
		t.Fatal("scheduler reset user's automatic setting")
	}
}

func TestTranscriptionLoopUnavailablePreservesPendingRecord(t *testing.T) {
	// An empty PATH deterministically makes local transcription unavailable,
	// regardless of installed tools, credentials, or network connectivity.
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct{ provider, state, reason string }{
		{"offline", "downloaded", "whisper-cli"},
		{"offline", "queued", "whisper-cli"},
		{"doway", "downloaded", "DOWAY"},
		{"doway", "queued", "DOWAY"},
	} {
		t.Run(tc.provider+"/"+tc.state, func(t *testing.T) {
			state := tc.state
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			c := s.Config()
			c.Provider, c.Auto, c.AutoTranscribe = tc.provider, true, state == "downloaded"
			if err := s.SaveConfig(c); err != nil {
				t.Fatal(err)
			}
			if err := s.Catalog(c.Serial, []DeviceFile{{"20260709220314", 10}}); err != nil {
				t.Fatal(err)
			}
			id := c.Serial + "-20260709220314"
			audio := filepath.Join(s.Root, "fixture.mp3")
			if err := os.WriteFile(audio, []byte("must not be submitted"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := s.Update(id, func(r *Record) { r.Audio, r.State, r.Error = audio, state, "" }); err != nil {
				t.Fatal(err)
			}
			// A newer downloaded recording is encountered first. With automatic
			// transcription off it must be skipped in favor of the manual queue.
			var downloadOnlyID string
			if state == "queued" {
				if err := s.Catalog(c.Serial, []DeviceFile{{"20260709220314", 10}, {"20260709220414", 10}}); err != nil {
					t.Fatal(err)
				}
				downloadOnlyID = c.Serial + "-20260709220414"
				if err := s.Update(downloadOnlyID, func(r *Record) { r.Audio, r.State = audio, "downloaded" }); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			s.SetStatus(Status{Phase: "connected", Battery: -1})
			done := make(chan struct{})
			go func() { defer close(done); transcriptionLoop(ctx, s) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("transcription loop did not stop after cancellation")
				}
			})
			deadline := time.Now().Add(3 * time.Second)
			for {
				status := s.TranscriptionStatus()
				if status.Phase == "waiting" {
					if status.Provider != tc.provider || status.Current != id || !strings.Contains(status.Detail, tc.reason) {
						t.Fatalf("waiting status did not explain local prerequisite: %+v", status)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("never reached waiting status: %+v", status)
				}
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("cancellation did not interrupt readiness retry delay")
			}
			r, err := s.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if r.State != state || r.Error != "" || r.Audio != audio || r.Transcript != "" || r.Provider != "" {
				t.Fatalf("unavailable local provider changed pending recording: %+v", r)
			}
			if downloadOnlyID != "" {
				r, err := s.Get(downloadOnlyID)
				if err != nil || r.State != "downloaded" || r.Error != "" || r.Audio != audio || r.Transcript != "" || r.Provider != "" {
					t.Fatalf("download-only recording changed: %+v, %v", r, err)
				}
			}
		})
	}
}

func TestLegacyConfigKeepsAutomaticTranscriptionOff(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"locale":"en","device_serial":"HD5GA00725","automatic":true,"provider":"doway","fallback_provider":"elevenlabs"}`)
	if err := os.WriteFile(s.path("config.json"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	c := s.Config()
	if !c.Auto || c.AutoTranscribe || c.Provider != "doway" {
		t.Fatalf("legacy config enabled transcription or changed provider: %+v", c)
	}
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.path("config.json"))
	if err != nil || strings.Contains(string(data), "fallback_provider") {
		t.Fatalf("removed fallback was persisted: %s, %v", data, err)
	}
}

func TestRestartDoesNotRequeueInterruptedTranscription(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := s.Config()
	if err := s.Catalog(c.Serial, []DeviceFile{{"20260709220314", 10}, {"20260709220414", 10}}); err != nil {
		t.Fatal(err)
	}
	interruptedID, queuedID := c.Serial+"-20260709220314", c.Serial+"-20260709220414"
	for id, state := range map[string]string{interruptedID: "transcribing", queuedID: "queued"} {
		if err := s.Update(id, func(r *Record) { r.Audio, r.State = "retained.mp3", state }); err != nil {
			t.Fatal(err)
		}
	}
	// Startup recovery runs before Bluetooth setup; cancellation keeps this
	// check independent of the user's recorder and network connections.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, s); err != nil {
		t.Fatal(err)
	}
	interrupted, err := s.Get(interruptedID)
	if err != nil || interrupted.State != "error" || interrupted.Audio != "retained.mp3" || !strings.Contains(interrupted.Error, "retry manually") {
		t.Fatalf("interrupted request was eligible for automatic replay: %+v, %v", interrupted, err)
	}
	queued, err := s.Get(queuedID)
	if err != nil || queued.State != "queued" {
		t.Fatalf("startup lost the explicit manual queue: %+v, %v", queued, err)
	}
}
