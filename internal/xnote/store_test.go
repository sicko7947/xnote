package xnote

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDeviceCRCAndFragments(t *testing.T) {
	if crc16([]byte("123456789")) != 0xbb3d {
		t.Fatal("CRC")
	}
	if hex.EncodeToString(frame(2, nil)) != "a00a01020061d3" {
		t.Fatal("request frame")
	}
	d := &Device{replies: make(chan reply, 5), fail: make(chan error, 1)}
	p, _ := hex.DecodeString("a00a010a1400323032363037303932323033313400004a6401463f")
	d.receive(append([]byte("noise"), p[:5]...))
	if len(d.replies) != 0 {
		t.Fatal("partial accepted")
	}
	d.receive(p[5:])
	r := <-d.replies
	if r.op != 10 || string(r.data[1:15]) != "20260709220314" {
		t.Fatal("wrong reply")
	}
	p[len(p)-1] ^= 1
	d.receive(p)
	if len(d.replies) != 0 {
		t.Fatal("bad CRC accepted")
	}
}
func TestCatalogKeepsTranscriptTitleAndTrash(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	files := []DeviceFile{{"20260709220314", 19044}}
	if e = s.Catalog("HD5GA00725", files); e != nil {
		t.Fatal(e)
	}
	id := "HD5GA00725-20260709220314"
	if e = s.Update(id, func(r *Record) {
		r.Title = "会议 Project"
		r.Transcript = "讨论 Sydney 招标计划"
		r.State = "done"
		r.Provider = "codex"
	}); e != nil {
		t.Fatal(e)
	}
	if e = s.Catalog("HD5GA00725", files); e != nil {
		t.Fatal(e)
	}
	hits, e := s.Search("sydney 计划", false)
	if e != nil || len(hits) != 1 {
		t.Fatal(hits, e)
	}
	b, e := os.ReadFile(filepath.Join(hits[0].Directory, "transcript.md"))
	if e != nil || !bytes.Contains(b, []byte("招标")) {
		t.Fatal("missing plain text export", e)
	}
	_ = s.Update(id, func(r *Record) { r.Trashed = true })
	hits, _ = s.Search("", false)
	if len(hits) != 0 {
		t.Fatal("trash in search")
	}
	hits, _ = s.Search("", true)
	if len(hits) != 1 {
		t.Fatal("trash missing")
	}
	if e = s.Catalog("HD5GA00725", nil); e != nil {
		t.Fatal(e)
	}
	r, _ := s.Get(id)
	if r.OnDevice || r.Transcript == "" || !r.Trashed {
		t.Fatal("directory refresh lost library metadata")
	}
}
func TestConcurrentChangesAndSingleWorker(t *testing.T) {
	s, _ := Open(t.TempDir())
	_ = s.Catalog("HD5GA00725", []DeviceFile{{"20260709220314", 10}})
	id := "HD5GA00725-20260709220314"
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if e := s.Update(id, func(r *Record) { r.Title = "new title" }); e != nil {
			t.Error(e)
		}
	}()
	go func() {
		defer wg.Done()
		if e := s.Update(id, func(r *Record) { r.Transcript = "words" }); e != nil {
			t.Error(e)
		}
	}()
	wg.Wait()
	r, _ := s.Get(id)
	if r.Title != "new title" || r.Transcript != "words" {
		t.Fatal("lost concurrent update")
	}
	owner, e := s.Owner()
	if e != nil {
		t.Fatal(e)
	}
	defer owner.Close()
	if other, e := s.Owner(); e == nil {
		other.Close()
		t.Fatal("two BLE owners")
	}
}
func TestSignBodyAppRules(t *testing.T) {
	body := map[string]any{"account": "a+b@example.com", "passWd": "a&b=c", "mobile": map[string]any{"uuid": "x"}, "flag": true}
	r := signBody(body, "test-key", 123)
	if r["signature"] != "P1FMmAZlEQ31bAP3mGHT5b4QlLdHDBtnHciIICr7htw=" {
		t.Fatal("signature does not match independent HMAC fixture")
	}
	if _, ok := body["timestamp"]; ok {
		t.Fatal("input mutated")
	}
}
func TestFileValidationAndIdentity(t *testing.T) {
	s, _ := Open(t.TempDir())
	if e := s.Catalog("../escape", []DeviceFile{{"20260709220314", 10}}); e == nil {
		t.Fatal("path traversal")
	}
	p := filepath.Join(t.TempDir(), "bad.mp3")
	_ = os.WriteFile(p, []byte("not an mp3"), 0600)
	if e := ValidateMP3(p); e == nil {
		t.Fatal("invalid audio accepted")
	}
}
func TestLanguagesAndSelection(t *testing.T) {
	s, _ := Open(t.TempDir())
	_ = s.Catalog("HD5GA00725", []DeviceFile{{"20260709220314", 10}})
	d := newDesktop(context.Background(), s)
	for _, locale := range []string{"zh-CN", "en", "ja"} {
		c := s.Config()
		c.Locale = locale
		_ = s.SaveConfig(c)
		d.refresh()
		if d.selected != "HD5GA00725-20260709220314" {
			t.Fatal("selection lost")
		}
		if d.library.GetRowCount() != 2 {
			t.Fatal("missing row")
		}
		for k, values := range words {
			for _, v := range values {
				if v == "" {
					t.Fatal("missing translation", k)
				}
			}
		}
	}
}

func TestMetadataJSONIsReadable(t *testing.T) {
	s, _ := Open(t.TempDir())
	_ = s.Catalog("HD5GA00725", []DeviceFile{{"20260709220314", 10}})
	rows, _ := s.Records()
	b, _ := os.ReadFile(filepath.Join(s.Dir(rows[0]), "metadata.json"))
	var v map[string]any
	if e := json.Unmarshal(b, &v); e != nil || v["device_filename"] != "20260709220314" {
		t.Fatal(e)
	}
}

func TestNativePlaybackSeek(t *testing.T) {
	path := os.Getenv("XNOTE_TEST_AUDIO")
	if path == "" {
		t.Skip("set XNOTE_TEST_AUDIO for physical audio check")
	}
	if e := ValidateMP3(path); e != nil {
		t.Fatal(e)
	}
	if e := PlayerOpen(path); e != nil {
		t.Fatal(e)
	}
	defer PlayerClose()
	PlayerToggle()
	_, duration, playing := PlayerPosition()
	if duration <= 0 || playing {
		t.Fatal("pause/duration failed")
	}
	indexed, e := AudioDuration(path)
	if e != nil || indexed < duration-0.2 || indexed > duration+0.2 {
		t.Fatal("duration mismatch", indexed, duration, e)
	}
	target := duration / 2
	PlayerSeek(target)
	position, _, _ := PlayerPosition()
	if position < target-0.2 || position > target+0.2 {
		t.Fatal("seek mismatch", position, target)
	}
	PlayerRate(1.5)
	bar := &timeline{Box: tview.NewBox()}
	bar.SetRect(0, 0, 132, 3)
	consumed, _ := bar.MouseHandler()(tview.MouseLeftClick, tcell.NewEventMouse(79, 1, tcell.Button1, 0), func(tview.Primitive) {})
	position, _, _ = PlayerPosition()
	if !consumed || position < duration*0.75-0.2 || position > duration*0.75+0.2 {
		t.Fatal("mouse seek mismatch", position, duration)
	}
}
func BenchmarkLibrarySearch(b *testing.B) {
	s, _ := Open(b.TempDir())
	files := []DeviceFile{}
	for i := 0; i < 100; i++ {
		files = append(files, DeviceFile{fmt.Sprintf("20260709%02d%02d00", i/60, i%60), 100})
	}
	_ = s.Catalog("HD5GA00725", files)
	rows, _ := s.Records()
	for _, r := range rows {
		_ = s.Update(r.ID, func(r *Record) {
			r.Transcript = strings.Repeat("meeting Sydney tender 内容 ", 1000)
			r.State = "done"
		})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, e := s.Search("sydney 内容", false); e != nil {
			b.Fatal(e)
		}
	}
}
