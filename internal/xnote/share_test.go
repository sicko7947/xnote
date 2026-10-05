package xnote

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shareFixture(t *testing.T) (*Store, string) {
	t.Helper()
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Catalog("TEST", []DeviceFile{{"20261006120000", 10}}); e != nil {
		t.Fatal(e)
	}
	id := "TEST-20261006120000"
	if e = s.Update(id, func(r *Record) {
		r.Title = "Meeting"
		r.Transcript = "Alice: Ship next week."
		r.Segments = []Segment{{Start: 0, End: 10, Speaker: "Alice", Text: "Ship next week.", Timing: "provider"}}
		r.CloudUID = "private-cloud"
		r.Duration = 10
	}); e != nil {
		t.Fatal(e)
	}
	return s, id
}
func TestSharePublishStableURLRevokeAndAllowlist(t *testing.T) {
	s, id := shareFixture(t)
	var requests []string
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 40) {
			t.Error("missing auth")
		}
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.Method == "PUT" && !strings.Contains(r.URL.Path, "/audio/") {
			if e := json.NewDecoder(r.Body).Decode(&payload); e != nil {
				t.Error(e)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	t.Setenv("XNOTE_SHARE_TOKEN", strings.Repeat("s", 40))
	c := s.Config()
	c.ShareURL = srv.URL
	s.SaveConfig(c)
	first, e := s.Publish(context.Background(), id, false)
	if e != nil {
		t.Fatal(e)
	}
	if first.Status != "published" || len(first.ID) != 48 {
		t.Fatal(first)
	}
	for _, k := range []string{"device_serial", "cloud_uid", "audio_path", "id", "provider"} {
		if _, ok := payload[k]; ok {
			t.Fatalf("leaked %s", k)
		}
	}
	again, e := s.Publish(context.Background(), id, false)
	if e != nil || again.URL != first.URL {
		t.Fatal(again, e)
	}
	if e = s.RevokeShare(context.Background(), id); e != nil {
		t.Fatal(e)
	}
	state, _ := s.ShareInfo(id)
	if state.Status != "revoked" {
		t.Fatal(state)
	}
	next, e := s.Publish(context.Background(), id, false)
	if e != nil || next.ID == first.ID {
		t.Fatal("revoked URL was reused", e)
	}
	if len(requests) != 4 || requests[2] != "DELETE /api/shares/"+first.ID {
		t.Fatal(requests)
	}
}
func TestShareAudioStreamingAndFailureRetry(t *testing.T) {
	s, id := shareFixture(t)
	r, _ := s.Get(id)
	audio := filepath.Join(s.Dir(r), "audio.mp3")
	os.WriteFile(audio, []byte("synthetic-audio"), 0600)
	s.Update(id, func(r *Record) { r.Audio = audio })
	fail := true
	audioSeen := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/audio/") {
			b, _ := io.ReadAll(r.Body)
			audioSeen = string(b) == "synthetic-audio" && r.Header.Get("Content-Type") == "audio/mpeg" && r.ContentLength == 15
			return
		}
		if fail {
			w.WriteHeader(503)
			return
		}
		var d ShareDocument
		json.NewDecoder(r.Body).Decode(&d)
		if len(d.Audio) != 32 {
			t.Error("missing audio revision")
		}
	}))
	defer srv.Close()
	t.Setenv("XNOTE_SHARE_TOKEN", strings.Repeat("s", 40))
	c := s.Config()
	c.ShareURL = srv.URL
	s.SaveConfig(c)
	state, e := s.Publish(context.Background(), id, true)
	if e == nil || state == nil || !audioSeen {
		t.Fatal(state, e, audioSeen)
	}
	saved, _ := s.ShareInfo(id)
	if saved.Status != "pending" {
		t.Fatal(saved)
	}
	fail = false
	retried, e := s.Publish(context.Background(), id, true)
	if e != nil || retried.ID != saved.ID {
		t.Fatal(retried, e)
	}
}
func TestShareRefusesRedirectAndInsecureOrigin(t *testing.T) {
	for _, value := range []string{"http://public.example", "https://u:p@example.com", "https://example.com/path", "https://example.com?token=secret", ""} {
		if _, e := shareEndpoint(value); e == nil {
			t.Fatal(value)
		}
	}
	targetCalled := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalled = true }))
	defer target.Close()
	from := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer from.Close()
	e := shareRequest(context.Background(), shareClient(), "PUT", from.URL, "secret", "application/json", strings.NewReader("{}"), 2)
	if e == nil || targetCalled {
		t.Fatal("redirect leaked request", e)
	}
}
func TestShareInsightsUseCurrentTranscriptAndSelectedOutputs(t *testing.T) {
	s, id := shareFixture(t)
	r, _ := s.Get(id)
	r.Summary = &SummaryResult{Markdown: "Summary", Mindmap: &Mindmap{Title: "Meeting", Branches: []MindmapBranch{{Title: "Plan", Points: []string{"Ship"}}}}}
	r.SummaryState = "done"
	r.SummaryInputHash = summaryInputHash(r)
	a := shareInsights(r, Config{})
	if a == nil || a.Summary != "Summary" || a.Mindmap != nil {
		t.Fatal("default outputs", a)
	}
	a = shareInsights(r, Config{MindmapEnabled: true, SummaryDisabled: true})
	if a == nil || a.Summary != "" || a.Mindmap == nil {
		t.Fatal("mindmap-only outputs", a)
	}
	if shareInsights(r, Config{SummaryDisabled: true}) != nil {
		t.Fatal("disabled content shared")
	}
	r.Title = "Renamed"
	if shareInsights(r, Config{}) == nil {
		t.Fatal("title invalidated analysis")
	}
	r.Segments[0].Speaker = "Bob"
	if shareInsights(r, Config{}) != nil {
		t.Fatal("stale analysis shared")
	}
}
func TestShareOperationLockHonorsCancellation(t *testing.T) {
	s, id := shareFixture(t)
	unlock, e := s.recordOperationLock(context.Background(), id, "share")
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, e = s.recordOperationLock(ctx, id, "share"); e == nil {
		t.Fatal("concurrent operation was not blocked")
	}
}
