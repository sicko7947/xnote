package xnote

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func renderTestLibrary(count int) *desktop {
	d := &desktop{library: tview.NewTable().SetFixed(1, 0).SetSelectable(true, false), viewportWidth: 153}
	for col, title := range []string{"Recorded", "Recording", "Duration", "State"} {
		d.library.SetCell(0, col, tview.NewTableCell(title).SetSelectable(false))
	}
	for row := 1; row <= count; row++ {
		for col, text := range []string{"2026-10-05  18:30", fmt.Sprintf("Meeting %d 会议记录 %s", row, strings.Repeat("notes ", row%12)), "12:34", "Done"} {
			d.library.SetCell(row, col, tview.NewTableCell(text))
		}
		d.library.GetCell(row, 1).SetExpansion(1).SetMaxWidth(103)
	}
	d.library.SetRect(0, 0, 153, 50)
	d.library.Select(1, 0)
	return d
}

func TestLibraryScrollKeepsColumnsStable(t *testing.T) {
	d := renderTestLibrary(300)
	d.library.GetCell(280, 2).SetText("123:45:56")
	d.prepareLibraryColumns()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(153, 50)
	d.library.Draw(screen)
	// Every x coordinate must still point to the same column after the widest
	// duration/title rows enter or leave the viewport.
	var columns [153]int
	for x := range columns {
		_, columns[x] = d.library.CellAt(x, 0)
	}
	for _, offset := range []int{50, 260, 0} {
		d.library.SetOffset(offset, 0)
		d.library.Draw(screen)
		for x, want := range columns {
			if _, got := d.library.CellAt(x, 0); got != want {
				t.Fatalf("offset %d: column at %d moved from %d to %d", offset, x, want, got)
			}
		}
	}
}

func TestLibraryWheelDoesNotSnapBackToSelection(t *testing.T) {
	d := renderTestLibrary(300)
	d.prepareLibraryColumns()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(153, 50)
	d.library.Draw(screen)
	for range 70 {
		d.library.MouseHandler()(tview.MouseScrollDown, tcell.NewEventMouse(5, 5, tcell.WheelDown, 0), func(tview.Primitive) {})
		d.library.Draw(screen)
	}
	offset, _ := d.library.GetOffset()
	selected, _ := d.library.GetSelection()
	if offset != 70 || selected != 1 {
		t.Fatalf("wheel offset=%d selection=%d", offset, selected)
	}
	// Ordinary redraws after a window/focus change must preserve the viewport.
	for range 3 {
		d.library.Draw(screen)
	}
	if got, _ := d.library.GetOffset(); got != offset {
		t.Fatalf("redraw jumped to %d", got)
	}
}

func BenchmarkLibraryTableDraw(b *testing.B) {
	for _, count := range []int{90, 1000, 10000} {
		for _, all := range []bool{true, false} {
			b.Run(fmt.Sprintf("rows=%d/all=%t", count, all), func(b *testing.B) {
				d := renderTestLibrary(count)
				if !all {
					d.prepareLibraryColumns()
				}
				d.library.SetEvaluateAllRows(all)
				screen := tcell.NewSimulationScreen("UTF-8")
				if err := screen.Init(); err != nil {
					b.Fatal(err)
				}
				defer screen.Fini()
				screen.SetSize(153, 50)
				d.library.Draw(screen)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					d.library.SetOffset(i%(count-50), 0)
					d.library.Draw(screen)
				}
			})
		}
	}
}

func TestLibraryMetadataRefreshPreservesWheelViewport(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := newDesktop(context.Background(), s)
	for row := range 120 {
		d.rows = append(d.rows, Record{ID: fmt.Sprintf("record-%03d", row), RecordedAt: fmt.Sprintf("2026-10-05T18:%02d:00Z", row%60), Title: fmt.Sprintf("Meeting %03d", row), State: "done"})
	}
	d.renderLibrary()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(153, 50)
	d.pages.SetRect(0, 0, 153, 50)
	d.layout(153, 50)
	d.pages.Draw(screen)
	x, y, _, _ := d.library.GetInnerRect()
	for range 60 {
		d.library.MouseHandler()(tview.MouseScrollDown, tcell.NewEventMouse(x+1, y+2, tcell.WheelDown, 0), func(tview.Primitive) {})
		d.pages.Draw(screen)
	}
	offset, _ := d.library.GetOffset()
	selected, _ := d.library.GetSelection()
	if offset < selected {
		t.Fatal("test did not scroll selected row out of view")
	}
	d.rows[0].Title = "Background transcription completed"
	d.rows[0].UpdatedAt = "2026-10-05T19:00:00Z"
	d.renderLibrary()
	d.layout(153, 50)
	d.pages.Draw(screen)
	if got, _ := d.library.GetOffset(); got != offset {
		t.Fatalf("metadata refresh snapped viewport: %d -> %d", offset, got)
	}
}
