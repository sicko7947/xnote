package xnote

import (
	"regexp"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type recordAction struct {
	label string
	key   rune
	run   func()
}

func (d *desktop) menu() {
	list := tview.NewList().ShowSecondaryText(false)
	list.SetBorder(true).SetTitle(" " + d.t("menu") + " · ↑↓ / Enter · Esc ")
	for _, action := range d.recordActions() {
		action := action
		list.AddItem(d.t(action.label), "", action.key, func() { d.closeModal(); action.run() })
	}
	list.SetDoneFunc(d.closeModal)
	d.popup(list, 68, max(4, list.GetItemCount()+2))
}

func (d *desktop) runAction(key rune) {
	for _, action := range d.recordActions() {
		if action.key == key {
			action.run()
			return
		}
	}
	d.noticeUntil = time.Now().Add(4 * time.Second)
	d.notice.SetText(d.t("action_unavailable"))
}

func (d *desktop) focusMain() {
	if d.showingDetail {
		d.app.SetFocus(d.detail)
	} else {
		d.app.SetFocus(d.library)
	}
}

func (d *desktop) togglePlayback() {
	if (d.library.HasFocus() || d.detail.HasFocus() || d.actions.HasFocus()) && d.selected != d.playingID {
		d.play()
		return
	}
	_, duration, _ := PlayerPosition()
	if duration > 0 {
		PlayerToggle()
	} else {
		d.play()
	}
}

// Let editing widgets own typed characters. Route commands consistently from
// all other main-screen controls, instead of disabling them in the toolbar.
func (d *desktop) handleKey(e *tcell.EventKey) *tcell.EventKey {
	d.noticeUntil = time.Time{}
	if e.Key() == tcell.KeyCtrlC {
		d.app.Stop()
		return nil
	}
	if d.modal {
		if form, ok := d.modalContent.(*tview.Form); ok {
			for i := 0; i < form.GetFormItemCount(); i++ {
				if field, ok := form.GetFormItem(i).(*tview.DropDown); ok && field.IsOpen() {
					if e.Key() == tcell.KeyEscape {
						// Forms forward to DropDown, whose Escape handler exits the
						// form. Send cancellation to its focused choice list instead.
						d.app.GetFocus().InputHandler()(e, func(p tview.Primitive) { d.app.SetFocus(p) })
						return nil
					}
					if e.Key() == tcell.KeyTab || e.Key() == tcell.KeyBacktab {
						focus := func(p tview.Primitive) { d.app.SetFocus(p) }
						d.app.GetFocus().InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, 0), focus)
						field.InputHandler()(e, focus)
						return nil
					}
					// In particular, Escape cancels the open choices, not the form.
					return e
				}
			}
			if e.Key() == tcell.KeyCtrlS {
				for i := 0; i < form.GetButtonCount(); i++ {
					if form.GetButton(i).GetLabel() == d.t("save") {
						form.GetButton(i).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, 0), func(p tview.Primitive) { d.app.SetFocus(p) })
						return nil
					}
				}
			}
		}
		if e.Key() == tcell.KeyEscape {
			d.closeModal()
			return nil
		}
		return e
	}
	if e.Key() == tcell.KeyTab || e.Key() == tcell.KeyBacktab {
		d.cycleFocus(e.Key() == tcell.KeyBacktab)
		return nil
	}
	if d.search.HasFocus() {
		if e.Key() == tcell.KeyDown || e.Key() == tcell.KeyEnter {
			d.showingDetail = false
			d.focusMain()
			return nil
		}
		return e
	}
	if e.Key() == tcell.KeyEscape {
		if d.library.HasFocus() && d.search.GetText() != "" {
			d.search.SetText("")
		}
		if d.detail.HasFocus() {
			d.backToLibrary()
		} else {
			d.focusMain()
		}
		return nil
	}
	// Arrow navigation stays within the controls currently being operated.
	for _, group := range [][]tview.Primitive{
		{d.filterButtons[0], d.filterButtons[1], d.filterButtons[2]},
		d.actionButtons(),
	} {
		for i, item := range group {
			if !item.HasFocus() {
				continue
			}
			if e.Key() == tcell.KeyRune && e.Rune() == ' ' {
				return tcell.NewEventKey(tcell.KeyEnter, 0, 0)
			}
			switch e.Key() {
			case tcell.KeyLeft:
				d.app.SetFocus(group[(i+len(group)-1)%len(group)])
				return nil
			case tcell.KeyRight:
				d.app.SetFocus(group[(i+1)%len(group)])
				return nil
			case tcell.KeyUp, tcell.KeyDown:
				d.focusMain()
				return nil
			}
		}
	}
	if d.timeline.HasFocus() {
		switch e.Key() {
		case tcell.KeyEnter:
			d.togglePlayback()
			return nil
		case tcell.KeyUp, tcell.KeyDown:
			d.focusMain()
			return nil
		case tcell.KeyHome:
			PlayerSeek(0)
			return nil
		case tcell.KeyEnd:
			_, duration, _ := PlayerPosition()
			PlayerSeek(duration)
			return nil
		}
	}
	switch e.Key() {
	case tcell.KeyLeft:
		position, _, _ := PlayerPosition()
		PlayerSeek(position - 10)
		return nil
	case tcell.KeyRight:
		position, _, _ := PlayerPosition()
		PlayerSeek(position + 10)
		return nil
	case tcell.KeyDelete, tcell.KeyBackspace, tcell.KeyBackspace2:
		d.deleteSelected()
		return nil
	case tcell.KeyF2:
		d.runAction('r')
		return nil
	case tcell.KeyCtrlF:
		d.app.SetFocus(d.search)
		return nil
	}
	switch e.Rune() {
	case '1', '2', '3':
		d.setView([]string{"all", "done", "pending"}[int(e.Rune()-'1')])
	case '/':
		d.app.SetFocus(d.search)
	case 'm':
		d.menu()
	case 's':
		d.settings()
	case 'c':
		d.connectionStatus()
	case 'x':
		d.deleteSelected()
	case 'b':
		d.search.SetText("")
		d.setView("trash")
	case 'r', 'd', 't', 'f', 'D', 'u':
		d.runAction(e.Rune())
	case 'p':
		d.togglePlayback()
	case ' ':
		d.togglePlayback()
	case 'g':
		d.jump()
	case 'n':
		d.matchingSegments()
	case '[':
		d.nextSegment(false)
	case ']':
		d.nextSegment(true)
	case '?':
		d.help()
	case 'q':
		d.app.Stop()
	case '+', '=', '-':
		step := 0.25
		if e.Rune() == '-' {
			step = -step
		}
		d.rate = max(0.5, min(2, d.rate+step))
		PlayerRate(d.rate)
	default:
		return e
	}
	return nil
}

func (d *desktop) keyboardHint() string {
	if d.modal {
		if form, ok := d.modalContent.(*tview.Form); ok {
			for i := 0; i < form.GetButtonCount(); i++ {
				if form.GetButton(i).GetLabel() == d.t("save") {
					return d.t("keys_form")
				}
			}
			return d.t("keys_input")
		}
		if _, ok := d.modalContent.(*tview.Modal); ok {
			return d.t("keys_confirm")
		}
		if _, ok := d.modalContent.(*tview.TextView); ok {
			return d.t("keys_readonly")
		}
		return d.t("keys_menu")
	}
	if d.search.HasFocus() {
		return d.t("keys_search")
	}
	if d.timeline.HasFocus() {
		return d.t("keys_player")
	}
	for _, button := range d.filterButtons {
		if button.HasFocus() {
			return d.t("keys_tabs")
		}
	}
	if d.actions.HasFocus() {
		return d.t("keys_buttons")
	}
	if d.showingDetail {
		return d.t("keys_reader")
	}
	return d.t("keys_library")
}

func (d *desktop) updateFocusStyle() {
	style := tcell.StyleDefault.Background(surface).Foreground(tcell.ColorWhite)
	if d.library.HasFocus() {
		style = tcell.StyleDefault.Background(teal).Foreground(background).Bold(true)
	}
	d.library.SetSelectedStyle(style)
	d.search.SetLabelColor(dim)
	if d.search.HasFocus() {
		d.search.SetLabelColor(teal)
	}
	d.timeline.SetBorderColor(border)
	if d.timeline.HasFocus() {
		d.timeline.SetBorderColor(teal)
	}
	d.updateActionLabels()
	if time.Now().Before(d.noticeUntil) {
		d.footer.SwitchToPage("hint")
	} else if d.modal || d.search.HasFocus() || d.timeline.HasFocus() || d.tabs.HasFocus() {
		d.notice.SetText(formatKeyHint(d.keyboardHint()))
		d.footer.SwitchToPage("hint")
	} else {
		d.footer.SwitchToPage("actions")
	}
}

func (d *desktop) setNotice(text string) {
	d.noticeUntil = time.Now().Add(6 * time.Second)
	d.notice.SetText(tview.Escape(text))
}

var keyHintPattern = regexp.MustCompile(`\[[^\]]+\]`)

func formatKeyHint(text string) string {
	return keyHintPattern.ReplaceAllStringFunc(text, func(key string) string { return "[#e4eaf2::b]" + tview.Escape(key) + "[-:-:-]" })
}
