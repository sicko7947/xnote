package xnote

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func parseTimecode(value string) (float64, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) > 3 || len(parts) == 0 {
		return 0, errors.New("use seconds, MM:SS, or HH:MM:SS")
	}
	total := float64(0)
	for i, p := range parts {
		n, e := strconv.ParseFloat(p, 64)
		if e != nil || n < 0 || strings.ContainsAny(p, "eENnaAiIfF") || (i > 0 && n >= 60) {
			return 0, errors.New("use seconds, MM:SS, or HH:MM:SS")
		}
		total = total*60 + n
	}
	return total, nil
}
func (d *desktop) cycleFocus(back bool) {
	main := tview.Primitive(d.library)
	if d.showingDetail {
		main = d.detail
	}
	items := []tview.Primitive{main, d.search, d.filterButtons[0], d.filterButtons[1], d.filterButtons[2], d.timeline}
	items = append(items, d.actionButtons()...)
	index := 0
	for i, p := range items {
		if p.HasFocus() {
			index = i
			break
		}
	}
	step := 1
	if back {
		step = -1
	}
	d.app.SetFocus(items[(index+step+len(items))%len(items)])
}
func (d *desktop) jump() {
	d.prompt(d.t("jump"), "", func(value string) {
		seconds, e := parseTimecode(value)
		if e != nil {
			d.message(e)
			return
		}
		d.playAt(seconds)
	})
}
func (d *desktop) nextSegment(forward bool) {
	r, ok := d.record()
	if !ok {
		return
	}
	position, _, _ := PlayerPosition()
	if d.playingID != r.ID {
		position = -1
	}
	target := -1.0
	for _, seg := range r.Segments {
		if forward && seg.Start > position+0.3 {
			target = seg.Start
			break
		}
		if !forward && seg.Start < position-0.3 {
			target = seg.Start
		}
	}
	if target >= 0 {
		d.playAt(target)
	}
}
func (d *desktop) matchingSegments() {
	r, ok := d.record()
	if !ok {
		return
	}
	terms := strings.Fields(strings.ToLower(d.search.GetText()))
	list := tview.NewList().ShowSecondaryText(true)
	list.SetBorder(true).SetTitle(" " + d.t("matches") + " ")
	for _, seg := range r.Segments {
		if len(terms) > 0 && !matchesTerms(seg.Text+" "+seg.Speaker, terms) {
			continue
		}
		seg := seg
		prefix := clockTime(seg.Start)
		if seg.Timing == "chunk" {
			prefix = "≈ " + prefix
		}
		list.AddItem(prefix+"  "+seg.Speaker, strings.ReplaceAll(seg.Text, "\n", " "), 0, func() { d.closeModal(); d.playAt(seg.Start) })
	}
	if list.GetItemCount() == 0 {
		d.setNotice(d.t("no_timed_matches"))
		return
	}
	list.SetDoneFunc(d.closeModal)
	d.popup(list, 96, min(30, list.GetItemCount()*2+2))
}
func (d *desktop) help() {
	view := tview.NewTextView().SetText(d.t("help_text"))
	view.SetBorder(true).SetTitle(" " + d.t("help") + " ")
	view.SetDoneFunc(func(tcell.Key) { d.closeModal() })
	d.popup(view, 86, 18)
}
func (d *desktop) cloudBrowser() {
	local, haveLocal := d.record()
	d.async(func() error {
		rows, e := d.s.Cloud(d.ctx)
		if e != nil {
			return e
		}
		d.app.QueueUpdateDraw(func() {
			list := tview.NewList().ShowSecondaryText(true)
			list.SetBorder(true).SetTitle(" " + d.t("cloud") + " ")
			for _, row := range rows {
				uid := fmt.Sprint(row["audioFileUID"])
				title, _ := row["fileName"].(string)
				if title == "" {
					title = uid
				}
				list.AddItem(title, uid, 0, func() {
					if !haveLocal {
						d.setNotice(d.t("select_local"))
						return
					}
					d.closeModal()
					modal := tview.NewModal().SetText(fmt.Sprintf(d.t("cloud_confirm"), title, local.Title)).AddButtons([]string{d.t("import"), d.t("back")})
					modal.SetDoneFunc(func(i int, _ string) {
						d.closeModal()
						if i == 0 {
							d.async(func() error { return d.s.ImportCloud(d.ctx, local.ID, uid) })
						}
					})
					d.popup(modal, 86, 14)
				})
			}
			if len(rows) == 0 {
				list.AddItem(d.t("empty_cloud"), "", 0, nil)
			}
			list.SetDoneFunc(d.closeModal)
			d.popup(list, 96, 30)
		})
		return nil
	})
}

func (d *desktop) accountMenu() {
	list := tview.NewList().ShowSecondaryText(true)
	list.SetBorder(true).SetTitle(" DOWAY · " + d.t("account") + " ")
	status := d.t("account_optional")
	if _, e := os.Stat(d.s.path(".work/doway-session.json")); e == nil {
		status = d.t("account_present")
	}
	list.AddItem(d.t("login"), status, 0, func() { d.closeModal(); d.login() })
	list.AddItem(d.t("cloud"), d.t("cloud_import_hint"), 0, func() { d.closeModal(); d.cloudBrowser() })
	list.AddItem(d.t("cloud_sync"), d.t("cloud_sync_hint"), 0, func() { d.closeModal(); d.async(func() error { _, e := d.s.SyncCloud(d.ctx); return e }) })
	list.SetDoneFunc(d.closeModal)
	d.popup(list, 92, 11)
}
func (d *desktop) advancedMenu() {
	list := tview.NewList().ShowSecondaryText(false)
	list.SetBorder(true).SetTitle(d.t("advanced"))
	list.AddItem(d.t("diagnostics"), "", 0, func() {
		d.closeModal()
		b, _ := json.MarshalIndent(Doctor(d.s), "", "  ")
		view := tview.NewTextView().SetText(string(b))
		view.SetBorder(true).SetTitle(d.t("diagnostics"))
		view.SetDoneFunc(func(tcell.Key) { d.closeModal() })
		d.popup(view, 88, 22)
	})
	list.AddItem(d.t("trash_short"), "", 0, func() { d.closeModal(); d.setView("trash") })
	list.AddItem(d.t("help"), "", 0, func() { d.closeModal(); d.help() })
	list.SetDoneFunc(d.closeModal)
	d.popup(list, 64, 8)
}
