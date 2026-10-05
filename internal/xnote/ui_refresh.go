package xnote

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

func (d *desktop) requestLibraryRefresh() {
	select {
	case d.refreshRequests <- struct{}{}:
	default:
	}
}

// Ignore status heartbeats: they prove the worker is alive but do not change
// the library screen. Preserve task/progress/config changes in the signature.
func libraryVersion(rows []Record, config Config, status Status, transcription TranscriptionStatus, summary SummaryStatus) string {
	status.UpdatedAt = ""
	transcription.UpdatedAt = ""
	summary.UpdatedAt = ""
	data, _ := json.Marshal(struct {
		Config        Config
		Status        Status
		Transcription TranscriptionStatus
		Summary       SummaryStatus
	}{config, status, transcription, summary})
	var version strings.Builder
	version.Write(data)
	for _, r := range rows {
		version.WriteByte(0)
		version.WriteString(r.ID)
		version.WriteByte(0)
		version.WriteString(r.UpdatedAt)
	}
	return version.String()
}

// One reader publishes on the UI goroutine. Unchanged snapshots do not redraw;
// input remains independent of disk latency and periodic refreshes do not
// repaint an idle terminal twice per second.
func (d *desktop) loadLibrary(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.refreshRequests:
			rows, err := d.s.Records()
			version := ""
			if err == nil {
				version = libraryVersion(rows, d.s.Config(), d.s.Status(), d.s.TranscriptionStatus(), d.s.SummaryStatus())
			}
			if ctx.Err() != nil {
				return
			}
			d.app.QueueUpdate(func() {
				if d.applyLibrarySnapshot(rows, version, err) {
					d.app.ForceDraw()
				}
			})
		}
	}
}

func (d *desktop) applyLibrarySnapshot(rows []Record, version string, err error) bool {
	redraw := false
	if err != nil {
		if d.libraryReadError != err.Error() {
			d.libraryReadError = err.Error()
			d.message(err)
			redraw = true
		}
	} else {
		recovered := d.libraryReadError != ""
		d.libraryReadError = ""
		if version != d.loadedVersion || recovered {
			d.loadedVersion = version
			d.rows = rows
			d.renderLibrary()
			redraw = true
		}
	}
	// Playback and transient notices still advance even when library data is idle.
	_, _, playing := PlayerPosition()
	if !d.noticeUntil.IsZero() && !time.Now().Before(d.noticeUntil) {
		d.noticeUntil = time.Time{}
		redraw = true
	}
	if d.modal && d.connectionView != nil && d.modalContent == d.connectionView {
		d.connectionView.SetText(d.connectionText())
		redraw = true
	}
	return redraw || playing
}
