package xnote

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const mindmapRaw = `{"title":"Launch","keywords":["release"],"markdown":"## Plan\nShip next week.","mindmap":{"title":"Launch","branches":[{"title":"Testing","points":["Bob tests the release"]}]}}`

func TestMindmapSettingsDefaultAndIndependentControls(t *testing.T) {
	d, press := keyboardUI(t)
	if c := d.s.Config(); c.SummaryDisabled || c.MindmapEnabled {
		t.Fatal("expected summary only by default")
	}
	d.summarySettings()
	f := d.modalContent.(*tview.Form)
	f.GetFormItem(5).(*optionCheckbox).SetChecked(false)
	f.GetFormItem(6).(*optionCheckbox).SetChecked(true)
	press(tcell.KeyCtrlS, 0)
	c := d.s.Config()
	if !c.SummaryDisabled || !c.MindmapEnabled {
		t.Fatal("independent options not saved", c)
	}
	d.summarySettings()
	f = d.modalContent.(*tview.Form)
	f.GetFormItem(6).(*optionCheckbox).SetChecked(false)
	press(tcell.KeyEscape, 0)
	if d.s.Config() != c {
		t.Fatal("cancel saved changes")
	}
}
func TestMindmapPromptAndParserRespectOutputSelection(t *testing.T) {
	for _, c := range []Config{{}, {MindmapEnabled: true}, {SummaryDisabled: true, MindmapEnabled: true}} {
		msgs := dowaySummaryMessagesForOptions("template", "en", "source", c)
		prompt := msgs[0].Content
		if c.MindmapEnabled && !strings.Contains(prompt, "Also return mindmap") {
			t.Fatal(prompt)
		}
		if !c.MindmapEnabled && !strings.Contains(prompt, "Mindmap is disabled") {
			t.Fatal(prompt)
		}
		result, e := parseDOWAYSummaryResultForOptions(mindmapRaw, c)
		if e != nil {
			t.Fatal(e)
		}
		if (result.Mindmap != nil) != c.MindmapEnabled {
			t.Fatal("mindmap choice ignored")
		}
		if (result.Markdown == "") != c.SummaryDisabled {
			t.Fatal("summary choice ignored")
		}
	}
	if _, e := parseDOWAYSummaryResultForOptions(fakeDOWAYSummaryRaw, Config{MindmapEnabled: true}); e == nil {
		t.Fatal("missing requested mindmap accepted")
	}
	if _, e := parseDOWAYSummaryResultForOptions(strings.Replace(mindmapRaw, "Bob tests the release", strings.Repeat("x", 2001), 1), Config{MindmapEnabled: true}); e == nil {
		t.Fatal("oversize mindmap accepted")
	}
}
func TestMindmapQueueSelectionSnapshotAndDisabledNoCalls(t *testing.T) {
	s, rows := summaryFixture(t, 1)
	id := rows[0].ID
	c := s.Config()
	c.SummaryDisabled = true
	s.SaveConfig(c)
	if e := s.QueueSummary(id); e == nil {
		t.Fatal("both disabled still queued")
	}
	c.MindmapEnabled = true
	s.SaveConfig(c)
	if e := s.QueueSummary(id); e != nil {
		t.Fatal(e)
	}
	c.SummaryDisabled = false
	c.MindmapEnabled = false
	s.SaveConfig(c)
	cancel, done := runSummaryTestLoop(t, s, func(_ context.Context, c Config, _ Record) (SummaryResult, error) {
		if !c.SummaryDisabled || !c.MindmapEnabled {
			t.Error("queued choice was not preserved")
		}
		return SummaryResult{Title: "Launch", Mindmap: &Mindmap{Title: "Launch", Branches: []MindmapBranch{{Title: "Testing", Points: []string{"Bob"}}}}}, nil
	})
	r := awaitSummary(t, s, id, "done")
	if r.Summary.Markdown != "" || r.Summary.Mindmap == nil {
		t.Fatal("mindmap-only job failed")
	}
	cancel()
	<-done
	// Explicitly asking for a different output queues a new task.
	if e := s.QueueSummary(id); e != nil {
		t.Fatal(e)
	}
}
func TestDOWAYMindmapCacheSeparatedFromSummaryOnly(t *testing.T) {
	f := newDOWAYSummaryFixture(t)
	first, e := f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d)
	if e != nil || first.Markdown == "" {
		t.Fatal(e)
	}
	f.c.MindmapEnabled = true
	f.d.Post = func(_ context.Context, path string, body map[string]any) (json.RawMessage, error) {
		f.reports++
		var job dowaySummaryJob
		cache := strings.TrimSuffix(f.jobPath(), ".json") + "-summary-mindmap.json"
		if err := readJSON(cache, &job); err != nil || job.Phase != "reporting" || job.RawOutput == "" || path != "/api/player/add_summary_record" {
			t.Fatal("mindmap report preceded durable response", err)
		}
		return json.RawMessage(`null`), nil
	}
	f.d.Chat = func(_ context.Context, _ dowayChatConfig, m []dowayChatMessage) (string, error) {
		f.chats++
		if !strings.Contains(m[0].Content, "Also return mindmap") {
			t.Error("missing mindmap prompt")
		}
		return mindmapRaw, nil
	}
	both, e := f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d)
	if e != nil || both.Mindmap == nil {
		t.Fatal(both, e)
	}
	if f.chats != 2 || f.reports != 2 {
		t.Fatal("separate selection reused old result", f.chats, f.reports)
	}
	again, e := f.s.generateDOWAYSummary(context.Background(), f.c, f.r, f.d)
	if e != nil || again.Mindmap == nil || f.chats != 2 || f.reports != 2 {
		t.Fatal("cached mindmap was resubmitted", e)
	}
}
func TestMindmapRecordCacheReturnsDeepCopies(t *testing.T) {
	s, rows := summaryFixture(t, 1)
	id := rows[0].ID
	result, e := parseDOWAYSummaryResultForOptions(mindmapRaw, Config{MindmapEnabled: true})
	if e != nil {
		t.Fatal(e)
	}
	s.Update(id, func(r *Record) { r.Summary = &result })
	r, _ := s.Get(id)
	r.Summary.Mindmap.Branches[0].Points[0] = "mutated"
	fresh, _ := s.Get(id)
	if fresh.Summary.Mindmap.Branches[0].Points[0] == "mutated" {
		t.Fatal("shared cache pointer")
	}
	data, _ := json.Marshal(fresh.Summary)
	if !strings.Contains(string(data), "mindmap") {
		t.Fatal("mindmap not persisted")
	}
}
