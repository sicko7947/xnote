package xnote

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestSummaryConfigDefaultsAndValidation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := s.Config()
	if c.AutoSummary || c.SummaryThinking || c.SummaryConcurrency != 2 || c.SummaryLanguage != "" {
		t.Fatalf("unexpected summary defaults: %+v", c)
	}
	for _, tc := range []struct {
		stored, effective int
		valid             bool
	}{{0, 2, true}, {1, 1, true}, {8, 8, true}, {-1, 1, false}, {9, 8, false}} {
		c.SummaryConcurrency = tc.stored
		if got := EffectiveSummaryConcurrency(c); got != tc.effective {
			t.Fatalf("stored %d: effective concurrency %d", tc.stored, got)
		}
		if err := s.SaveConfig(c); (err == nil) != tc.valid {
			t.Fatalf("stored %d: unexpected validation result %v", tc.stored, err)
		}
	}
	c.SummaryConcurrency = 2
	for _, language := range []string{"", "zh-CN", "en", "ja"} {
		c.SummaryLanguage = language
		if err := s.SaveConfig(c); err != nil {
			t.Fatal(err)
		}
	}
	c.SummaryLanguage = "unsupported"
	if err := s.SaveConfig(c); err == nil {
		t.Fatal("accepted unsupported summary output language")
	}
}

func TestSummaryExportPreservesTranscript(t *testing.T) {
	d, _ := keyboardUI(t)
	id := d.selected
	if err := d.s.Update(id, func(r *Record) { r.Transcript, r.Provider, r.State = "Original transcript", "elevenlabs", "done" }); err != nil {
		t.Fatal(err)
	}
	r, err := d.s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(d.s.Dir(r), "transcript.md")
	before, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.s.Update(id, func(r *Record) {
		r.SummaryState = "done"
		r.SummaryInputHash = summaryInputHash(*r)
		r.Summary = &SummaryResult{Title: "Planning\nmeeting", Keywords: []string{"budget", "next\nsteps"}, Markdown: "## Decisions\n\nKeep the original transcript.", Warning: "Usage report not confirmed."}
	}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(transcriptPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("summary export modified transcript.md", err)
	}
	data, err := os.ReadFile(filepath.Join(d.s.Dir(r), "summary.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"# Planning meeting", "Keywords: budget, next steps", "## Decisions", "Keep the original transcript.", "> Note: Usage report not confirmed."} {
		if !strings.Contains(string(data), part) {
			t.Fatalf("summary export omitted %q: %s", part, data)
		}
	}

}

func TestSummarySettingsCancelValidationAndFreshConfig(t *testing.T) {
	d, press := keyboardUI(t)
	original := d.s.Config()
	open := func() *tview.Form {
		press(tcell.KeyRune, 's')
		press(tcell.KeyRune, '6')
		return d.modalContent.(*tview.Form)
	}
	form := open()
	form.GetFormItem(1).(*optionCheckbox).SetChecked(true)
	form.GetFormItem(2).(*tview.DropDown).SetCurrentOption(2)
	form.GetFormItem(3).(*tview.InputField).SetText("3")
	press(tcell.KeyEscape, 0)
	if d.s.Config() != original {
		t.Fatal("cancel saved summary settings")
	}
	form = open()
	input := form.GetFormItem(3).(*tview.InputField)
	for _, invalid := range []string{"", "0", "9", "1.5"} {
		input.SetText(invalid)
		press(tcell.KeyCtrlS, 0)
		if !d.modal || d.s.Config() != original {
			t.Fatalf("invalid summary concurrency %q was accepted", invalid)
		}
	}
	fresh := d.s.Config()
	fresh.Provider, fresh.AutoTranscribe, fresh.TranscriptionConcurrency, fresh.DeviceID = "elevenlabs", true, 7, "fresh-device"
	if err := d.s.SaveConfig(fresh); err != nil {
		t.Fatal(err)
	}
	checkbox := form.GetFormItem(1).(*optionCheckbox)
	form.SetFocus(1)
	d.app.SetFocus(form)
	press(tcell.KeyRune, ' ')
	if !checkbox.IsChecked() {
		t.Fatal("automatic summary option did not toggle")
	}
	form.GetFormItem(2).(*tview.DropDown).SetCurrentOption(2)
	input.SetText("3")
	press(tcell.KeyCtrlS, 0)
	fresh.AutoSummary, fresh.SummaryLanguage, fresh.SummaryConcurrency = true, "en", 3
	if d.modal || d.s.Config() != fresh {
		t.Fatalf("summary settings overwrote unrelated configuration: %+v", d.s.Config())
	}
	rows, err := d.s.Records()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.SummaryState != "" {
			t.Fatal("enabling automatic summary enqueued historical recordings")
		}
	}
}

func TestSummaryMenuQueuesSelectedAndKeepsMatchingResult(t *testing.T) {
	d, press := keyboardUI(t)
	id := d.selected
	if err := d.s.Update(id, func(r *Record) { r.State, r.Transcript = "done", "Discuss budget and next steps." }); err != nil {
		t.Fatal(err)
	}
	d.refresh()
	press(tcell.KeyRune, 'm')
	press(tcell.KeyRune, 'i')
	r, err := d.s.Get(id)
	if err != nil || r.SummaryState != "queued" || d.s.Config().AutoSummary {
		t.Fatalf("manual summary queue changed automatic mode or failed: %+v, %v", r, err)
	}
	if err := d.s.Update(id, func(r *Record) {
		r.SummaryState = "done"
		r.Summary = &SummaryResult{Title: "Budget", Markdown: "Budget summary"}
	}); err != nil {
		t.Fatal(err)
	}
	d.refresh()
	press(tcell.KeyRune, 'm')
	press(tcell.KeyRune, 'i')
	r, err = d.s.Get(id)
	if err != nil || r.SummaryState != "done" || r.Summary.Markdown != "Budget summary" {
		t.Fatalf("matching summary was unnecessarily queued again: %+v, %v", r, err)
	}
}

func TestSummaryDetailEscapesContentBeforeTranscript(t *testing.T) {
	d, _ := keyboardUI(t)
	id := d.selected
	summary := SummaryResult{Title: "[red]AI title[-]", Keywords: []string{"[blue]keyword[-]"}, Markdown: "## Summary\n[green]Summary body[-]", Warning: "[yellow]Usage report unconfirmed[-]"}
	if err := d.s.Update(id, func(r *Record) {
		r.State, r.Transcript, r.SummaryState, r.Summary = "done", "Original transcript text", "done", &summary
	}); err != nil {
		t.Fatal(err)
	}
	d.refresh()
	d.showDetail()
	text := d.detail.GetText(false)
	for _, part := range []string{summary.Title, summary.Keywords[0], summary.Markdown, summary.Warning} {
		if !strings.Contains(text, tview.Escape(part)) {
			t.Fatalf("summary content was not rendered as literal text: %s", text)
		}
	}
	if strings.Index(text, tview.Escape(summary.Markdown)) > strings.Index(text, "Original transcript text") {
		t.Fatal("summary was not shown before the transcript")
	}
}

func TestBatchSummaryConfirmationUsesDOWAYAndConfirmedSnapshot(t *testing.T) {
	d, press := keyboardUI(t)
	c := d.s.Config()
	c.Provider = "elevenlabs"
	if err := d.s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	states := []string{"", "", "done", "error", "queued", "running", "done", "", "", ""}
	files := make([]DeviceFile, len(states))
	for i := range files {
		files[i] = DeviceFile{Name: fmt.Sprintf("2026071112%02d00", i), Size: 10}
	}
	if err := d.s.Catalog(c.Serial, files); err != nil {
		t.Fatal(err)
	}
	want := make(map[string]string)
	for i, file := range files {
		id := c.Serial + "-" + file.Name
		if err := d.s.Update(id, func(r *Record) {
			r.State, r.Transcript, r.SummaryState = "done", "Transcript text", states[i]
			if i == 2 {
				r.SummaryInputHash = summaryInputHash(*r)
				r.Summary = &SummaryResult{Title: "Existing", Markdown: "Existing result"}
			}
			if i == 6 {
				r.SummaryInputHash = "previous-input"
				r.Summary = &SummaryResult{Title: "Previous", Markdown: "Keep while generating"}
			}
			if i == 7 {
				r.Trashed = true
			}
			if i == 8 {
				r.State = "downloaded"
			}
			if i == 9 {
				r.Transcript = ""
			}
		}); err != nil {
			t.Fatal(err)
		}
		want[id] = states[i]
	}
	d.refresh()
	open := func() {
		press(tcell.KeyRune, 'm')
		press(tcell.KeyRune, 'I')
		if _, ok := d.modalContent.(*tview.Modal); !ok {
			t.Fatal("batch summary did not ask for confirmation")
		}
		text := renderedQueueText(t, d)
		if !strings.Contains(text, "为3条已完成转写") || !strings.Contains(text, "DOWAY") || !strings.Contains(text, "使用当前DOWAY登录会话") {
			t.Fatalf("summary confirmation did not identify count, service and text-only scope: %s", text)
		}
	}
	assertStates := func() {
		t.Helper()
		for id, state := range want {
			r, err := d.s.Get(id)
			if err != nil || r.SummaryState != state {
				t.Fatalf("unexpected summary state for %s: %+v, %v", id, r, err)
			}
		}
	}
	open()
	assertStates()
	press(tcell.KeyEnter, 0)
	assertStates()
	open()
	runningID := c.Serial + "-" + files[1].Name
	if err := d.s.Update(runningID, func(r *Record) { r.SummaryState = "running" }); err != nil {
		t.Fatal(err)
	}
	want[runningID] = "running"
	newID := c.Serial + "-" + files[9].Name
	if err := d.s.Update(newID, func(r *Record) { r.Transcript = "Newly completed after confirmation" }); err != nil {
		t.Fatal(err)
	}
	press(tcell.KeyRight, 0)
	press(tcell.KeyEnter, 0)
	want[c.Serial+"-"+files[0].Name], want[c.Serial+"-"+files[6].Name] = "queued", "queued"
	assertStates()
	if d.s.Config() != c {
		t.Fatal("batch summary changed transcription provider or automatic settings")
	}
	previous, err := d.s.Get(c.Serial + "-" + files[6].Name)
	if err != nil || previous.Summary.Markdown != "Keep while generating" {
		t.Fatal("queueing discarded an existing summary", err)
	}
}

func TestSummaryThinkingSaveCancelAndFreshConfig(t *testing.T) {
	d, press := keyboardUI(t)
	original := d.s.Config()
	open := func() *tview.Form {
		press(tcell.KeyRune, 's')
		press(tcell.KeyRune, '6')
		form := d.modalContent.(*tview.Form)
		form.SetFocus(4)
		d.app.SetFocus(form)
		return form
	}
	form := open()
	checkbox := form.GetFormItem(4).(*optionCheckbox)
	if checkbox.IsChecked() {
		t.Fatal("deep thinking was enabled by default")
	}
	press(tcell.KeyRune, ' ')
	if !checkbox.IsChecked() || d.s.Config().SummaryThinking {
		t.Fatal("thinking checkbox did not update only its draft")
	}
	press(tcell.KeyEscape, 0)
	if d.s.Config() != original {
		t.Fatal("cancel saved deep thinking")
	}
	open()
	fresh := d.s.Config()
	fresh.Provider, fresh.AutoSummary, fresh.DeviceID = "elevenlabs", true, "fresh-device"
	if err := d.s.SaveConfig(fresh); err != nil {
		t.Fatal(err)
	}
	press(tcell.KeyRune, ' ')
	press(tcell.KeyCtrlS, 0)
	fresh.SummaryThinking = true
	if d.modal || d.s.Config() != fresh {
		t.Fatalf("thinking save reset another live setting: %+v", d.s.Config())
	}
}

func TestBatchSummaryRejectsChangedThinkingMode(t *testing.T) {
	d, press := keyboardUI(t)
	id := d.selected
	if err := d.s.Update(id, func(r *Record) { r.State, r.Transcript = "done", "Completed transcript" }); err != nil {
		t.Fatal(err)
	}
	d.refresh()
	press(tcell.KeyRune, 'm')
	press(tcell.KeyRune, 'I')
	if _, ok := d.modalContent.(*tview.Modal); !ok {
		t.Fatal("batch confirmation missing")
	}
	c := d.s.Config()
	c.SummaryThinking = true
	if err := d.s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	press(tcell.KeyRight, 0)
	press(tcell.KeyEnter, 0)
	r, err := d.s.Get(id)
	if err != nil || r.SummaryState != "" {
		t.Fatalf("batch used a thinking mode not shown at confirmation: %+v, %v", r, err)
	}
}

func TestSummarySettingsFitSmallTerminal(t *testing.T) {
	d, _ := keyboardUI(t)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(80, 24)
	d.pages.SetRect(0, 0, 80, 24)
	d.layout(80, 24)
	d.summarySettings()
	d.pages.Draw(screen)
	form := d.modalContent.(*tview.Form)
	for _, p := range []tview.Primitive{form.GetFormItem(4), form.GetButton(0), form.GetButton(1)} {
		x, y, width, height := p.GetRect()
		if x < 0 || y < 0 || x+width > 80 || y+height > 24 {
			t.Fatalf("summary setting clipped in 80x24 terminal: %d,%d %dx%d", x, y, width, height)
		}
	}
}
