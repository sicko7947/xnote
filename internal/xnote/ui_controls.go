package xnote

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// A single, non-wrapping action row. Compact padding leaves room for help even
// in an 80-column terminal; buttons remain ordinary mouse/keyboard controls.
type actionBar struct {
	*tview.Flex
	buttons []*tview.Button
}

func newActionBar() *actionBar {
	b := &actionBar{Flex: tview.NewFlex()}
	b.SetBackgroundColor(surface)
	return b
}

func (b *actionBar) Clear() {
	b.Flex.Clear()
	b.buttons = nil
}

func (b *actionBar) AddButton(label string, action func()) *actionBar {
	button := tview.NewButton(label).SetSelectedFunc(action).
		SetStyle(tcell.StyleDefault.Background(surface).Foreground(tcell.ColorWhite)).
		SetActivatedStyle(tcell.StyleDefault.Background(teal).Foreground(background).Bold(true))
	b.buttons = append(b.buttons, button)
	b.AddItem(button, 0, 1, false)
	return b
}

func (b *actionBar) GetButton(i int) *tview.Button { return b.buttons[i] }
func (b *actionBar) GetButtonCount() int           { return len(b.buttons) }

func (b *actionBar) Draw(screen tcell.Screen) {
	for _, button := range b.buttons {
		b.ResizeItem(button, tview.TaggedStringWidth(button.GetLabel())+2, 0)
	}
	b.Flex.Draw(screen)
}

// tview's default checkbox places a one-cell X after the form's longest label.
// Render the box and its label together, aligned with the start of each row.
type optionCheckbox struct{ *tview.Checkbox }

func newOptionCheckbox(label string, checked bool, changed func(bool)) *optionCheckbox {
	return &optionCheckbox{tview.NewCheckbox().SetChecked(checked).SetChangedFunc(changed).
		SetCheckedString(tview.Escape("[✓]  " + label)).
		SetUncheckedString(tview.Escape("[ ]  " + label))}
}

func (c *optionCheckbox) SetFormAttributes(_ int, label, bg, text, fieldBG tcell.Color) tview.FormItem {
	c.Checkbox.SetFormAttributes(0, label, bg, text, fieldBG)
	c.SetActivatedStyle(tcell.StyleDefault.Foreground(background).Background(teal).Bold(true))
	return c
}
