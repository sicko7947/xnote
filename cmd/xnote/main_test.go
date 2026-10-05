package main

import (
	"os"
	"testing"

	"github.com/sicko7947/xnote/internal/xnote"
)

func TestCLITranscriptionConcurrencyValidationPreservesOtherSettings(t *testing.T) {
	root := t.TempDir()
	s, err := xnote.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Config()
	c.Provider, c.Language, c.AutoTranscribe = "elevenlabs", "auto", true
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })
	t.Setenv("XNOTE_ENV_FILE", "")
	for _, value := range []string{"0", "-1", "17", "abc", "1.5"} {
		os.Args = []string{"xnote", "--data", root, "config", "transcription_concurrency", value}
		if err := run(); err == nil {
			t.Fatalf("accepted invalid concurrency %q", value)
		}
		if got := s.Config(); got != c {
			t.Fatalf("invalid setting changed configuration: %+v", got)
		}
	}
	os.Args = []string{"xnote", "--data", root, "config", "transcription_concurrency", "8"}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	c.TranscriptionConcurrency = 8
	if got := s.Config(); got != c {
		t.Fatalf("changing concurrency reset another setting: %+v", got)
	}
}

func TestCLISummaryConfigAndManualQueue(t *testing.T) {
	root := t.TempDir()
	s, err := xnote.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Config()
	c.Provider, c.AutoTranscribe = "elevenlabs", true
	if err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })
	t.Setenv("XNOTE_ENV_FILE", "")
	for _, setting := range [][2]string{{"summary_concurrency", "0"}, {"summary_concurrency", "9"}, {"automatic_summary", "maybe"}, {"summary_thinking", "1"}, {"summary_thinking", "TRUE"}, {"summary_language", "es"}} {
		os.Args = []string{"xnote", "--data", root, "config", setting[0], setting[1]}
		if err := run(); err == nil {
			t.Fatalf("invalid summary setting accepted: %v", setting)
		}
		if s.Config() != c {
			t.Fatal("invalid summary setting changed configuration")
		}
	}
	for _, setting := range [][2]string{{"automatic_summary", "true"}, {"summary_thinking", "true"}, {"summary_concurrency", "3"}, {"summary_language", "en"}} {
		os.Args = []string{"xnote", "--data", root, "config", setting[0], setting[1]}
		if err := run(); err != nil {
			t.Fatal(err)
		}
	}
	c.AutoSummary, c.SummaryThinking, c.SummaryConcurrency, c.SummaryLanguage = true, true, 3, "en"
	if s.Config() != c {
		t.Fatal("summary settings changed transcription configuration")
	}
	if err := s.Catalog(c.Serial, []xnote.DeviceFile{{Name: "20260712120000", Size: 10}}); err != nil {
		t.Fatal(err)
	}
	id := c.Serial + "-20260712120000"
	if err := s.Update(id, func(r *xnote.Record) { r.State, r.Transcript = "done", "Text to summarize" }); err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"xnote", "--data", root, "summarize", id}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(id)
	if err != nil || r.SummaryState != "queued" || r.State != "done" {
		t.Fatalf("summarize did not queue independent text processing: %+v, %v", r, err)
	}
}
