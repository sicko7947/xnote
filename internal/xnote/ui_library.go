package xnote

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
)

func (d *desktop) selectRecording(row, _ int) {
	if row > 0 && row <= len(d.hits) {
		d.selected = d.hits[row-1].Record.ID
		if d.showingDetail {
			d.showDetail()
		}
	}
}

func (d *desktop) primaryAction() {
	if d.showingDetail {
		d.backToLibrary()
	} else {
		d.openRecording()
	}
}

func (d *desktop) openRecording() {
	if _, ok := d.record(); !ok {
		return
	}
	d.showingDetail = true
	d.refresh()
	d.app.SetFocus(d.detail)
}

func (d *desktop) backToLibrary() {
	d.showingDetail = false
	d.refresh()
	d.keepSelectionVisible()
	d.app.SetFocus(d.library)
}

func (d *desktop) keepSelectionVisible() {
	row, col := d.library.GetSelection()
	offset, horizontal := d.library.GetOffset()
	// tview enables tail-following when all rows fit. Disable it before a
	// resize or returning from the reader so the chosen row remains visible.
	d.library.SetOffset(offset, horizontal).Select(row, col)
}

func (d *desktop) setView(view string) {
	d.view = view
	d.backToLibrary()
}

func (d *desktop) updateTabs(hits []Hit) {
	if d.view == "trash" {
		hits = d.s.searchRecords(d.rows, d.search.GetText(), false)
	}
	counts := [3]int{len(hits), 0, 0}
	for _, hit := range hits {
		if hit.Record.State == "done" {
			counts[1]++
		} else if hit.Record.State != "no_speech" {
			counts[2]++
		}
	}
	for i, key := range []string{"all", "done", "pending"} {
		label := key
		if key == "pending" {
			label = "to_process"
		}
		button := d.filterButtons[i]
		button.SetLabel(fmt.Sprintf("%d %s · %d", i+1, d.t(label), counts[i]))
		button.SetStyle(tcell.StyleDefault.Background(surface).Foreground(dim))
		if d.view == key {
			button.SetStyle(tcell.StyleDefault.Background(teal).Foreground(background).Bold(true))
		}
	}
}

func (d *desktop) recordingName(r Record) string {
	if displayTitle(r) != displayDate(r.RecordedAt) {
		return r.Title
	}
	// An excerpt helps identify untitled recordings without inventing or saving
	// a cloud title. Quotes distinguish it from a user-supplied name.
	if excerpt := strings.Join(strings.Fields(r.Transcript), " "); excerpt != "" {
		text := []rune(excerpt)
		if len(text) > 100 {
			excerpt = string(text[:100]) + "…"
		}
		return "“" + excerpt + "”"
	}
	return d.t("untitled")
}

func (d *desktop) recordingState(r Record) string {
	switch r.State {
	case "on_device":
		return d.t("download_short")
	case "queued":
		return d.t("transcribe_short")
	case "error", "download_error":
		return d.t("failed_short")
	case "no_speech":
		return d.t("silent_short")
	default:
		return d.t(r.State)
	}
}
