package xnote

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type countingScreen struct {
	tcell.SimulationScreen
	writes, dirty, syncs int
}

func (s *countingScreen) SetContent(x, y int, r rune, c []rune, style tcell.Style) {
	s.writes++
	s.SimulationScreen.SetContent(x, y, r, c, style)
}
func (s *countingScreen) Show() {
	cells := s.SimulationScreen.(interface{ GetCells() *tcell.CellBuffer }).GetCells()
	width, height := cells.Size()
	s.dirty = 0
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if cells.Dirty(x, y) {
				s.dirty++
			}
			// tcell's physical draw skips wide-character continuation cells.
			_, _, _, cellWidth := cells.GetContent(x, y)
			x += max(1, cellWidth) - 1
		}
	}
	s.SimulationScreen.Show()
}
func (s *countingScreen) Sync() { s.syncs++; s.SimulationScreen.Sync() }

func TestFrameScreenOnlySubmitsFinalChanges(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(fmt.Sprint(buffered), func(t *testing.T) {
			raw := &countingScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
			var screen tcell.Screen = raw
			if buffered {
				screen = newFrameScreen(raw)
			}
			if err := screen.Init(); err != nil {
				t.Fatal(err)
			}
			defer screen.Fini()
			raw.SetSize(153, 50)
			d := renderTestLibrary(90)
			d.prepareLibraryColumns()
			draw := func() { screen.Clear(); d.library.Draw(screen); screen.Show() }
			draw()
			raw.writes = 0
			draw()
			if buffered && (raw.writes != 0 || raw.dirty != 0) {
				t.Fatalf("unchanged frame writes=%d dirty=%d", raw.writes, raw.dirty)
			}
			if !buffered && raw.dirty == 0 {
				t.Fatal("baseline failed to reproduce redundant tcell repaint")
			}
			t.Logf("unchanged frame: writes=%d dirty=%d", raw.writes, raw.dirty)
			raw.writes = 0
			d.library.Select(2, 0)
			draw()
			t.Logf("selection frame: writes=%d dirty=%d", raw.writes, raw.dirty)
			if buffered && raw.dirty > 306 {
				t.Fatalf("selection repainted more than two rows: %d", raw.dirty)
			}
		})
	}
}

func TestFrameScreenUnicodeRemovalResizeAndSync(t *testing.T) {
	raw := &countingScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
	screen := newFrameScreen(raw)
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	style := tcell.StyleDefault.Foreground(tcell.ColorYellow).Background(tcell.ColorBlue)
	screen.SetStyle(style)
	for _, size := range [][2]int{{15, 4}, {30, 8}, {8, 3}} {
		raw.SetSize(size[0], size[1])
		screen.Clear()
		tview.Print(screen, "会议 e\u0301", 0, 0, size[0], tview.AlignLeft, tcell.ColorWhite)
		screen.SetCell(0, 1, style, 'X')
		screen.Show()
		cells, w, _ := raw.GetContents()
		if cells[0].Runes[0] != '会' || cells[2].Runes[0] != '议' || cells[w].Runes[0] != 'X' {
			t.Fatalf("Unicode/render mismatch at %v", size)
		}
		if !slices.Equal(cells[5].Runes, []rune{'e', '\u0301'}) {
			t.Fatalf("combining mark lost: %q", cells[5].Runes)
		}
		if cells[w].Style != style {
			t.Fatal("explicit style changed")
		}
		screen.Clear()
		tview.Print(screen, " 会议 e\u0300", 0, 0, size[0], tview.AlignLeft, tcell.ColorWhite)
		screen.Show()
		cells, _, _ = raw.GetContents()
		if cells[0].Runes[0] != ' ' || cells[1].Runes[0] != '会' || cells[3].Runes[0] != '议' || !slices.Equal(cells[6].Runes, []rune{'e', '\u0300'}) {
			t.Fatalf("overlapping wide glyph shift or combining change failed at %v", size)
		}
		screen.Clear()
		screen.Show()
		cells, _, _ = raw.GetContents()
		for _, cell := range cells {
			if len(cell.Runes) > 0 && cell.Runes[0] != ' ' {
				t.Fatalf("stale glyph at %v: %q", size, cell.Runes)
			}
		}
	}
	syncs := raw.syncs
	screen.Sync()
	if raw.syncs != syncs+1 {
		t.Fatal("explicit full refresh not forwarded")
	}
	newStyle := tcell.StyleDefault.Background(tcell.ColorRed)
	screen.SetStyle(newStyle)
	screen.Show()
	cells, _, _ := raw.GetContents()
	if cells[0].Style != newStyle {
		t.Fatal("default style change was not repainted")
	}
}

func BenchmarkLibraryFrame(b *testing.B) {
	for _, buffered := range []bool{false, true} {
		b.Run(fmt.Sprintf("buffered=%t", buffered), func(b *testing.B) {
			raw := tcell.NewSimulationScreen("UTF-8")
			var screen tcell.Screen = raw
			if buffered {
				screen = newFrameScreen(raw)
			}
			if err := screen.Init(); err != nil {
				b.Fatal(err)
			}
			defer screen.Fini()
			raw.SetSize(153, 50)
			d := renderTestLibrary(90)
			d.prepareLibraryColumns()
			screen.Clear()
			d.library.Draw(screen)
			screen.Show()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				screen.Clear()
				d.library.Select(1+i%2, 0)
				d.library.Draw(screen)
				screen.Show()
			}
		})
	}
}

type inputModeScreen struct {
	tcell.SimulationScreen
	initialized, mouse, paste bool
}

func (s *inputModeScreen) Init() error {
	err := s.SimulationScreen.Init()
	s.initialized = err == nil
	return err
}
func (s *inputModeScreen) EnableMouse(flags ...tcell.MouseFlags) {
	s.mouse = s.initialized
	s.SimulationScreen.EnableMouse(flags...)
}
func (s *inputModeScreen) EnablePaste() { s.paste = s.initialized; s.SimulationScreen.EnablePaste() }
func TestCustomScreenEnablesInputModesAfterInitialization(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := newDesktop(context.Background(), store)
	raw := &inputModeScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8")}
	d.setScreen(newFrameScreen(raw))
	defer raw.Fini()
	if !raw.initialized || !raw.mouse || !raw.paste {
		t.Fatalf("custom screen input disabled: init=%t mouse=%t paste=%t", raw.initialized, raw.mouse, raw.paste)
	}
}
