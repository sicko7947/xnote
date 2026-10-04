package xnote

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestLibrarySelectionSurvivesRefreshAndEmptySearch(t *testing.T) {
	s, _ := Open(t.TempDir())
	_ = s.Catalog("HD5GA00725", []DeviceFile{{"20260709220314", 10}, {"20260709220414", 20}})
	d := newDesktop(context.Background(), s)
	d.library.Select(2, 0)
	id := d.selected
	_ = s.Update(id, func(r *Record) { r.Title = "Chosen recording"; r.Transcript = "Chosen transcript" })
	d.refresh()
	row, _ := d.library.GetSelection()
	if d.selected != id || row != 2 || !strings.Contains(d.detail.GetText(true), "Chosen transcript") {
		t.Fatal("background update changed selection or left a stale preview")
	}
	d.search.SetText("no-such-recording")
	if d.selected != "" || d.library.GetRowCount() != 1 {
		t.Fatal("empty search retained a selection")
	}
	d.search.SetText("")
	d.library.Select(2, 0)
	if !strings.Contains(d.detail.GetText(true), "Chosen transcript") {
		t.Fatal("clearing search did not restore preview")
	}
}

func TestLibraryMouseSelectsWithoutPlayback(t *testing.T) {
	s, _ := Open(t.TempDir())
	_ = s.Catalog("HD5GA00725", []DeviceFile{{"20260709220314", 10}, {"20260709220414", 20}})
	d := newDesktop(context.Background(), s)
	d.library.SetRect(0, 0, 50, 12)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(50, 12)
	d.library.Draw(screen)
	x, y, _, _ := d.library.GetInnerRect()
	used, _ := d.library.MouseHandler()(tview.MouseLeftClick, tcell.NewEventMouse(x+2, y+2, tcell.Button1, 0), func(p tview.Primitive) { d.app.SetFocus(p) })
	if !used || d.selected != d.hits[1].Record.ID || d.playingID != "" {
		t.Fatal("click should select the second recording without starting audio")
	}
}

func TestSinglePageRecordingWorkflow(t *testing.T) {
	s, _ := Open(t.TempDir())
	_ = s.Catalog("HD5GA00725", []DeviceFile{{"20260709220314", 10}, {"20260709220414", 20}, {"20260709220514", 30}})
	_ = s.Update("HD5GA00725-20260709220314", func(r *Record) { r.State = "done"; r.Transcript = "Meeting notes" })
	_ = s.Update("HD5GA00725-20260709220414", func(r *Record) { r.State = "no_speech" })
	d := newDesktop(context.Background(), s)
	capture := d.app.GetInputCapture()
	capture(tcell.NewEventKey(tcell.KeyRune, '2', 0))
	if len(d.hits) != 1 || d.hits[0].Record.State != "done" {
		t.Fatal("transcribed filter included other states")
	}
	id := d.selected
	d.library.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(p tview.Primitive) { d.app.SetFocus(p) })
	if !d.showingDetail || !d.detail.HasFocus() || d.playingID != "" {
		t.Fatal("Enter must open reading view without audio")
	}
	d.help()
	capture(tcell.NewEventKey(tcell.KeyEscape, 0, 0))
	if !d.detail.HasFocus() || !d.showingDetail {
		t.Fatal("closing help lost reading context")
	}
	capture(tcell.NewEventKey(tcell.KeyEscape, 0, 0))
	if d.showingDetail || !d.library.HasFocus() || d.selected != id {
		t.Fatal("Escape did not return to selected recording")
	}
	capture(tcell.NewEventKey(tcell.KeyRune, '3', 0))
	if len(d.hits) != 1 || d.hits[0].Record.State != "on_device" {
		t.Fatal("pending includes completed or silent recordings")
	}
	capture(tcell.NewEventKey(tcell.KeyRune, '1', 0))
	if len(d.hits) != 3 {
		t.Fatal("All filter lost recordings")
	}
}

func TestResponsiveLibraryFocus(t *testing.T) {
	s, _ := Open(t.TempDir())
	_ = s.Catalog("HD5GA00725", []DeviceFile{{"20260709220314", 10}})
	d := newDesktop(context.Background(), s)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	for _, size := range [][2]int{{80, 24}, {100, 36}, {153, 68}} {
		for _, locale := range []string{"zh-CN", "en", "ja"} {
			c := s.Config()
			c.Locale = locale
			_ = s.SaveConfig(c)
			d.refresh()
			for _, focused := range []tview.Primitive{d.library, d.detail} {
				d.showingDetail = focused == d.detail
				d.app.SetFocus(focused)
				screen.SetSize(size[0], size[1])
				d.pages.SetRect(0, 0, size[0], size[1])
				d.layout(size[0], size[1])
				d.pages.Draw(screen)
				_, fy, _, fh := d.footer.GetRect()
				page, _ := d.footer.GetFrontPage()
				if fh != 1 || page != "actions" {
					t.Fatal("main screen must have one unified action row", fh, page)
				}
				for i := 0; i < d.actions.GetButtonCount(); i++ {
					bx, by, bw, bh := d.actions.GetButton(i).GetRect()
					if bx < 0 || by != fy || bh != 1 || bx+bw > size[0] || by+bh > size[1] {
						t.Fatalf("footer action clipped: %v %s button %d", size, locale, i)
					}
				}
				x, y, w, h := focused.GetRect()
				if x < 0 || y < 0 || x+w > size[0] || y+h > size[1] || w < 35 || h < 7 {
					t.Fatalf("unusable focused pane at %v %s: %d,%d %dx%d", size, locale, x, y, w, h)
				}
			}
		}
	}
}

func TestShrinkingTerminalKeepsSelectedRecordingVisible(t *testing.T) {
	s, _ := Open(t.TempDir())
	var files []DeviceFile
	for i := 0; i < 30; i++ {
		files = append(files, DeviceFile{fmt.Sprintf("2026070912%02d00", i), 10})
	}
	_ = s.Catalog("HD5GA00725", files)
	d := newDesktop(context.Background(), s)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	id := d.selected
	for _, size := range [][2]int{{153, 68}, {80, 24}} {
		screen.SetSize(size[0], size[1])
		d.pages.SetRect(0, 0, size[0], size[1])
		d.layout(size[0], size[1])
		d.pages.Draw(screen)
		row, _ := d.library.GetSelection()
		offset, _ := d.library.GetOffset()
		_, _, _, height := d.library.GetInnerRect()
		if d.selected != id || row <= offset || row >= offset+height {
			t.Fatal("selected recording scrolled out of view", row, offset, height)
		}
	}
}
