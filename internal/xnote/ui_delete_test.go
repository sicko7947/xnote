package xnote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestLocalDeleteConfirmRestoreAndKeepDevice(t *testing.T) {
	d, press := keyboardUI(t)
	d.library.Select(2, 0)
	id := d.selected
	audio := filepath.Join(d.s.Root, "audio.mp3")
	_ = os.WriteFile(audio, []byte("local fixture"), 0600)
	_ = d.s.Update(id, func(r *Record) { r.Audio = audio; r.Transcript = "Keep this text"; r.State = "done"; r.OnDevice = true })
	d.refresh()
	press(tcell.KeyDelete, 0)
	if _, ok := d.modalContent.(*tview.Modal); !ok {
		t.Fatal("Delete should ask about local trash")
	}
	press(tcell.KeyEnter, 0)
	r, _ := d.s.Get(id)
	if r.Trashed {
		t.Fatal("default Enter deleted a recording")
	}
	press(tcell.KeyBackspace2, 0) // The Delete key on a Mac keyboard.
	press(tcell.KeyRight, 0)
	press(tcell.KeyEnter, 0)
	r, _ = d.s.Get(id)
	if !r.Trashed || !r.OnDevice || r.Transcript != "Keep this text" {
		t.Fatal("local trash changed device or transcript")
	}
	if _, err := os.Stat(audio); err != nil {
		t.Fatal("local trash erased audio", err)
	}
	commands, _ := filepath.Glob(filepath.Join(d.s.Root, ".work/commands/*.json"))
	if len(commands) != 0 {
		t.Fatal("local trash queued a device command")
	}
	press(tcell.KeyRune, 'b')
	if d.selected != id {
		t.Fatal("trashed recording missing")
	}
	press(tcell.KeyRune, 'u')
	r, _ = d.s.Get(id)
	if r.Trashed || r.Transcript != "Keep this text" {
		t.Fatal("restore failed")
	}
}

func TestDeviceDeleteNeedsExactConfirmationAndNeverChangesLocal(t *testing.T) {
	d, press := keyboardUI(t)
	id := d.selected
	press(tcell.KeyRune, 'D')
	form, ok := d.modalContent.(*tview.Form)
	if !ok {
		t.Fatal("Shift+D should open device confirmation")
	}
	text := form.GetFormItem(0).(*tview.TextView).GetText(true)
	if !strings.Contains(text, d.t("device_delete_no_local")) {
		t.Fatal("missing no-local-copy explanation")
	}
	press(tcell.KeyEscape, 0)
	press(tcell.KeyRune, 'x') // A device-only recording takes the same explicit path.
	input := d.app.GetFocus().(*tview.InputField)
	input.SetText("delete")
	press(tcell.KeyEnter, 0)
	commands, _ := filepath.Glob(filepath.Join(d.s.Root, ".work/commands/*.json"))
	if len(commands) != 0 {
		t.Fatal("wrong confirmation queued a deletion")
	}
	press(tcell.KeyRune, 'D')
	d.app.GetFocus().(*tview.InputField).SetText("DELETE")
	press(tcell.KeyEnter, 0)
	commands, _ = filepath.Glob(filepath.Join(d.s.Root, ".work/commands/*.json"))
	if len(commands) != 1 {
		t.Fatal("device command not queued exactly once")
	}
	var command Command
	if err := readJSON(commands[0], &command); err != nil || command.Action != "device-delete" || command.ID != id {
		t.Fatal("wrong delete target", command, err)
	}
	r, _ := d.s.Get(id)
	if r.Trashed || !r.OnDevice {
		t.Fatal("queueing was presented as completed deletion")
	}
}

func TestConnectionStateIsIndependentOfAutomaticProcessing(t *testing.T) {
	d, press := keyboardUI(t)
	c := d.s.Config()
	c.Auto = true
	c.DeviceID = "remembered"
	_ = d.s.SaveConfig(c)
	d.s.SetStatus(Status{Phase: "searching"})
	d.refresh()
	if !strings.Contains(d.header.GetText(true), d.t("link_searching")) {
		t.Fatal("automatic setting masqueraded as connection")
	}
	press(tcell.KeyRune, 'c')
	if d.modalContent != d.connectionView || !strings.Contains(d.connectionView.GetText(true), d.t("device_saved")) {
		t.Fatal("connection details missing")
	}
	d.s.SetStatus(Status{Phase: "connected"})
	d.refresh()
	if !strings.Contains(d.connectionView.GetText(true), d.t("link_connected")) {
		t.Fatal("open connection panel did not refresh")
	}
	_ = atomicJSON(d.s.path(".work/status.json"), Status{Phase: "connected", UpdatedAt: time.Now().Add(-time.Minute).Format(time.RFC3339)})
	d.refresh()
	if !strings.Contains(d.header.GetText(true), d.t("link_stopped")) {
		t.Fatal("stale connection presented as connected")
	}
	_ = atomicJSON(d.s.path(".work/status.json"), Status{Phase: "connected", UpdatedAt: "invalid"})
	if d.s.Status().Phase != "stopped" {
		t.Fatal("invalid heartbeat presented as connected")
	}
}
