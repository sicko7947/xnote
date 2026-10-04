package xnote

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rivo/tview"
)

func connectionState(st Status) (key, color string) {
	switch st.Phase {
	case "connected", "downloading":
		return "link_connected", "#7bd8c4"
	case "searching":
		return "link_searching", "#efb879"
	case "waiting":
		return "link_waiting", "#efb879"
	default:
		return "link_stopped", "#a4aebe"
	}
}

func (d *desktop) updateHeader(c Config, st Status) {
	mode := d.t("library")
	if d.showingDetail {
		mode = d.t("transcript")
	}
	if d.view == "trash" {
		mode = d.t("trash_short")
	}
	key, color := connectionState(st)
	processing := d.t("disable")
	if c.Auto {
		processing = d.t("enable")
	}
	reconnect := d.t("disable")
	if st.Phase == "connected" || st.Phase == "downloading" || st.Phase == "searching" || st.Phase == "waiting" {
		reconnect = d.t("enable")
	}
	detail := d.t(key)
	if st.Phase == "downloading" {
		detail += fmt.Sprintf(" · %d%% · %.1f KiB/s", st.Progress, st.BytesPerSecond/1024)
	}
	d.header.SetText(fmt.Sprintf("[#7bd8c4::b]X NOTE[-:-:-]  %s    [%s]%s[-]\n[#a4aebe]%s  ·  %s %s  ·  %s %s  ·  [#7bd8c4]c[-] %s[-]", mode, color, detail, tview.Escape(c.Serial), d.t("reconnect_short"), reconnect, d.t("processing_short"), processing, d.t("connection")))
}

func (d *desktop) connectionStatus() {
	view := tview.NewTextView().SetDynamicColors(true).SetWordWrap(true)
	view.SetBorder(true).SetTitle(" X NOTE · "+d.t("connection")+" ").SetBorderPadding(1, 1, 2, 2)
	d.connectionView = view
	view.SetText(d.connectionText())
	d.popup(view, 84, 20)
}

func (d *desktop) connectionText() string {
	c, st := d.s.Config(), d.s.Status()
	key, color := connectionState(st)
	var b strings.Builder
	fmt.Fprintf(&b, "[%s::b]%s[-:-:-]\n\nX NOTE · %s\n", color, d.t(key), tview.Escape(c.Serial))
	remembered := d.t("device_not_saved")
	if c.DeviceID != "" {
		remembered = d.t("device_saved")
	}
	fmt.Fprintf(&b, "%s\n\n%s\n", remembered, d.t("reconnect_explain"))
	if c.Auto {
		fmt.Fprintf(&b, "%s\n", d.t("auto_processing_on"))
	} else {
		fmt.Fprintf(&b, "%s\n", d.t("auto_processing_off"))
	}
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, "Library/LaunchAgents/ai.xnote.sync.plist")); err == nil {
		fmt.Fprintf(&b, "%s\n", d.t("background_installed"))
	} else {
		fmt.Fprintf(&b, "%s\n", d.t("background_not_installed"))
	}
	if stamp, err := time.Parse(time.RFC3339, st.UpdatedAt); err == nil {
		fmt.Fprintf(&b, "%s  %s\n", d.t("status_updated"), stamp.Local().Format("15:04:05"))
	}
	if st.Phase == "searching" || st.Phase == "waiting" {
		fmt.Fprintf(&b, "\n%s\n", d.t("connection_help"))
	}
	if st.Detail != "" {
		fmt.Fprintf(&b, "\n%s\n", tview.Escape(st.Detail))
	}
	fmt.Fprintf(&b, "\nEsc  %s", d.t("back_short"))
	return b.String()
}

func (d *desktop) providerStatus() {
	view := tview.NewTextView().SetWordWrap(true)
	view.SetBorder(true).SetTitle(" Codex Dictate ").SetBorderPadding(1, 1, 2, 2)
	view.SetText(d.t("busy"))
	d.popup(view, 78, 13)
	go func() {
		result := Doctor(d.s)
		status := d.t("provider_unreachable")
		if result["codex_proxy_ready"] == true {
			status = d.t("provider_reachable")
		}
		d.app.QueueUpdateDraw(func() {
			if d.modalContent != view {
				return
			}
			view.SetText(status + "\n\n127.0.0.1:8377\n\n" + d.t("text_only") + "\n\n" + d.t("codex_provider_help"))
		})
	}()
}
