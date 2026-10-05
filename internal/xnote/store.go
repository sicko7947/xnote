package xnote

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Config struct {
	Locale                   string `json:"locale"`
	Serial                   string `json:"device_serial"`
	DeviceID                 string `json:"device_id"`
	Auto                     bool   `json:"automatic"`
	AutoTranscribe           bool   `json:"automatic_transcription"`
	AutoSummary              bool   `json:"automatic_summary"`
	SummaryConcurrency       int    `json:"summary_concurrency"`
	SummaryLanguage          string `json:"summary_language"`
	SummaryThinking          bool   `json:"summary_thinking"`
	TranscriptionConcurrency int    `json:"transcription_concurrency"`
	TranscriptionPaused      bool   `json:"transcription_paused"`
	DOWAYPublicUpload        bool   `json:"doway_public_upload"`
	Provider                 string `json:"provider"`
	Language                 string `json:"transcription_language"`
	APIURL                   string `json:"api_url"`
	APIKeyEnv                string `json:"api_key_env"`
	Model                    string `json:"api_model"`
	OfflineModel             string `json:"offline_model"`
}
type Record struct {
	SummaryState     string         `json:"summary_state,omitempty"`
	SummaryError     string         `json:"summary_error,omitempty"`
	SummaryInputHash string         `json:"summary_input_hash,omitempty"`
	Summary          *SummaryResult `json:"summary,omitempty"`
	TitleSource      string         `json:"title_source,omitempty"`
	Duration         float64        `json:"duration_seconds,omitempty"`
	CloudUID         string         `json:"cloud_uid,omitempty"`
	Segments         []Segment      `json:"segments,omitempty"`
	DownloadSeconds  float64        `json:"download_seconds,omitempty"`
	DownloadBytes    int64          `json:"download_bytes,omitempty"`
	ID               string         `json:"id"`
	Serial           string         `json:"device_serial"`
	DeviceName       string         `json:"device_filename"`
	Title            string         `json:"title"`
	RecordedAt       string         `json:"recorded_at"`
	Size             int64          `json:"size_bytes"`
	OnDevice         bool           `json:"on_device"`
	Audio            string         `json:"audio_path,omitempty"`
	Transcript       string         `json:"transcript,omitempty"`
	Provider         string         `json:"provider,omitempty"`
	State            string         `json:"state"`
	Error            string         `json:"error,omitempty"`
	Trashed          bool           `json:"trashed"`
	UpdatedAt        string         `json:"updated_at"`
}
type Status struct {
	BytesPerSecond float64 `json:"bytes_per_second,omitempty"`
	Phase          string  `json:"phase"`
	Battery        int     `json:"battery"`
	Detail         string  `json:"detail,omitempty"`
	Current        string  `json:"current,omitempty"`
	Progress       int     `json:"progress"`
	UpdatedAt      string  `json:"updated_at"`
}
type Store struct {
	Root         string
	recordsMu    sync.Mutex
	recordsCache map[string]cachedRecord
}

type cachedRecord struct {
	info   os.FileInfo
	record Record
}

// Callers may mutate records, so cached slices and pointers never escape.
func cloneRecord(r Record) Record {
	r.Segments = append([]Segment(nil), r.Segments...)
	if r.Summary != nil {
		summary := *r.Summary
		summary.Keywords = append([]string(nil), summary.Keywords...)
		r.Summary = &summary
	}
	return r
}

func Open(root string) (*Store, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	for _, d := range []string{"", "recordings", ".work"} {
		if err = os.MkdirAll(filepath.Join(root, d), 0700); err != nil {
			return nil, err
		}
	}
	s := &Store{Root: root}
	if _, err = os.Stat(s.path("config.json")); errors.Is(err, os.ErrNotExist) {
		err = s.SaveConfig(Config{Locale: "zh-CN", Serial: "HD5GA00725", Auto: true, TranscriptionConcurrency: 4, SummaryConcurrency: 2, Provider: "doway", APIKeyEnv: "XNOTE_API_KEY", Model: "whisper-1"})
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}
func (s *Store) path(p string) string { return filepath.Join(s.Root, p) }
func atomicJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return atomicWrite(path, append(b, '\n'))
}
func atomicWrite(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".write-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
func readJSON(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func (s *Store) Config() (c Config) { _ = readJSON(s.path("config.json"), &c); return }
func (s *Store) SaveConfig(c Config) error {
	if c.Locale != "zh-CN" && c.Locale != "en" && c.Locale != "ja" {
		return errors.New("unsupported UI language")
	}
	switch c.Provider {
	case "codex", "api", "offline", "doway", "elevenlabs":
	default:
		return errors.New("unsupported provider")
	}
	if c.TranscriptionConcurrency < 0 || c.TranscriptionConcurrency > 16 {
		return errors.New("transcription_concurrency must be between 1 and 16 (or 0 for the default)")
	}
	if c.SummaryConcurrency < 0 || c.SummaryConcurrency > 8 {
		return errors.New("summary_concurrency must be between 1 and 8 (or 0 for the default)")
	}
	switch c.SummaryLanguage {
	case "", "zh-CN", "en", "ja":
	default:
		return errors.New("summary_language must be empty, zh-CN, en, or ja")
	}
	if !safePart(c.Serial) {
		return errors.New("invalid device serial")
	}
	return atomicJSON(s.path("config.json"), c)
}

// EffectiveTranscriptionConcurrency preserves the default for older libraries
// whose configuration does not yet contain a concurrency setting.
func EffectiveTranscriptionConcurrency(c Config) int {
	if c.TranscriptionConcurrency == 0 {
		return 4
	}
	return max(1, min(16, c.TranscriptionConcurrency))
}

// EffectiveSummaryConcurrency keeps older libraries at the default of two jobs.
func EffectiveSummaryConcurrency(c Config) int {
	if c.SummaryConcurrency == 0 {
		return 2
	}
	return max(1, min(8, c.SummaryConcurrency))
}

func safePart(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func validName(s string) bool {
	if len(s) != 14 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func (s *Store) Dir(r Record) string {
	month := "imported"
	if validName(r.DeviceName) {
		month = r.DeviceName[:4] + "/" + r.DeviceName[4:6]
	}
	return s.path(filepath.Join("recordings", month, r.ID))
}
func (s *Store) Records() ([]Record, error) {
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()
	cache := make(map[string]cachedRecord, len(s.recordsCache))
	rows := []Record{}
	err := filepath.WalkDir(s.path("recordings"), func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Name() != "metadata.json" || d.IsDir() {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		entry, ok := s.recordsCache[p]
		// Stat every metadata file: a second CLI can edit, add, or delete
		// records. SameFile also detects atomic replacements with preserved
		// modification times and sizes.
		if !ok || !os.SameFile(entry.info, info) || entry.info.Size() != info.Size() || !entry.info.ModTime().Equal(info.ModTime()) {
			var r Record
			if e = readJSON(p, &r); e != nil {
				return fmt.Errorf("read metadata %s: %w", p, e)
			}
			if !safePart(r.ID) {
				return errors.New("invalid recording identity")
			}
			entry = cachedRecord{info: info, record: r}
		}
		cache[p] = entry
		rows = append(rows, cloneRecord(entry.record))
		return nil
	})
	if err == nil {
		s.recordsCache = cache
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].RecordedAt > rows[j].RecordedAt })
	return rows, err
}
func (s *Store) Get(id string) (Record, error) {
	rows, e := s.Records()
	if e != nil {
		return Record{}, e
	}
	for _, r := range rows {
		if r.ID == id {
			return r, nil
		}
	}
	return Record{}, errors.New("recording not found")
}
func (s *Store) mutate(fn func() error) error {
	f, e := os.OpenFile(s.path(".work/write.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); e != nil {
		return e
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
func (s *Store) save(r Record) error {
	if !safePart(r.ID) || !safePart(r.Serial) || !validName(r.DeviceName) {
		return errors.New("invalid record identity")
	}
	r.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	dir := s.Dir(r)
	if len(r.Segments) == 0 {
		if e := os.Remove(filepath.Join(dir, "segments.json")); e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	if r.Transcript != "" || r.State == "no_speech" {
		var md strings.Builder
		if len(r.Segments) > 0 {
			fmt.Fprintf(&md, "# %s\n\nRecorded: %s\nID: %s\nAudio: [audio.mp3](audio.mp3)\n\n", strings.ReplaceAll(r.Title, "\n", " "), r.RecordedAt, r.ID)
			for _, seg := range r.Segments {
				label := clockTime(seg.Start)
				if seg.Timing == "chunk" {
					label += " (chunk start; approximate)"
				}
				fmt.Fprintf(&md, "## %s %s\n\n%s\n\n", label, seg.Speaker, seg.Text)
			}
			if e := atomicJSON(filepath.Join(dir, "segments.json"), r.Segments); e != nil {
				return e
			}
		} else {
			fmt.Fprintf(&md, "# %s\n\nRecorded: %s\nDevice: %s\nID: %s\nAudio: [audio.mp3](audio.mp3)\nTranscription: %s\n\n%s\n", strings.ReplaceAll(r.Title, "\n", " "), r.RecordedAt, r.Serial, r.ID, r.Provider, r.Transcript)
		}
		if r.State == "no_speech" {
			md.WriteString("\n_No speech detected by the transcription provider._\n")
		}
		if e := atomicWrite(filepath.Join(dir, "transcript.md"), []byte(md.String())); e != nil {
			return e
		}
	}
	summaryPath := filepath.Join(dir, "summary.md")
	if r.Summary != nil {
		var summary strings.Builder
		fmt.Fprintf(&summary, "# %s\n\n", strings.Join(strings.Fields(r.Summary.Title), " "))
		if r.Summary.Warning != "" {
			fmt.Fprintf(&summary, "> Note: %s\n\n", strings.Join(strings.Fields(r.Summary.Warning), " "))
		}
		if len(r.Summary.Keywords) > 0 {
			keywords := make([]string, len(r.Summary.Keywords))
			for i, keyword := range r.Summary.Keywords {
				keywords[i] = strings.Join(strings.Fields(keyword), " ")
			}
			fmt.Fprintf(&summary, "Keywords: %s\n\n", strings.Join(keywords, ", "))
		}
		summary.WriteString(strings.TrimSpace(r.Summary.Markdown))
		summary.WriteByte('\n')
		if err := atomicWrite(summaryPath, []byte(summary.String())); err != nil {
			return err
		}
	}
	return atomicJSON(filepath.Join(dir, "metadata.json"), r)
}
func (s *Store) Update(id string, fn func(*Record)) error {
	return s.mutate(func() error {
		r, e := s.Get(id)
		if e != nil {
			return e
		}
		fn(&r)
		return s.save(r)
	})
}
func (s *Store) Catalog(serial string, files []DeviceFile) error {
	return s.mutate(func() error {
		rows, e := s.Records()
		if e != nil {
			return e
		}
		existing := map[string]Record{}
		for _, r := range rows {
			existing[r.ID] = r
		}
		seen := map[string]bool{}
		for _, f := range files {
			id := serial + "-" + f.Name
			seen[id] = true
			r, ok := existing[id]
			if !ok {
				date, e := time.ParseInLocation("20060102150405", f.Name, time.Local)
				if e != nil {
					return e
				}
				r = Record{ID: id, Serial: serial, DeviceName: f.Name, Title: date.Format("2006-01-02 15:04:05"), RecordedAt: date.Format(time.RFC3339), State: "on_device"}
			}
			if ok && r.Size == f.Size && r.OnDevice {
				continue
			}
			r.Size = f.Size
			r.OnDevice = true
			if e = s.save(r); e != nil {
				return e
			}
		}
		for _, r := range rows {
			if r.Serial == serial && r.OnDevice && !seen[r.ID] {
				r.OnDevice = false
				if e = s.save(r); e != nil {
					return e
				}
			}
		}
		return nil
	})
}
func (s *Store) SetStatus(st Status) {
	st.UpdatedAt = time.Now().Format(time.RFC3339Nano)
	_ = atomicJSON(s.path(".work/status.json"), st)
}
func (s *Store) Status() (st Status) {
	st.Phase = "stopped"
	st.Battery = -1
	_ = readJSON(s.path(".work/status.json"), &st)
	if t, e := time.Parse(time.RFC3339, st.UpdatedAt); e != nil || time.Since(t) > 45*time.Second {
		st.Phase = "stopped"
	}
	return
}
func (s *Store) Owner() (*os.File, error) {
	f, e := os.OpenFile(s.path(".work/engine.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("sync already running in another process")
	}
	return f, nil
}

type Hit struct {
	Record    Record    `json:"recording"`
	Snippet   string    `json:"snippet"`
	Directory string    `json:"directory"`
	Matches   []Segment `json:"matches,omitempty"`
}

func (s *Store) Search(query string, trash bool) ([]Hit, error) {
	rows, e := s.Records()
	if e != nil {
		return nil, e
	}
	return s.searchRecords(rows, query, trash), nil
}

func (s *Store) searchRecords(rows []Record, query string, trash bool) []Hit {
	hits := []Hit{}
	terms := strings.Fields(strings.ToLower(query))
	for _, r := range rows {
		if r.Trashed != trash {
			continue
		}
		match := true
		if len(terms) > 0 {
			var text strings.Builder
			text.WriteString(r.Title + " " + r.RecordedAt + " " + r.ID + " " + r.Transcript)
			for _, seg := range r.Segments {
				text.WriteByte(' ')
				text.WriteString(seg.Speaker)
			}
			hay := strings.ToLower(text.String())
			for _, term := range terms {
				if !strings.Contains(hay, term) {
					match = false
					break
				}
			}
		}
		if !match {
			continue
		}
		snippet := []rune(strings.ReplaceAll(r.Transcript, "\n", " "))
		start := 0
		if len(terms) > 0 {
			lower := strings.ToLower(string(snippet))
			if i := strings.Index(lower, terms[0]); i >= 0 {
				start = len([]rune(lower[:i])) - 35
				if start < 0 {
					start = 0
				}
			}
		}
		end := start + 180
		if end > len(snippet) {
			end = len(snippet)
		}
		matches := []Segment{}
		if len(terms) > 0 {
			for _, seg := range r.Segments {
				if matchesTerms(seg.Text+" "+seg.Speaker, terms) {
					matches = append(matches, seg)
				}
			}
		}
		hits = append(hits, Hit{Record: r, Snippet: string(snippet[start:end]), Directory: s.Dir(r), Matches: matches})
	}
	return hits
}

func matchesTerms(text string, terms []string) bool {
	text = strings.ToLower(text)
	for _, term := range terms {
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}
