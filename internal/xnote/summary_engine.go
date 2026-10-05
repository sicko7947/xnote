package xnote

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

type SummaryGenerator func(context.Context, Config, Record) (SummaryResult, error)

type SummaryActive struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Phase string `json:"phase"`
}

type SummaryStatus struct {
	Phase       string          `json:"phase"`
	Current     string          `json:"current,omitempty"`
	Detail      string          `json:"detail,omitempty"`
	UpdatedAt   string          `json:"updated_at,omitempty"`
	Running     int             `json:"running"`
	Queued      int             `json:"queued"`
	Concurrency int             `json:"concurrency"`
	Active      []SummaryActive `json:"active,omitempty"`
}

func (s *Store) SummaryStatus() (st SummaryStatus) {
	if readJSON(s.path(".work/summary-status.json"), &st) != nil {
		st.Phase = "idle"
	}
	return
}

func (s *Store) writeSummaryStatus(st SummaryStatus) {
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	_ = atomicJSON(s.path(".work/summary-status.json"), st)
}

func RunSummaryLoop(ctx context.Context, s *Store, generate SummaryGenerator) {
	summaryLoopWithDeps(ctx, s, summaryLoopDeps{Generate: generate})
}

type summaryLoopDeps struct {
	Generate     SummaryGenerator
	PollInterval time.Duration
}

type summaryFinished struct {
	ID  string
	Err error
}

func summaryLoopWithDeps(ctx context.Context, s *Store, d summaryLoopDeps) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if d.PollInterval <= 0 {
		d.PollInterval = 500 * time.Millisecond
	}
	ticker := time.NewTicker(d.PollInterval)
	defer ticker.Stop()
	active := map[string]SummaryActive{}
	finished := make(chan summaryFinished, 8)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		workers.Wait()
		queued := 0
		if rows, err := s.Records(); err == nil {
			for _, r := range rows {
				if r.SummaryState == "queued" && !r.Trashed {
					queued++
				}
			}
		}
		s.writeSummaryStatus(SummaryStatus{Phase: "stopped", Queued: queued, Concurrency: EffectiveSummaryConcurrency(s.Config())})
	}()
	last := summaryFinished{}
	phase := "idle"
	for ctx.Err() == nil {
		c := s.Config()
		limit := EffectiveSummaryConcurrency(c)
		rows, err := s.Records()
		if err != nil {
			phase, last.Err = "error", err
		}
		pending := make([]Record, 0)
		for _, r := range rows {
			if _, running := active[r.ID]; !running && !r.Trashed && r.SummaryState == "queued" {
				pending = append(pending, r)
			}
		}
		queued := len(pending)
		if d.Generate != nil {
			for _, candidate := range pending {
				if len(active) >= limit || ctx.Err() != nil {
					break
				}
				r := candidate
				claimed := false
				var inputErr error
				claimErr := s.Update(r.ID, func(current *Record) {
					if ctx.Err() != nil || s.Config() != c || current.Trashed || current.SummaryState != "queued" {
						return
					}
					if current.State != "done" {
						return
					}
					if strings.TrimSpace(current.Transcript) == "" || summaryInputHash(*current) != current.SummaryInputHash {
						inputErr = errors.New("Transcript changed before AI processing; queue it again")
						current.SummaryState, current.SummaryError = "error", inputErr.Error()
						return
					}
					current.SummaryState, current.SummaryError = "running", ""
					r, claimed = *current, true
				})
				if claimErr != nil {
					phase, last.Err = "error", claimErr
					continue
				}
				if inputErr != nil {
					phase, last = "error", summaryFinished{ID: r.ID, Err: inputErr}
					queued--
				}
				if !claimed {
					continue
				}
				active[r.ID] = SummaryActive{ID: r.ID, Title: r.Title, Phase: "running"}
				queued--
				phase, last = "ready", summaryFinished{}
				workers.Add(1)
				go func(r Record, c Config) {
					defer workers.Done()
					result, generateErr := d.Generate(ctx, c, r)
					if generateErr != nil && ctx.Err() != nil {
						generateErr = errors.New("AI processing interrupted; retry manually to avoid duplicate requests")
					}
					if generateErr == nil && strings.TrimSpace(result.Markdown) == "" {
						generateErr = errors.New("AI processing returned an empty summary")
					}
					saveErr := s.Update(r.ID, func(current *Record) {
						if current.SummaryState != "running" || current.SummaryInputHash != r.SummaryInputHash {
							return
						}
						if current.Trashed || current.State != "done" || summaryInputHash(*current) != r.SummaryInputHash {
							generateErr = errors.New("Transcript changed during AI processing; queue it again")
						}
						if generateErr != nil {
							current.SummaryState, current.SummaryError = "error", generateErr.Error()
							return
						}
						if strings.TrimSpace(result.Title) != "" && summaryMayReplaceTitle(*current) {
							current.Title, current.TitleSource = result.Title, "summary"
						}
						current.Summary = &result
						current.SummaryState, current.SummaryError = "done", result.Warning
					})
					if saveErr != nil {
						generateErr = saveErr
					}
					finished <- summaryFinished{ID: r.ID, Err: generateErr}
				}(r, c)
			}
		}
		st := SummaryStatus{Phase: phase, Running: len(active), Queued: queued, Concurrency: limit}
		if last.Err != nil {
			st.Current, st.Detail = last.ID, last.Err.Error()
		}
		for _, item := range active {
			st.Active = append(st.Active, item)
		}
		sort.Slice(st.Active, func(i, j int) bool { return st.Active[i].ID < st.Active[j].ID })
		if len(st.Active) > 0 {
			st.Phase, st.Current, st.Detail = "running", st.Active[0].ID, ""
		}
		if d.Generate == nil && queued > 0 {
			st.Phase, st.Detail = "waiting", "AI processing backend is not available"
		} else if len(st.Active) == 0 && queued > 0 {
			st.Phase, st.Detail = "waiting", "Waiting for transcription to complete"
		}
		s.writeSummaryStatus(st)
		select {
		case <-ctx.Done():
			return
		case result := <-finished:
			delete(active, result.ID)
			last, phase = result, "ready"
			if result.Err != nil {
				phase = "error"
			}
		case <-ticker.C:
		}
	}
}
