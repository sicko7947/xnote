package xnote

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSchedulersPublishChangesWithoutRewritingIdleStatus(t *testing.T) {
	for _, kind := range []string{"transcription", "summary"} {
		t.Run(kind, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				if kind == "transcription" {
					transcriptionLoopWithDeps(ctx, s, transcriptionLoopDeps{PollInterval: time.Millisecond})
				} else {
					summaryLoopWithDeps(ctx, s, summaryLoopDeps{PollInterval: time.Millisecond})
				}
			}()
			defer func() { cancel(); <-done }()
			path := filepath.Join(s.Root, ".work", kind+"-status.json")
			waitStatus := func(concurrency int) os.FileInfo {
				t.Helper()
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					var st struct {
						Concurrency int `json:"concurrency"`
					}
					data, e := os.ReadFile(path)
					if e == nil && json.Unmarshal(data, &st) == nil && st.Concurrency == concurrency {
						info, e := os.Stat(path)
						if e == nil {
							return info
						}
					}
					time.Sleep(time.Millisecond)
				}
				t.Fatal("status did not reflect settings")
				return nil
			}
			limit := 4
			if kind == "summary" {
				limit = 2
			}
			initial := waitStatus(limit)
			// Many accelerated scheduler ticks should not rewrite identical state.
			time.Sleep(40 * time.Millisecond)
			idle, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(initial, idle) || !initial.ModTime().Equal(idle.ModTime()) {
				t.Fatal("idle scheduler rewrote unchanged status")
			}
			c := s.Config()
			c.TranscriptionConcurrency = 3
			c.SummaryConcurrency = 3
			if err = s.SaveConfig(c); err != nil {
				t.Fatal(err)
			}
			changed := waitStatus(3)
			if os.SameFile(idle, changed) {
				t.Fatal("changed status was not published")
			}
		})
	}
}
