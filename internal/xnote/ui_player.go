package xnote

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Shared geometry keeps mouse seeking aligned with the rendered progress bar.
func (p *timeline) seekRect() (x, y, width int) {
	x, y, available, _ := p.GetInnerRect()
	return x + 34, y + 1, min(61, max(1, available-47))
}

func (p *timeline) Draw(screen tcell.Screen) {
	p.Box.DrawForSubclass(screen, p)
	x, y, width, height := p.GetInnerRect()
	if width < 48 || height < 2 {
		return
	}
	d := p.desktop
	position, duration, playing := PlayerPosition()
	active := duration > 0 && d.playingID != ""
	title := d.t("selected_recording")
	available := false
	selected := false
	if active {
		title = d.t("paused")
		if playing {
			title = d.t("now_playing")
		}
		title += "  " + d.playingTitle
		available = true
	} else if r, ok := d.record(); ok {
		selected = true
		title += "  " + displayTitle(r)
		duration = r.Duration
		available = r.Audio != ""
	}
	color := tcell.ColorWhite
	if p.HasFocus() {
		color = teal
	}
	tview.Print(screen, "[::b]"+tview.Escape(title)+"[-:-:-]", x+1, y, width-2, tview.AlignLeft, color)
	if !available {
		hint := d.t("no_selection")
		if selected {
			hint = d.t("audio_pending")
		}
		tview.Print(screen, hint, x+1, y+1, width-2, tview.AlignLeft, dim)
		return
	}
	label := d.t("play_short")
	if playing {
		label = d.t("pause")
	}
	tview.Print(screen, label, x+1, y+1, 11, tview.AlignLeft, teal)
	total := "—"
	if duration > 0 {
		total = clockTime(duration)
	}
	tview.Print(screen, clockTime(position)+" / "+total, x+13, y+1, 20, tview.AlignLeft, tcell.ColorWhite)
	bx, by, bw := p.seekRect()
	filled := 0
	if duration > 0 {
		filled = min(bw, max(0, int(position/duration*float64(bw))))
	}
	tview.Print(screen, strings.Repeat("━", filled), bx, by, filled, tview.AlignLeft, teal)
	tview.Print(screen, strings.Repeat("─", bw-filled), bx+filled, by, bw-filled, tview.AlignLeft, dim)
	tview.Print(screen, fmt.Sprintf("%.2f×", d.rate), bx+bw+2, by, 8, tview.AlignLeft, dim)
}

func (p *timeline) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return p.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, focus func(tview.Primitive)) (bool, tview.Primitive) {
		if !p.InRect(event.Position()) && !p.dragging {
			return false, nil
		}
		bx, by, bw := p.seekRect()
		x, _, _, _ := p.GetInnerRect()
		mx, my := event.Position()
		onBar := my == by && mx >= bx && mx < bx+bw
		seek := func() {
			ratio := max(0, min(1, float64(mx-bx)/float64(max(1, bw-1))))
			_, duration, _ := PlayerPosition()
			if duration > 0 {
				PlayerSeek(duration * ratio)
			} else if p.desktop != nil {
				if r, ok := p.desktop.record(); ok && r.Audio != "" {
					p.desktop.playAt(r.Duration * ratio)
				}
			}
		}
		switch action {
		case tview.MouseLeftDown:
			focus(p)
			if onBar {
				p.dragging = true
				seek()
				return true, p
			}
		case tview.MouseMove:
			if p.dragging {
				seek()
				return true, p
			}
		case tview.MouseLeftUp:
			if p.dragging {
				seek()
				p.dragging = false
			}
			return true, nil
		case tview.MouseLeftClick:
			if onBar {
				seek()
			} else if my == by && mx >= x && mx < x+12 && p.desktop != nil {
				p.desktop.togglePlayback()
			}
		}
		return true, nil
	})
}
