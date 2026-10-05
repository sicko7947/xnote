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
	Title       string   `json:"title"`
	Keywords    []string `json:"keywords,omitempty"`
	Markdown    string   `json:"markdown"`
	Warning     string   `json:"warning,omitempty"`
	Mindmap     *Mindmap `json:"mindmap,omitempty"`
	Model       string   `json:"model,omitempty"`
	GeneratedAt string   `json:"generated_at,omitempty"`
}

type MindmapBranch struct {
	Title  string   `json:"title"`
	Points []string `json:"points"`
}
type Mindmap struct {
	Title    string          `json:"title"`
	Branches []MindmapBranch `json:"branches"`
}

func summaryOptionsKey(c Config) string {
	if c.SummaryDisabled {
		if c.MindmapEnabled {
			return "mindmap"
		}
		return "none"
	}
	if c.MindmapEnabled {
		return "summary-mindmap"
	}
	return ""
}
func summaryConfigForOptions(c Config, options string) Config {
	c.SummaryDisabled = options == "mindmap" || options == "none"
	c.MindmapEnabled = options == "mindmap" || options == "summary-mindmap"
	return c
}
func validateMindmap(m *Mindmap) error {
	if m == nil || strings.TrimSpace(m.Title) == "" || len(m.Title) > 500 || len(m.Branches) == 0 || len(m.Branches) > 30 {
		return errors.New("AI returned an invalid mindmap")
	}
	for _, b := range m.Branches {
		if strings.TrimSpace(b.Title) == "" || len(b.Title) > 500 || len(b.Points) > 30 {
			return errors.New("AI returned an invalid mindmap branch")
		}
		for _, p := range b.Points {
			if strings.TrimSpace(p) == "" || len(p) > 2000 {
				return errors.New("AI returned an invalid mindmap point")
			}
		}
	}
	return nil
}
func mindmapMarkdown(m *Mindmap) string {
	if m == nil {
		return ""
	}
	var out strings.Builder
	out.WriteString("# " + strings.Join(strings.Fields(m.Title), " ") + "\n\n")
	for _, b := range m.Branches {
		out.WriteString("- " + strings.Join(strings.Fields(b.Title), " ") + "\n")
		for _, p := range b.Points {
			out.WriteString("  - " + strings.Join(strings.Fields(p), " ") + "\n")
		}
	}
	return out.String()
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
	options := summaryOptionsKey(s.Config())
	if options == "none" {
		return errors.New("enable Summary or Mindmap in AI settings first")
	}
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
		if r.SummaryInputHash == hash && r.SummaryOptions == options && (r.SummaryState == "queued" || r.SummaryState == "done") {
			return
		}
		r.SummaryState, r.SummaryError, r.SummaryInputHash = "queued", "", hash
		r.SummaryOptions = options
	})
	if err != nil {
		return err
	}
	return queueErr
}

func SummaryEligibleForBatch(r Record, configs ...Config) bool {
	c := Config{}
	if len(configs) > 0 {
		c = configs[0]
	}
	options := summaryOptionsKey(c)
	if options == "none" {
		return false
	}
	if r.Trashed || r.State != "done" || strings.TrimSpace(r.Transcript) == "" {
		return false
	}
	switch r.SummaryState {
	case "":
		return true
	case "done":
		return r.SummaryInputHash != summaryInputHash(r) || r.SummaryOptions != options
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
		c := s.Config()
		rows, err := s.Records()
		if err != nil {
			return err
		}
		for _, r := range rows {
			if !wanted[r.ID] || !SummaryEligibleForBatch(r, c) {
				continue
			}
			r.SummaryState, r.SummaryError, r.SummaryInputHash = "queued", "", summaryInputHash(r)
			r.SummaryOptions = summaryOptionsKey(c)
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
	if !c.AutoSummary || summaryOptionsKey(c) == "none" || r.Trashed || strings.TrimSpace(r.Transcript) == "" || r.SummaryState == "queued" || r.SummaryState == "running" {
		return
	}
	hash := summaryInputHash(*r)
	if r.SummaryInputHash == hash && (r.SummaryState == "done" || r.SummaryState == "error") {
		return
	}
	r.SummaryState, r.SummaryError, r.SummaryInputHash = "queued", "", hash
	r.SummaryOptions = summaryOptionsKey(c)
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
