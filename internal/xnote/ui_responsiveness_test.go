package xnote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestLibraryNavigationUsesSnapshotWhileDiskUnavailable(t *testing.T) {
	d, press := keyboardUI(t)
	r, _ := d.record()
	if err := d.s.Update(r.ID, func(r *Record) { r.Transcript = strings.Repeat("Meeting notes. ", 10000); r.State = "done" }); err != nil {
		t.Fatal(err)
	}
	d.refresh()
	// Model an outstanding background read. UI navigation must keep using the
	// last successful snapshot, even if a file becomes unreadable meanwhile.
	d.refreshRequests = make(chan struct{}, 1)
	if err := os.WriteFile(filepath.Join(d.s.Dir(r), "metadata.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		press(tcell.KeyDown, 0)
		press(tcell.KeyUp, 0)
	}
	if d.detailVersion != "" {
		t.Fatal("list navigation eagerly rendered the hidden transcript")
	}
	press(tcell.KeyEnter, 0)
	if !d.showingDetail || !strings.Contains(d.detail.GetText(true), "Meeting notes.") {
		t.Fatal("cached recording did not open while storage unavailable")
	}
	press(tcell.KeyEscape, 0)
	d.search.SetText("Meeting")
	if len(d.hits) != 1 || d.hits[0].Record.ID != r.ID {
		t.Fatal("search did not use cached records")
	}
	if len(d.refreshRequests) != 1 {
		t.Fatal("refresh requests were not coalesced")
	}
}
