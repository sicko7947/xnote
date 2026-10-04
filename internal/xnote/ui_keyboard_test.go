package xnote

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Match Application.Run: capture first, then dispatch through the root's
// containers. Sending directly to the focused child hides Form routing bugs.
func keyboardUI(t *testing.T) (*desktop, func(tcell.Key, rune)) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Catalog("HD5GA00725", []DeviceFile{{"20260709220314", 10}, {"20260709220414", 20}}); err != nil {
		t.Fatal(err)
	}
	d := newDesktop(context.Background(), s)
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(screen.Fini)
	screen.SetSize(100, 36)
	draw := func() { d.pages.SetRect(0, 0, 100, 36); d.layout(100, 36); d.pages.Draw(screen) }
	draw()
	return d, func(key tcell.Key, r rune) {
		t.Helper()
		e := d.handleKey(tcell.NewEventKey(key, r, 0))
		if e != nil {
			if handler := d.pages.InputHandler(); handler != nil {
				handler(e, func(p tview.Primitive) { d.app.SetFocus(p) })
			}
		}
		draw()
	}
}

func TestHomeDownloadAndTranscribeShortcuts(t *testing.T) {
	d, press := keyboardUI(t)
	press(tcell.KeyRune, 'd')
	commands, err := filepath.Glob(filepath.Join(d.s.Root, ".work", "commands", "*.json"))
	if err != nil || len(commands) != 1 {
		t.Fatal("download shortcut did not enqueue a command", commands, err)
	}
	if err := d.s.Update(d.selected, func(r *Record) { r.Audio = filepath.Join(d.s.Root, "fixture.mp3"); r.State = "downloaded" }); err != nil {
		t.Fatal(err)
	}
	d.refresh()
	press(tcell.KeyRune, 't')
	r, err := d.s.Get(d.selected)
	if err != nil || r.State != "queued" {
		t.Fatal("transcribe shortcut did not queue selected recording", err)
	}
}

func TestKeyboardPlaybackControls(t *testing.T) {
	path := os.Getenv("XNOTE_TEST_AUDIO")
	if path == "" {
		t.Skip("set XNOTE_TEST_AUDIO for native playback verification")
	}
	d, press := keyboardUI(t)
	if err := d.s.Update(d.selected, func(r *Record) { r.Audio = path }); err != nil {
		t.Fatal(err)
	}
	d.refresh()
	press(tcell.KeyRune, 'p')
	defer PlayerClose()
	_, duration, playing := PlayerPosition()
	if duration <= 0 || !playing {
		t.Fatal("p did not start playback")
	}
	press(tcell.KeyRune, ' ')
	_, _, playing = PlayerPosition()
	if playing {
		t.Fatal("Space did not pause")
	}
	PlayerSeek(duration / 2)
	press(tcell.KeyLeft, 0)
	position, _, _ := PlayerPosition()
	want := max(0, duration/2-10)
	if position < want-0.2 || position > want+0.2 {
		t.Fatal("left seek failed", position, want)
	}
	press(tcell.KeyRune, 'g')
	input, ok := d.app.GetFocus().(*tview.InputField)
	if !ok {
		t.Fatal("jump field not focused")
	}
	input.SetText("00:01")
	press(tcell.KeyEnter, 0)
	position, _, playing = PlayerPosition()
	if d.modal || position < 0.8 || position > 1.5 || !playing {
		t.Fatal("keyboard time jump failed", position, playing)
	}
}

func TestKeyboardRenameFromToolbarAndLiteralSearch(t *testing.T) {
	d, press := keyboardUI(t)
	id := d.selected
	d.app.SetFocus(d.actions.GetButton(2))
	press(tcell.KeyF2, 0)
	input, ok := d.app.GetFocus().(*tview.InputField)
	if !ok || !d.modal {
		t.Fatal("rename shortcut failed from toolbar")
	}
	input.SetText("")
	for _, r := range "Budget 会议" {
		press(tcell.KeyRune, r)
	}
	press(tcell.KeyEnter, 0)
	r, err := d.s.Get(id)
	if err != nil || r.Title != "Budget 会议" || d.modal {
		t.Fatal("Enter did not save rename", err)
	}
	press(tcell.KeyRune, '/')
	for _, r := range "rdtms123" {
		press(tcell.KeyRune, r)
	}
	if d.search.GetText() != "rdtms123" || d.modal {
		t.Fatal("typing triggered a shortcut")
	}
	press(tcell.KeyBackspace2, 0)
	if d.search.GetText() != "rdtms12" || d.modal {
		t.Fatal("Mac Delete triggered recording deletion while editing search")
	}
	press(tcell.KeyEscape, 0)
	if d.search.GetText() != "" || !d.library.HasFocus() {
		t.Fatal("search Escape failed")
	}
}

func TestKeyboardDropdownCancelTabAndSave(t *testing.T) {
	d, press := keyboardUI(t)
	press(tcell.KeyRune, 's')
	press(tcell.KeyRune, '1')
	form, ok := d.modalContent.(*tview.Form)
	if !ok {
		t.Fatal("keyboard did not open general settings")
	}
	locale := form.GetFormItem(0).(*tview.DropDown)
	press(tcell.KeyEnter, 0)
	press(tcell.KeyDown, 0)
	press(tcell.KeyEscape, 0)
	if !d.modal || locale.IsOpen() || !locale.HasFocus() {
		t.Fatal("Escape closed settings instead of choices")
	}
	if d.s.Config().Locale != "zh-CN" {
		t.Fatal("cancel saved a locale change")
	}
	press(tcell.KeyEnter, 0)
	press(tcell.KeyTab, 0)
	if locale.IsOpen() || !form.GetFormItem(1).HasFocus() {
		t.Fatal("Tab trapped inside choices")
	}
	press(tcell.KeyBacktab, 0)
	press(tcell.KeyEnter, 0)
	press(tcell.KeyHome, 0)
	press(tcell.KeyDown, 0)
	press(tcell.KeyEnter, 0)
	press(tcell.KeyCtrlS, 0)
	if d.modal || d.s.Config().Locale != "en" {
		t.Fatal("Ctrl+S failed to save selected language")
	}
}

func TestKeyboardFocusArrowsAndPlayer(t *testing.T) {
	d, press := keyboardUI(t)
	press(tcell.KeyTab, 0)
	press(tcell.KeyTab, 0)
	if !d.filterButtons[0].HasFocus() {
		t.Fatal("category not reachable")
	}
	press(tcell.KeyRight, 0)
	if !d.filterButtons[1].HasFocus() {
		t.Fatal("category arrow was intercepted as seeking")
	}
	press(tcell.KeyDown, 0)
	if !d.library.HasFocus() {
		t.Fatal("category Down did not return to list")
	}
	press(tcell.KeyEnter, 0)
	if !d.showingDetail {
		t.Fatal("reader did not open")
	}
	for i := 0; i < 5; i++ {
		press(tcell.KeyTab, 0)
	}
	if !d.timeline.HasFocus() {
		t.Fatal("player missing from Tab order")
	}
	press(tcell.KeyRune, '-')
	if d.rate != 0.75 {
		t.Fatal("player speed key failed")
	}
	press(tcell.KeyEscape, 0)
	if !d.detail.HasFocus() {
		t.Fatal("player Escape did not return to visible reader")
	}
	d.app.SetFocus(d.actions.GetButton(2))
	press(tcell.KeyRight, 0)
	press(tcell.KeyRune, ' ')
	if _, ok := d.modalContent.(*tview.List); !ok {
		t.Fatal("toolbar arrow / Enter did not open actions")
	}
	press(tcell.KeyRune, 'r')
	if _, ok := d.app.GetFocus().(*tview.InputField); !ok {
		t.Fatal("menu shortcut failed")
	}
}

func TestAutomaticCheckboxKeyboardMouseAndCancel(t *testing.T) {
	d, press := keyboardUI(t)
	initial := d.s.Config().Auto
	press(tcell.KeyRune, 's')
	press(tcell.KeyRune, '1')
	form := d.modalContent.(*tview.Form)
	checkbox := form.GetFormItem(3).(*optionCheckbox)
	for i := 0; i < 3; i++ {
		press(tcell.KeyTab, 0)
	}
	if !checkbox.HasFocus() {
		t.Fatal("automatic checkbox is not reachable in field order")
	}
	press(tcell.KeyRune, ' ')
	if checkbox.IsChecked() == initial || d.s.Config().Auto != initial {
		t.Fatal("Space must toggle the draft only")
	}
	press(tcell.KeyEscape, 0)
	if d.s.Config().Auto != initial {
		t.Fatal("Escape saved a draft setting")
	}
	press(tcell.KeyRune, 's')
	press(tcell.KeyRune, '1')
	checkbox = d.modalContent.(*tview.Form).GetFormItem(3).(*optionCheckbox)
	x, y, _, _ := checkbox.GetRect()
	focus := func(p tview.Primitive) { d.app.SetFocus(p) }
	checkbox.MouseHandler()(tview.MouseLeftDown, tcell.NewEventMouse(x+8, y, tcell.Button1, 0), focus)
	checkbox.MouseHandler()(tview.MouseLeftClick, tcell.NewEventMouse(x+8, y, tcell.ButtonNone, 0), focus)
	if checkbox.IsChecked() == initial || !checkbox.HasFocus() {
		t.Fatal("clicking the label did not focus and toggle the checkbox")
	}
	press(tcell.KeyCtrlS, 0)
	if d.modal || d.s.Config().Auto == initial {
		t.Fatal("Ctrl+S did not persist the checkbox")
	}
}
