package xnote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func summaryFixture(t *testing.T, count int) (*Store, []Record) {
	t.Helper()
	s, _ := transcriptionPoolFixture(t, count, 0, 2)
	rows, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if err = s.Update(r.ID, func(r *Record) {
			r.State = "done"
			r.Transcript = "Transcript for " + r.ID
			r.Segments = []Segment{{Text: r.Transcript, Start: 0, End: 1, Speaker: "A", Timing: "provider"}}
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err = s.Records()
	if err != nil {
		t.Fatal(err)
	}
	return s, rows
}

func runSummaryTestLoop(t *testing.T, s *Store, generate SummaryGenerator) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		summaryLoopWithDeps(ctx, s, summaryLoopDeps{Generate: generate, PollInterval: 5 * time.Millisecond})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("summary workers did not cancel and join")
		}
	})
	return cancel, done
}

func awaitSummary(t *testing.T, s *Store, id, state string) Record {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		r, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if r.SummaryState == state {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("summary state: %+v", r)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSummaryQueueCompletionIsIndependentAndIdempotent(t *testing.T) {
	s, rows := summaryFixture(t, 1)
	id := rows[0].ID
	if err := s.QueueSummary(id); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueSummary(id); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := 0
	runSummaryTestLoop(t, s, func(context.Context, Config, Record) (SummaryResult, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return SummaryResult{Title: "Generated title", Keywords: []string{"one", "two"}, Markdown: "## Decisions\n\nA useful summary."}, nil
	})
	r := awaitSummary(t, s, id, "done")
	if r.State != "done" || r.Transcript != rows[0].Transcript || r.Provider != rows[0].Provider || r.Summary == nil || r.Title != "Generated title" || r.TitleSource != "summary" {
		t.Fatalf("summary changed transcription or was not saved: %+v", r)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir(r), "summary.md"))
	if err != nil || !strings.Contains(string(data), "A useful summary.") {
		t.Fatalf("summary export missing: %v", err)
	}
	if err = s.QueueSummary(id); err != nil {
		t.Fatal(err)
	}
	if r = awaitSummary(t, s, id, "done"); r.SummaryInputHash != summaryInputHash(r) {
		t.Fatal("completed result lost input binding")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatal("completed summary was generated twice")
	}
}

func TestSummaryProtectsManualAndLegacyTitles(t *testing.T) {
	for _, source := range []string{"local", "manual", ""} {
		t.Run(source, func(t *testing.T) {
			s, rows := summaryFixture(t, 1)
			id := rows[0].ID
			if err := s.Update(id, func(r *Record) { r.Title = "My chosen title"; r.TitleSource = source }); err != nil {
				t.Fatal(err)
			}
			if err := s.QueueSummary(id); err != nil {
				t.Fatal(err)
			}
			runSummaryTestLoop(t, s, func(context.Context, Config, Record) (SummaryResult, error) {
				return SummaryResult{Title: "Suggested title", Markdown: "Summary"}, nil
			})
			r := awaitSummary(t, s, id, "done")
			if r.Title != "My chosen title" || r.TitleSource != source || r.Summary.Title != "Suggested title" {
				t.Fatal("manual title overwritten or AI suggestion lost")
			}
		})
	}
}

func TestSummaryChangedTranscriptOrSegmentsDiscardsResultKeepsPrevious(t *testing.T) {
	for _, change := range []string{"text", "segments"} {
		t.Run(change, func(t *testing.T) {
			s, rows := summaryFixture(t, 1)
			id := rows[0].ID
			if err := s.Update(id, func(r *Record) { r.Summary = &SummaryResult{Title: "Previous", Markdown: "Previous summary"} }); err != nil {
				t.Fatal(err)
			}
			if err := s.QueueSummary(id); err != nil {
				t.Fatal(err)
			}
			started, release := make(chan struct{}), make(chan struct{})
			runSummaryTestLoop(t, s, func(ctx context.Context, _ Config, _ Record) (SummaryResult, error) {
				close(started)
				select {
				case <-release:
					return SummaryResult{Title: "Stale new title", Markdown: "Stale summary"}, nil
				case <-ctx.Done():
					return SummaryResult{}, ctx.Err()
				}
			})
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("summary did not start")
			}
			if err := s.Update(id, func(r *Record) {
				if change == "text" {
					r.Transcript = "Changed text"
				} else {
					r.Segments[0].Speaker = "B"
				}
			}); err != nil {
				t.Fatal(err)
			}
			close(release)
			r := awaitSummary(t, s, id, "error")
			if r.Summary == nil || r.Summary.Markdown != "Previous summary" || !strings.Contains(r.SummaryError, "Transcript changed") || r.State != "done" {
				t.Fatalf("stale output overwrote record: %+v", r)
			}
			data, err := os.ReadFile(filepath.Join(s.Dir(r), "summary.md"))
			if err != nil || strings.Contains(string(data), "Stale") || !strings.Contains(string(data), "Previous summary") {
				t.Fatal("prior summary file was overwritten")
			}
		})
	}
}

func TestSummaryQueueRejectsUnavailableAndRunningIsNotDuplicated(t *testing.T) {
	s, rows := summaryFixture(t, 1)
	id := rows[0].ID
	for _, state := range []string{"empty", "trashed", "transcribing", "queued", "error"} {
		if err := s.Update(id, func(r *Record) {
			r.State = "done"
			r.Trashed = false
			r.Transcript = "text"
			switch state {
			case "empty":
				r.Transcript = ""
			case "trashed":
				r.Trashed = true
			case "transcribing":
				r.State = "transcribing"
			case "queued", "error":
				r.State = state
			}
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.QueueSummary(id); err == nil {
			t.Fatalf("queued unavailable transcript: %s", state)
		}
	}
	if err := s.Update(id, func(r *Record) {
		r.State = "done"
		r.Transcript = "text"
		r.SummaryState = "running"
		r.SummaryInputHash = "original"
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.QueueSummary(id); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(id)
	if err != nil || r.SummaryState != "running" || r.SummaryInputHash != "original" {
		t.Fatal("active summary was requeued")
	}
}

func TestSummaryAutoOnlyQueuesFutureSuccessfulTranscription(t *testing.T) {
	s, rows := summaryFixture(t, 2)
	c := s.Config()
	c.AutoSummary = true
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		summaryLoopWithDeps(ctx, s, summaryLoopDeps{Generate: func(context.Context, Config, Record) (SummaryResult, error) {
			t.Error("automatic summary backfilled historical transcript")
			return SummaryResult{}, errors.New("unexpected")
		}, PollInterval: 5 * time.Millisecond})
	}()
	deadline := time.Now().Add(time.Second)
	for s.SummaryStatus().Concurrency != 2 {
		if time.Now().After(deadline) {
			t.Fatal("summary loop did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	for _, row := range rows {
		r, err := s.Get(row.ID)
		if err != nil || r.SummaryState != "" {
			t.Fatal("enabling auto queued history")
		}
	}
	// Exercise the real successful-transcription hook with an injected provider.
	id := rows[0].ID
	if err := s.Update(id, func(r *Record) { r.State = "queued" }); err != nil {
		t.Fatal(err)
	}
	p := &fakeTranscriptionPool{Started: make(chan poolCall, 8), calls: map[string]int{}}
	runFakeTranscriptionPool(t, s, p)
	call := nextPoolCall(t, p)
	close(call.Release)
	r := awaitSummary(t, s, id, "queued")
	if r.SummaryInputHash != summaryInputHash(r) {
		t.Fatal("future result queued with wrong input")
	}
	old, err := s.Get(rows[1].ID)
	if err != nil || old.SummaryState != "" {
		t.Fatal("transcription completion backfilled another record")
	}
}

func TestSummaryFailureDoesNotRetryAndCancellationJoins(t *testing.T) {
	s, rows := summaryFixture(t, 2)
	for _, r := range rows {
		if err := s.QueueSummary(r.ID); err != nil {
			t.Fatal(err)
		}
	}
	started := make(chan string, 2)
	cancel, done := runSummaryTestLoop(t, s, func(ctx context.Context, _ Config, r Record) (SummaryResult, error) {
		started <- r.ID
		<-ctx.Done()
		return SummaryResult{}, ctx.Err()
	})
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("parallel summary workers did not start")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("summary cancellation did not join")
	}
	for _, row := range rows {
		r := awaitSummary(t, s, row.ID, "error")
		if !strings.Contains(r.SummaryError, "retry manually") || r.State != "done" {
			t.Fatal("summary cancellation changed transcription or permits auto replay")
		}
	}
	ctx, stop := context.WithCancel(context.Background())
	again := make(chan struct{})
	go func() {
		defer close(again)
		summaryLoopWithDeps(ctx, s, summaryLoopDeps{Generate: func(context.Context, Config, Record) (SummaryResult, error) {
			t.Error("failed summary was replayed")
			return SummaryResult{}, nil
		}, PollInterval: 5 * time.Millisecond})
	}()
	deadline := time.Now().Add(time.Second)
	for s.SummaryStatus().Phase == "stopped" {
		if time.Now().After(deadline) {
			t.Fatal("summary restart not observed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	<-again
}

func TestSummaryPoolDynamicConcurrencyIsBounded(t *testing.T) {
	s, rows := summaryFixture(t, 4)
	c := s.Config()
	c.SummaryConcurrency = 1
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if err := s.QueueSummary(r.ID); err != nil {
			t.Fatal(err)
		}
	}
	started := make(chan poolCall, 8)
	var mu sync.Mutex
	running, peak := 0, 0
	runSummaryTestLoop(t, s, func(ctx context.Context, c Config, r Record) (SummaryResult, error) {
		mu.Lock()
		running++
		if running > peak {
			peak = running
		}
		mu.Unlock()
		defer func() { mu.Lock(); running--; mu.Unlock() }()
		call := poolCall{Record: r, Config: c, Release: make(chan struct{})}
		started <- call
		select {
		case <-call.Release:
			return SummaryResult{Title: "Title", Markdown: "Summary"}, nil
		case <-ctx.Done():
			return SummaryResult{}, ctx.Err()
		}
	})
	next := func() poolCall {
		select {
		case call := <-started:
			return call
		case <-time.After(2 * time.Second):
			t.Fatal("summary worker did not start")
			return poolCall{}
		}
	}
	status := func(match func(SummaryStatus) bool) {
		deadline := time.Now().Add(2 * time.Second)
		for {
			st := s.SummaryStatus()
			if match(st) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("summary pool status: %+v", st)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	first := next()
	c.SummaryConcurrency = 2
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	second := next()
	c.SummaryConcurrency = 1
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	status(func(st SummaryStatus) bool { return st.Concurrency == 1 && st.Running == 2 })
	close(first.Release)
	status(func(st SummaryStatus) bool { return st.Running == 1 })
	select {
	case <-started:
		t.Fatal("summary pool exceeded decreased limit")
	default:
	}
	close(second.Release)
	third := next()
	close(third.Release)
	fourth := next()
	close(fourth.Release)
	status(func(st SummaryStatus) bool { return st.Running == 0 && st.Queued == 0 })
	mu.Lock()
	defer mu.Unlock()
	if peak != 2 {
		t.Fatalf("summary peak concurrency=%d", peak)
	}
}

func TestSummaryBatchUsesConfirmedSnapshotAndNeverRetriesFailures(t *testing.T) {
	s, rows := summaryFixture(t, 4)
	if err := s.Update(rows[1].ID, func(r *Record) {
		r.SummaryState = "done"
		r.SummaryInputHash = summaryInputHash(*r)
		r.SummaryOptions = summaryOptionsKey(s.Config())
		r.Summary = &SummaryResult{Markdown: "Done"}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(rows[2].ID, func(r *Record) { r.SummaryState = "error"; r.SummaryError = "prior ambiguous request" }); err != nil {
		t.Fatal(err)
	}
	ids := []string{rows[0].ID, rows[1].ID, rows[2].ID, rows[0].ID}
	count, err := s.QueueSummaries(ids)
	if err != nil || count != 1 {
		t.Fatalf("batch count=%d err=%v", count, err)
	}
	if count, err = s.QueueSummaries(ids); err != nil || count != 0 {
		t.Fatal("batch repeated an existing request")
	}
	for i, row := range rows {
		r, err := s.Get(row.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"queued", "done", "error", ""}[i]
		if r.SummaryState != want {
			t.Fatalf("snapshot boundary changed %d: %+v", i, r)
		}
	}
	// An explicit per-record retry binds the user's current transcript instead.
	if err = s.Update(rows[2].ID, func(r *Record) { r.Transcript = "Edited current transcript" }); err != nil {
		t.Fatal(err)
	}
	if err = s.QueueSummary(rows[2].ID); err != nil {
		t.Fatal(err)
	}
	r := awaitSummary(t, s, rows[2].ID, "queued")
	if r.SummaryInputHash != summaryInputHash(r) || r.SummaryError != "" {
		t.Fatal("manual retry did not bind current text")
	}
}

func TestSummaryManualRenameWhileGeneratingIsPreserved(t *testing.T) {
	s, rows := summaryFixture(t, 1)
	id := rows[0].ID
	if err := s.QueueSummary(id); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	runSummaryTestLoop(t, s, func(ctx context.Context, _ Config, _ Record) (SummaryResult, error) {
		close(started)
		select {
		case <-release:
			return SummaryResult{Title: "Generated", Markdown: "Summary"}, nil
		case <-ctx.Done():
			return SummaryResult{}, ctx.Err()
		}
	})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("summary did not start")
	}
	if err := s.Update(id, func(r *Record) { r.Title, r.TitleSource = "Renamed during request", "local" }); err != nil {
		t.Fatal(err)
	}
	close(release)
	r := awaitSummary(t, s, id, "done")
	if r.Title != "Renamed during request" || r.TitleSource != "local" || r.Summary.Title != "Generated" {
		t.Fatal("concurrent manual rename was overwritten")
	}
}

func TestSummaryCrashRecoveryDoesNotChangeTranscriptionOrRequeue(t *testing.T) {
	s, rows := summaryFixture(t, 1)
	id := rows[0].ID
	if err := s.Update(id, func(r *Record) { r.SummaryState = "running" }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, s); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(id)
	if err != nil || r.State != "done" || r.Transcript != rows[0].Transcript || r.SummaryState != "error" || !strings.Contains(r.SummaryError, "retry manually") {
		t.Fatalf("summary crash recovery changed recording: %+v %v", r, err)
	}
}

func TestSummaryUsageWarningKeepsCompletedVisibleResult(t *testing.T) {
	s, rows := summaryFixture(t, 1)
	id := rows[0].ID
	if err := s.QueueSummary(id); err != nil {
		t.Fatal(err)
	}
	runSummaryTestLoop(t, s, func(context.Context, Config, Record) (SummaryResult, error) {
		return SummaryResult{Title: "Generated", Keywords: []string{"note"}, Markdown: "Visible result", Warning: "Usage report not confirmed"}, nil
	})
	r := awaitSummary(t, s, id, "done")
	if r.Summary == nil || r.Summary.Markdown != "Visible result" || r.SummaryError != "Usage report not confirmed" || r.State != "done" {
		t.Fatal("usage warning hid a successful summary")
	}
}
