package xnote

import (
	"slices"
	"sync"

	"github.com/gdamore/tcell/v2"
)

// frameScreen stages widget painting until Show. tview clears and repaints each
// frame; tcell marks a cell dirty when its intermediate rune changes, even if
// its final content matches the previous frame. Only submitting final changes
// avoids retransmitting all text on every keypress, especially through tmux.
// Input, terminal lifecycle, cursor handling and explicit Sync stay with tcell.
type frameScreen struct {
	tcell.Screen
	mu           sync.Mutex
	frame        tcell.CellBuffer
	style        tcell.Style
	styleChanged bool
}

func newFrameScreen(screen tcell.Screen) *frameScreen { return &frameScreen{Screen: screen} }

func (s *frameScreen) Init() error {
	if err := s.Screen.Init(); err != nil {
		return err
	}
	s.Clear()
	return nil
}

func (s *frameScreen) resize() {
	width, height := s.Screen.Size()
	oldWidth, oldHeight := s.frame.Size()
	if width != oldWidth || height != oldHeight {
		s.frame.Resize(width, height)
	}
}

func (s *frameScreen) Clear() { s.Fill(' ', tcell.StyleDefault) }
func (s *frameScreen) SetStyle(style tcell.Style) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Screen.SetStyle(style)
	if style != s.style {
		s.style, s.styleChanged = style, true
	}
}
func (s *frameScreen) Fill(r rune, style tcell.Style) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resize()
	s.frame.Fill(r, style)
}
func (s *frameScreen) SetContent(x, y int, r rune, combining []rune, style tcell.Style) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frame.SetContent(x, y, r, combining, style)
}
func (s *frameScreen) SetCell(x, y int, style tcell.Style, chars ...rune) {
	if len(chars) == 0 {
		s.SetContent(x, y, ' ', nil, style)
	} else {
		s.SetContent(x, y, chars[0], chars[1:], style)
	}
}
func (s *frameScreen) GetContent(x, y int) (rune, []rune, tcell.Style, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frame.GetContent(x, y)
}
func (s *frameScreen) flush() {
	width, height := s.frame.Size()
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, combining, style, _ := s.frame.GetContent(x, y)
			oldRune, oldCombining, oldStyle, _ := s.Screen.GetContent(x, y)
			if r != oldRune || style != oldStyle || !slices.Equal(combining, oldCombining) {
				s.Screen.SetContent(x, y, r, combining, style)
			}
		}
	}
}
func (s *frameScreen) Show() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flush()
	if s.styleChanged {
		// StyleDefault cells keep the same logical value when the terminal
		// default changes, so explicitly repaint them once.
		s.Screen.Sync()
		s.styleChanged = false
	} else {
		s.Screen.Show()
	}
}
func (s *frameScreen) Sync() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flush()
	s.Screen.Sync()
	s.styleChanged = false
}
