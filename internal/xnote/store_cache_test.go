package xnote

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRecordsCacheIsolationAndExternalChanges(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Catalog("serial", []DeviceFile{{Name: "20261005000000", Size: 10}}); err != nil {
		t.Fatal(err)
	}
	id := "serial-20261005000000"
	if err = s.Update(id, func(r *Record) {
		r.Transcript = "original"
		r.Segments = []Segment{{Text: "original"}}
		r.Summary = &SummaryResult{Title: "title", Keywords: []string{"original"}, Markdown: "body"}
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	rows[0].Segments[0].Text = "mutated"
	rows[0].Summary.Keywords[0] = "mutated"
	rows[0].Summary.Title = "mutated"
	fresh, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Segments[0].Text != "original" || fresh.Summary.Keywords[0] != "original" || fresh.Summary.Title != "title" {
		t.Fatal("caller mutated cached record")
	}
	// Match size and mtime while replacing the file, as external tools can do.
	path := filepath.Join(s.Dir(fresh), "metadata.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Transcript = "external" // same byte length
	if err = atomicJSON(path, fresh); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	fresh, err = s.Get(id)
	if err != nil || fresh.Transcript != "external" {
		t.Fatalf("replacement was stale: %v, %q", err, fresh.Transcript)
	}
	other, err := Open(s.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err = other.Catalog("serial", []DeviceFile{{Name: "20261005000000", Size: 10}, {Name: "20261005000001", Size: 20}}); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Records()
	if err != nil || len(rows) != 2 {
		t.Fatalf("new external record missing: %v %d", err, len(rows))
	}
	if err = os.RemoveAll(s.Dir(fresh)); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Records()
	if err != nil || len(rows) != 1 || rows[0].ID == id {
		t.Fatalf("deleted record remains: %v", err)
	}
	path = filepath.Join(s.Dir(rows[0]), "metadata.json")
	if err = atomicWrite(path, []byte("invalid JSON")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Records(); err == nil {
		t.Fatal("cache hid external corruption")
	}
}

func TestRecordsCacheConcurrentReadersAndWriter(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Catalog("serial", []DeviceFile{{Name: "20261005000000", Size: 10}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				rows, e := s.Records()
				if e != nil || len(rows) != 1 {
					t.Errorf("read: %v", e)
					return
				}
				rows[0].Title = "private"
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 10; j++ {
			if e := s.Update("serial-20261005000000", func(r *Record) { r.Title = "saved" }); e != nil {
				t.Error(e)
				return
			}
		}
	}()
	wg.Wait()
	r, err := s.Get("serial-20261005000000")
	if err != nil || r.Title != "saved" {
		t.Fatalf("final state: %v %q", err, r.Title)
	}
}

// Compare an unchanged-library poll against forcing every metadata file to be
// decoded, using 100 recordings with long transcripts and timestamped segments.
func BenchmarkRecordsPolling(b *testing.B) {
	s, err := Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("20261005%02d%02d00", i/60, i%60)
		r := Record{ID: "serial-" + name, Serial: "serial", DeviceName: name, State: "done", Transcript: strings.Repeat("meeting notes 会议内容 ", 1000)}
		for j := 0; j < 100; j++ {
			r.Segments = append(r.Segments, Segment{Text: "meeting notes 会议内容", Start: float64(j)})
		}
		if err := s.save(r); err != nil {
			b.Fatal(err)
		}
	}
	for _, cached := range []bool{false, true} {
		name := "decode_all"
		if cached {
			name = "unchanged"
		}
		b.Run(name, func(b *testing.B) {
			if _, err := s.Records(); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !cached {
					s.recordsCache = nil
				}
				if _, err := s.Records(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestSaveTranscriptMarkdownExactOutput(t *testing.T) {
	for _, state := range []string{"done", "no_speech"} {
		for _, segmented := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/segments=%v", state, segmented), func(t *testing.T) {
				s, err := Open(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				r := Record{ID: "serial-20261005000000", Serial: "serial", DeviceName: "20261005000000", Title: "Title\n标题", RecordedAt: "2026-10-05T00:00:00Z", Provider: "elevenlabs", State: state, Transcript: "Original text\n第二行"}
				if segmented {
					r.Segments = []Segment{{Start: 1.25, Speaker: "Speaker 1", Text: "First paragraph\n中文"}, {Start: 123.5, Speaker: "", Timing: "chunk", Text: "Approximate segment"}}
				}
				// Keep the previous serializer as the byte-for-byte compatibility oracle.
				expected := fmt.Sprintf("# %s\n\nRecorded: %s\nDevice: %s\nID: %s\nAudio: [audio.mp3](audio.mp3)\nTranscription: %s\n\n%s\n", strings.ReplaceAll(r.Title, "\n", " "), r.RecordedAt, r.Serial, r.ID, r.Provider, r.Transcript)
				if segmented {
					expected = fmt.Sprintf("# %s\n\nRecorded: %s\nID: %s\nAudio: [audio.mp3](audio.mp3)\n\n", strings.ReplaceAll(r.Title, "\n", " "), r.RecordedAt, r.ID)
					for _, seg := range r.Segments {
						label := clockTime(seg.Start)
						if seg.Timing == "chunk" {
							label += " (chunk start; approximate)"
						}
						expected += fmt.Sprintf("## %s %s\n\n%s\n\n", label, seg.Speaker, seg.Text)
					}
				}
				if state == "no_speech" {
					expected += "\n_No speech detected by the transcription provider._\n"
				}
				if err = s.save(r); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(filepath.Join(s.Dir(r), "transcript.md"))
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != expected {
					t.Fatalf("markdown format changed:\ngot %q\nwant %q", got, expected)
				}
			})
		}
	}
}
