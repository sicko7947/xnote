package xnote

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type SummaryResult struct {
	Title    string   `json:"title"`
	Keywords []string `json:"keywords,omitempty"`
	Markdown string   `json:"markdown"`
	Warning  string   `json:"warning,omitempty"`
}

// Include the transcript's speaker/timing context, but not its editable title.
func summaryInputHash(r Record) string {
	data, _ := json.Marshal(struct {
		Transcript string    `json:"transcript"`
		Segments   []Segment `json:"segments"`
	}{r.Transcript, r.Segments})
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func (s *Store) QueueSummary(id string) error {
	var queueErr error
	err := s.Update(id, func(r *Record) {
		if r.Trashed || r.State != "done" || strings.TrimSpace(r.Transcript) == "" {
			queueErr = errors.New("AI summary requires an available, completed transcript")
			return
		}
		if r.SummaryState == "running" {
			return
		}
		hash := summaryInputHash(*r)
		if r.SummaryInputHash == hash && (r.SummaryState == "queued" || r.SummaryState == "done") {
			return
		}
		r.SummaryState, r.SummaryError, r.SummaryInputHash = "queued", "", hash
	})
	if err != nil {
		return err
	}
	return queueErr
}

func SummaryEligibleForBatch(r Record) bool {
	if r.Trashed || r.State != "done" || strings.TrimSpace(r.Transcript) == "" {
		return false
	}
	switch r.SummaryState {
	case "":
		return true
	case "done":
		return r.SummaryInputHash != summaryInputHash(r)
	default:
		return false
	}
}

// Enqueue only the exact IDs from the caller's confirmed snapshot. Existing
// failures require a separate per-record retry, even when batch work is chosen.
func (s *Store) QueueSummaries(ids []string) (int, error) {
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	queued := 0
	err := s.mutate(func() error {
		rows, err := s.Records()
		if err != nil {
			return err
		}
		for _, r := range rows {
			if !wanted[r.ID] || !SummaryEligibleForBatch(r) {
				continue
			}
			r.SummaryState, r.SummaryError, r.SummaryInputHash = "queued", "", summaryInputHash(r)
			if err = s.save(r); err != nil {
				return err
			}
			queued++
		}
		return nil
	})
	return queued, err
}

// Called only in the successful transcription write, never while scanning old
// records or enabling AutoSummary. Existing queued/running work takes priority.
func queueAutomaticSummary(r *Record, c Config) {
	if !c.AutoSummary || r.Trashed || strings.TrimSpace(r.Transcript) == "" || r.SummaryState == "queued" || r.SummaryState == "running" {
		return
	}
	hash := summaryInputHash(*r)
	if r.SummaryInputHash == hash && (r.SummaryState == "done" || r.SummaryState == "error") {
		return
	}
	r.SummaryState, r.SummaryError, r.SummaryInputHash = "queued", "", hash
}

func summaryMayReplaceTitle(r Record) bool {
	if r.TitleSource == "local" || r.TitleSource == "manual" {
		return false
	}
	if r.TitleSource == "summary" {
		return true
	}
	if r.TitleSource != "" {
		return false
	}
	when, err := time.Parse(time.RFC3339, r.RecordedAt)
	return err == nil && r.Title == when.Format("2006-01-02 15:04:05")
}
