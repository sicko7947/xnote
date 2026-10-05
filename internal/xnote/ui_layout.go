package xnote

import (
	"fmt"
	"strings"
	"time"

	"github.com/rivo/tview"
)

// Presentation-only fallback: keep user and cloud titles exactly as saved.
func displayTitle(r Record) string {
	stamp, _ := time.Parse(time.RFC3339, r.RecordedAt)
	if r.TitleSource == "" && (r.Title == "" || strings.HasPrefix(r.Title, "录音 · ") || r.Title == stamp.Local().Format("2006-01-02 15:04:05")) {
		return displayDate(r.RecordedAt)
	}
	return r.Title
}

func displayDate(value string) string {
	if stamp, err := time.Parse(time.RFC3339, value); err == nil {
		return stamp.Local().Format("2006-01-02  15:04")
	}
	return value
}

func (d *desktop) layout(width, height int) {
	d.updateFocusStyle()
	if width != d.viewportWidth || height != d.viewportHeight {
		d.keepSelectionVisible()
		d.viewportWidth, d.viewportHeight = width, height
	}
	signature := fmt.Sprintf("%d/%d/%t/%d/%s", width, height, d.showingDetail, len(d.hits), d.signature)
	if signature == d.layoutSignature {
		return
	}
	d.layoutSignature = signature
	d.body.Clear()
	if d.showingDetail || len(d.hits) == 0 {
		readingWidth := min(width-2, 104)
		d.body.AddItem(nil, 0, 1, false).AddItem(d.detail, readingWidth, 0, false).AddItem(nil, 0, 1, false)
	} else {
		d.body.AddItem(d.library, 0, 1, false)
		for row := 0; row < d.library.GetRowCount(); row++ {
			d.library.GetCell(row, 1).SetMaxWidth(max(12, width-50))
		}
	}
}

func (d *desktop) settings() {
	list := tview.NewList().ShowSecondaryText(true).SetSecondaryTextColor(dim).
		SetSelectedBackgroundColor(teal).SetSelectedTextColor(background).SetHighlightFullLine(true)
	list.SetBorder(true).SetTitle(" "+d.t("settings")+" ").SetBorderPadding(1, 1, 2, 2)
	add := func(label, hint string, key rune, action func()) {
		list.AddItem(d.t(label), d.t(hint), key, func() { d.closeModal(); action() })
	}
	add("general", "general_hint", '1', d.generalSettings)
	add("provider", "provider_hint", '2', d.providerSettings)
	add("account", "account_hint", '3', d.accountMenu)
	add("advanced", "advanced_hint", '4', d.advancedMenu)
	add("connection", "connection_settings_hint", '5', d.connectionStatus)
	add("summary_settings", "summary_settings_hint", '6', d.summarySettings)
	list.SetDoneFunc(d.closeModal)
	d.popup(list, 76, 16)
}
