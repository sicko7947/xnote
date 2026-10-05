package xnote

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/rivo/tview"
)

func (d *desktop) shareSettings() {
	endpoint := d.s.Config().ShareURL
	form := tview.NewForm()
	form.AddTextView("", d.t("share_settings_hint"), 64, 2, true, false)
	form.AddInputField("URL", endpoint, 0, nil, func(v string) { endpoint = v })
	form.AddButton(d.t("save"), func() {
		if endpoint != "" {
			if _, err := shareEndpoint(endpoint); err != nil {
				d.message(err)
				return
			}
		}
		c := d.s.Config()
		c.ShareURL = strings.TrimSpace(endpoint)
		if err := d.s.SaveConfig(c); err != nil {
			d.message(err)
			return
		}
		d.closeModal()
	}).AddButton(d.t("back"), d.closeModal)
	form.SetBorder(true).SetTitle(" " + d.t("share_settings") + " ")
	form.SetCancelFunc(d.closeModal)
	d.popup(form, 90, 12)
}

func (d *desktop) shareRecording(r Record) {
	form := tview.NewForm()
	audio := false
	if state, _ := d.s.ShareInfo(r.ID); state != nil && state.Status == "published" {
		audio = state.IncludeAudio
	}
	form.AddTextView("", d.t("share_explain"), 0, 3, true, false)
	form.AddCheckbox(d.t("share_audio"), audio, func(v bool) { audio = v })
	form.AddButton(d.t("share_publish"), func() {
		d.closeModal()
		d.setNotice(d.t("busy"))
		go func() {
			state, e := d.s.Publish(d.ctx, r.ID, audio)
			d.app.QueueUpdateDraw(func() {
				if e != nil {
					d.message(e)
					return
				}
				d.showShareURL(state)
			})
		}()
	}).AddButton(d.t("cancel"), d.closeModal)
	form.SetBorder(true).SetTitle(" " + d.t("share") + " ")
	form.SetCancelFunc(d.closeModal)
	d.popup(form, 78, 14)
}
func (d *desktop) showShareURL(state *ShareState) {
	if state == nil || state.Status != "published" {
		d.setNotice(d.t("share_missing"))
		return
	}
	form := tview.NewForm()
	form.AddInputField("URL", state.URL, 0, nil, nil)
	form.AddButton(d.t("share_copy"), func() { d.async(func() error { return copyShareURL(d.ctx, state.URL) }) })
	form.AddButton(d.t("back"), d.closeModal)
	form.SetBorder(true).SetTitle(" " + d.t("share") + " ")
	form.SetCancelFunc(d.closeModal)
	d.popup(form, 100, 9)
}
func copyShareURL(ctx context.Context, value string) error {
	var candidates [][]string
	if runtime.GOOS == "darwin" {
		candidates = [][]string{{"pbcopy"}}
	} else {
		candidates = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	}
	for _, args := range candidates {
		if _, e := exec.LookPath(args[0]); e != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Stdin = strings.NewReader(value)
		if e := cmd.Run(); e == nil {
			return nil
		}
	}
	return fmt.Errorf("clipboard unavailable; copy the URL from the field (Linux: install wl-clipboard or xclip)")
}
func (d *desktop) confirmRevokeShare(r Record) {
	modal := tview.NewModal().SetText(d.t("share_revoke_confirm")).AddButtons([]string{d.t("cancel"), d.t("share_revoke")}).SetDoneFunc(func(index int, _ string) {
		d.closeModal()
		if index == 1 {
			d.async(func() error { return d.s.RevokeShare(d.ctx, r.ID) })
		}
	})
	d.popup(modal, 74, 10)
}
