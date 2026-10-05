package xnote

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestConcurrencySettingsValidateCancelAndPreserveFreshConfig(t *testing.T) {
	d, press := keyboardUI(t)
	open := func() *tview.InputField {
		press(tcell.KeyRune, 's')
		press(tcell.KeyRune, '1')
		return d.modalContent.(*tview.Form).GetFormItem(5).(*tview.InputField)
	}
	original := d.s.Config()
	field := open()
	field.SetText("9")
	press(tcell.KeyEscape, 0)
	if d.s.Config() != original {
		t.Fatal("cancel saved concurrency")
	}
	field = open()
	for _, invalid := range []string{"", "0", "17", "1.5", "x"} {
		field.SetText(invalid)
		press(tcell.KeyCtrlS, 0)
		if !d.modal || d.s.Config() != original {
			t.Fatalf("invalid concurrency %q was saved", invalid)
		}
	}
	// Changes made by a reconnect or another config command while the form
	// is open must survive editing only the concurrency field.
	fresh := d.s.Config()
	fresh.DeviceID, fresh.AutoTranscribe, fresh.TranscriptionPaused = "remembered-device", true, true
	if err := d.s.SaveConfig(fresh); err != nil {
		t.Fatal(err)
	}
	field.SetText("8")
	press(tcell.KeyCtrlS, 0)
	fresh.TranscriptionConcurrency = 8
	if d.modal || d.s.Config() != fresh {
		t.Fatalf("concurrency edit overwrote unrelated live settings: %+v", d.s.Config())
	}
}

func batchFixture(t *testing.T, d *desktop) map[string]string {
	t.Helper()
	states := []string{"downloaded", "downloaded", "queued", "transcribing", "done", "error", "no_speech", "downloaded", "downloaded"}
	files := make([]DeviceFile, len(states))
	for i := range states {
		files[i] = DeviceFile{Name: fmt.Sprintf("2026071012%02d00", i), Size: 10}
	}
	c := d.s.Config()
	if err := d.s.Catalog(c.Serial, files); err != nil {
		t.Fatal(err)
	}
	want := make(map[string]string)
	for i, file := range files {
		id := c.Serial + "-" + file.Name
		state := states[i]
		if err := d.s.Update(id, func(r *Record) {
			r.State, r.Audio, r.Title = state, "fixture.mp3", fmt.Sprintf("Recording %d", i)
			if i == 7 {
				r.Trashed = true
			}
			if i == 8 {
				r.Audio = ""
			}
		}); err != nil {
			t.Fatal(err)
		}
		want[id] = state
	}
	d.refresh()
	return want
}

func renderedQueueText(t *testing.T, d *desktop) string {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(100, 36)
	d.pages.SetRect(0, 0, 100, 36)
	d.layout(100, 36)
	d.pages.Draw(screen)
	var b strings.Builder
	for y := 0; y < 36; y++ {
		for x := 0; x < 100; x++ {
			r, _, _, _ := screen.GetContent(x, y)
			b.WriteRune(r)
		}
		b.WriteByte('\n')
	}
	return strings.Join(strings.Fields(b.String()), "")
}

func TestBatchTranscriptionConfirmsCountAndQueuesOnlyConfirmedEligibleRecords(t *testing.T) {
	d, press := keyboardUI(t)
	want := batchFixture(t, d)
	originalConfig := d.s.Config()
	open := func() {
		press(tcell.KeyRune, 'm')
		press(tcell.KeyRune, 'a')
		if _, ok := d.modalContent.(*tview.Modal); !ok {
			t.Fatal("batch action did not ask for confirmation")
		}
		if text := renderedQueueText(t, d); !strings.Contains(text, "将2条已下载录音") || !strings.Contains(text, originalConfig.Provider) {
			t.Fatalf("batch confirmation did not show concrete count and provider: %s", text)
		}
	}
	assertStates := func() {
		t.Helper()
		for id, state := range want {
			r, err := d.s.Get(id)
			if err != nil || r.State != state {
				t.Fatalf("unexpected state %s: %+v, %v", id, r, err)
			}
		}
	}
	open()
	assertStates()           // Opening the dialog must have no queue side effects.
	press(tcell.KeyEnter, 0) // Cancel is selected by default.
	assertStates()
	open()
	// One confirmed record starts independently while the dialog is open.
	claimedID := originalConfig.Serial + "-20260710120100"
	if err := d.s.Update(claimedID, func(r *Record) { r.State = "transcribing" }); err != nil {
		t.Fatal(err)
	}
	want[claimedID] = "transcribing"
	// A newly downloaded recording must not be added to the confirmed batch.
	newID := originalConfig.Serial + "-20260709220314"
	if err := d.s.Update(newID, func(r *Record) { r.State, r.Audio = "downloaded", "new.mp3" }); err != nil {
		t.Fatal(err)
	}
	press(tcell.KeyRight, 0)
	press(tcell.KeyEnter, 0)
	want[originalConfig.Serial+"-20260710120000"] = "queued"
	assertStates()
	newRecord, err := d.s.Get(newID)
	if err != nil || newRecord.State != "downloaded" {
		t.Fatalf("batch included an unconfirmed recording: %+v, %v", newRecord, err)
	}
	if d.s.Config() != originalConfig {
		t.Fatal("batch queueing changed automatic processing or provider settings")
	}
}

func TestBatchTranscriptionRejectsChangedProvider(t *testing.T) {
	d, press := keyboardUI(t)
	want := batchFixture(t, d)
	press(tcell.KeyRune, 'm')
	press(tcell.KeyRune, 'a')
	c := d.s.Config()
	c.Provider = "elevenlabs"
	if err := d.s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	press(tcell.KeyRight, 0)
	press(tcell.KeyEnter, 0)
	for id, state := range want {
		r, err := d.s.Get(id)
		if err != nil || r.State != state {
			t.Fatal("provider changed after confirmation and batch still queued", id, r.State, err)
		}
	}
}

func TestQueuePauseDoesNotChangeAutomaticModeOrActiveRecord(t *testing.T) {
	d, press := keyboardUI(t)
	c := d.s.Config()
	c.AutoTranscribe = true
	if err := d.s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	if err := d.s.Update(d.selected, func(r *Record) { r.State, r.Audio = "transcribing", "fixture.mp3" }); err != nil {
		t.Fatal(err)
	}
	for _, paused := range []bool{true, false} {
		press(tcell.KeyRune, 'm')
		press(tcell.KeyRune, 'z')
		if current := d.s.Config(); current.TranscriptionPaused != paused || !current.AutoTranscribe {
			t.Fatalf("pause changed automatic mode: %+v", current)
		}
		r, err := d.s.Get(d.selected)
		if err != nil || r.State != "transcribing" {
			t.Fatal("pause cancelled an active recording", err)
		}
	}
}

func TestConnectionShowsAggregateAndAllActiveTranscriptions(t *testing.T) {
	d, _ := keyboardUI(t)
	d.s.SetStatus(Status{Phase: "connected", Battery: -1})
	d.s.writeTranscriptionStatus(TranscriptionStatus{
		Phase: "transcribing", Running: 2, Queued: 5, Concurrency: 4,
		Active: []TranscriptionActive{
			{ID: "one", Title: "First meeting", Provider: "elevenlabs", Phase: "transcribing"},
			{ID: "two", Title: "Second meeting", Provider: "doway", Phase: "transcribing", Progress: 42},
		},
	})
	d.refresh()
	counts := fmt.Sprintf(d.t("transcription_counts"), 2, 5, 4)
	if !strings.Contains(d.header.GetText(true), counts) {
		t.Fatal("header omitted queue totals")
	}
	text := d.connectionText()
	for _, part := range []string{counts, "First meeting", "Second meeting", "elevenlabs", "doway", "42%"} {
		if !strings.Contains(text, part) {
			t.Fatalf("connection details omit %q: %s", part, text)
		}
	}
	if strings.Contains(text, "0%") {
		t.Fatal("unknown progress was displayed as measured zero progress")
	}
}
