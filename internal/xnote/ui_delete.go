package xnote

import (
	"fmt"

	"github.com/rivo/tview"
)

func (d *desktop) deleteSelected() {
	r, ok := d.record()
	if !ok {
		return
	}
	if r.Trashed {
		d.runAction('u')
		return
	}
	if r.Audio == "" && r.OnDevice {
		d.confirmDeviceDelete(r)
		return
	}
	d.confirmTrash(r)
}

func (d *desktop) confirmTrash(r Record) {
	modal := tview.NewModal().SetText(fmt.Sprintf(d.t("trash_confirm"), displayTitle(r))).
		AddButtons([]string{d.t("cancel"), d.t("trash_short")})
	modal.SetDoneFunc(func(index int, _ string) {
		d.closeModal()
		if index != 1 {
			return
		}
		if err := d.s.Update(r.ID, func(current *Record) { current.Trashed = true }); err != nil {
			d.message(err)
			return
		}
		if d.playingID == r.ID {
			PlayerClose()
			d.playingID = ""
			d.playingTitle = ""
			d.rate = 1
		}
		d.backToLibrary()
		d.setNotice(d.t("trashed_notice"))
	})
	modal.SetFocus(0) // Cancel is the default; Enter alone must not remove data.
	d.popup(modal, 70, 12)
}

func (d *desktop) confirmDeviceDelete(r Record) {
	copyStatus := d.t("device_delete_keeps_local")
	if r.Audio == "" {
		copyStatus = d.t("device_delete_no_local")
	}
	message := fmt.Sprintf(d.t("device_delete_prompt"), displayTitle(r), copyStatus)
	d.prompt(message, "", func(value string) {
		if value != "DELETE" {
			d.setNotice(d.t("delete_cancelled"))
			return
		}
		if err := d.s.Queue("device-delete", r.ID); err != nil {
			d.message(err)
		} else {
			d.setNotice(d.t("device_delete_queued"))
		}
	})
}
