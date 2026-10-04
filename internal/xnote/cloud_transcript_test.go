package xnote

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestDOWAYTimeline(t *testing.T) {
	raw := json.RawMessage(`{"content":"First\nSecond","sentences":[{"start":1250,"end":3000,"text":"First","speaker":0,"speakerName":"Alice"},{"start":4000,"end":6000,"text":"Second","speaker":2}]}`)
	encoded, _ := json.Marshal(string(raw))
	for _, data := range []json.RawMessage{raw, encoded} {
		result, e := ParseDOWAYTranscript(data)
		if e != nil {
			t.Fatal(e)
		}
		if len(result.Segments) != 2 || result.Segments[0].Start != 1.25 || result.Segments[0].Speaker != "Alice" || result.Segments[1].Speaker != "2" {
			t.Fatalf("wrong normalization: %+v", result)
		}
	}
	for _, raw := range []string{`{}`, `{"sentences":[{"end":100,"text":"bad"}]}`, `{"sentences":[{"start":100,"end":1,"text":"bad"}]}`} {
		if _, e := ParseDOWAYTranscript([]byte(raw)); e == nil {
			t.Fatal("accepted invalid transcript", raw)
		}
	}
}
func TestTimecodeAndKeyboardFocus(t *testing.T) {
	for input, want := range map[string]float64{"90": 90, "01:30": 90, "1:02:03": 3723, "0:01.5": 1.5} {
		got, e := parseTimecode(input)
		if e != nil || got != want {
			t.Fatal(input, got, e)
		}
	}
	for _, input := range []string{"", "-1", "1:99", "NaN", "Inf", "1:2:3:4"} {
		if _, e := parseTimecode(input); e == nil {
			t.Fatal("accepted", input)
		}
	}
	s, _ := Open(t.TempDir())
	d := newDesktop(context.Background(), s)
	capture := d.app.GetInputCapture()
	for _, want := range []interface{ HasFocus() bool }{d.search, d.filterButtons[0], d.filterButtons[1], d.filterButtons[2], d.timeline, d.actions.GetButton(0), d.actions.GetButton(1), d.actions.GetButton(2), d.actions.GetButton(3), d.actions.GetButton(4), d.actions.GetButton(5), d.library} {
		capture(tcell.NewEventKey(tcell.KeyTab, 0, 0))
		if !want.HasFocus() {
			t.Fatal("Tab skipped area")
		}
	}
	capture(tcell.NewEventKey(tcell.KeyBacktab, 0, 0))
	if !d.actions.HasFocus() {
		t.Fatal("reverse focus")
	}
}
func TestSpeakerSearchMatches(t *testing.T) {
	s, _ := Open(t.TempDir())
	_ = s.Catalog("HD5GA00725", []DeviceFile{{"20260709220314", 10}})
	_ = s.Update("HD5GA00725-20260709220314", func(r *Record) {
		r.Transcript = "讨论预算"
		r.Segments = []Segment{{Start: 12, End: 14, Text: "讨论预算", Speaker: "Alice", Timing: "provider"}}
	})
	hits, e := s.Search("Alice 预算", false)
	if e != nil || len(hits) != 1 || len(hits[0].Matches) != 1 || hits[0].Matches[0].Start != 12 {
		t.Fatal("missing timed speaker match", hits, e)
	}
}

func TestPromptStartsAtInputAndEscapeCloses(t *testing.T) {
	s, _ := Open(t.TempDir())
	d := newDesktop(context.Background(), s)
	d.prompt("Jump", "", func(string) {})
	if _, ok := d.app.GetFocus().(*tview.InputField); !ok {
		t.Fatalf("prompt focused %T", d.app.GetFocus())
	}
	d.app.GetInputCapture()(tcell.NewEventKey(tcell.KeyEscape, 0, 0))
	if d.modal || !d.library.HasFocus() {
		t.Fatal("Escape did not return to recordings")
	}
}
