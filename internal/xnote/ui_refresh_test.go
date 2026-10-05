package xnote

import (
	"errors"
	"testing"
	"time"
)

func TestLibraryVersionIgnoresHeartbeatsButIncludesVisibleChanges(t *testing.T) {
	rows := []Record{{ID: "one", UpdatedAt: "first"}}
	c := Config{Locale: "en"}
	status := Status{Phase: "connected", UpdatedAt: "first"}
	trans := TranscriptionStatus{Running: 1, UpdatedAt: "first"}
	summary := SummaryStatus{Running: 1, UpdatedAt: "first"}
	base := libraryVersion(rows, c, status, trans, summary)
	status.UpdatedAt = "second"
	trans.UpdatedAt = "second"
	summary.UpdatedAt = "second"
	if libraryVersion(rows, c, status, trans, summary) != base {
		t.Fatal("heartbeats require a repaint")
	}
	cases := map[string]func() string{
		"record": func() string {
			return libraryVersion([]Record{{ID: "one", UpdatedAt: "changed"}}, c, status, trans, summary)
		},
		"removal":       func() string { return libraryVersion(nil, c, status, trans, summary) },
		"progress":      func() string { v := status; v.Progress = 42; return libraryVersion(rows, c, v, trans, summary) },
		"config":        func() string { v := c; v.Locale = "zh-CN"; return libraryVersion(rows, v, status, trans, summary) },
		"transcription": func() string { v := trans; v.Running = 0; return libraryVersion(rows, c, status, v, summary) },
		"summary":       func() string { v := summary; v.Running = 0; return libraryVersion(rows, c, status, trans, v) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			if change() == base {
				t.Fatal("visible change was ignored")
			}
		})
	}
}

func TestIdleLibraryDoesNotRepaintAndRecoversFromReadFailure(t *testing.T) {
	d, _ := keyboardUI(t)
	if !d.applyLibrarySnapshot(d.rows, "initial", nil) {
		t.Fatal("initial snapshot did not repaint")
	}
	if d.applyLibrarySnapshot(d.rows, "initial", nil) {
		t.Fatal("unchanged library repainted")
	}
	d.noticeUntil = time.Now().Add(-time.Second)
	if !d.applyLibrarySnapshot(d.rows, "initial", nil) || !d.noticeUntil.IsZero() {
		t.Fatal("expired notice did not repaint once")
	}
	if d.applyLibrarySnapshot(d.rows, "initial", nil) {
		t.Fatal("expired notice keeps repainting")
	}
	failure := errors.New("temporary storage failure")
	if !d.applyLibrarySnapshot(nil, "", failure) || len(d.hits) == 0 {
		t.Fatal("failure discarded last successful library")
	}
	if d.applyLibrarySnapshot(nil, "", failure) {
		t.Fatal("repeated failure repainted")
	}
	if !d.applyLibrarySnapshot(d.rows, "initial", nil) {
		t.Fatal("recovery did not refresh")
	}
}
