package xnote

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const maxShareAudio = 95 << 20

type ShareState struct {
	ID           string `json:"id"`
	URL          string `json:"url"`
	Endpoint     string `json:"endpoint"`
	Status       string `json:"status"`
	IncludeAudio bool   `json:"include_audio"`
	UpdatedAt    string `json:"updated_at"`
}
type ShareDocument struct {
	Title      string         `json:"title"`
	RecordedAt string         `json:"recorded_at"`
	Duration   float64        `json:"duration_seconds"`
	Transcript string         `json:"transcript"`
	Segments   []Segment      `json:"segments"`
	Insights   *ShareInsights `json:"insights,omitempty"`
	Audio      string         `json:"audio,omitempty"`
}
type ShareInsights struct {
	Summary     string   `json:"summary"`
	Mindmap     *Mindmap `json:"mindmap,omitempty"`
	Model       string   `json:"model"`
	GeneratedAt string   `json:"generated_at"`
}

func shareInsights(r Record, c Config) *ShareInsights {
	if r.Summary == nil || r.SummaryState != "done" || r.SummaryInputHash != summaryInputHash(r) {
		return nil
	}
	a := &ShareInsights{Model: r.Summary.Model, GeneratedAt: r.Summary.GeneratedAt}
	if a.Model == "" {
		a.Model = "DOWAY"
	}
	if !c.SummaryDisabled {
		a.Summary = r.Summary.Markdown
	}
	if c.MindmapEnabled {
		a.Mindmap = r.Summary.Mindmap
	}
	if a.Summary == "" && a.Mindmap == nil {
		return nil
	}
	return a
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	_, e := rand.Read(b)
	return hex.EncodeToString(b), e
}
func shareEndpoint(raw string) (string, error) {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return "", errors.New("configure share_url as an HTTPS origin, without a path or credentials")
	}
	// HTTP is only accepted for local Wrangler development.
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
		return "", errors.New("share_url requires HTTPS (except localhost)")
	}
	return strings.TrimRight(u.String(), "/"), nil
}
func (s *Store) shareStatePath(id string) string {
	return s.path(filepath.Join(".work", "shares", id+".json"))
}
func (s *Store) ShareInfo(id string) (*ShareState, error) {
	if !safePart(id) {
		return nil, errors.New("invalid recording ID")
	}
	var state ShareState
	e := readJSON(s.shareStatePath(id), &state)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return &state, nil
}
func (s *Store) recordOperationLock(ctx context.Context, id, operation string) (func(), error) {
	if !safePart(id) {
		return nil, errors.New("invalid recording ID")
	}
	f, e := os.OpenFile(s.path(filepath.Join(".work", operation+"-"+id+".lock")), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	for {
		e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if e == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if e != syscall.EWOULDBLOCK && e != syscall.EAGAIN {
			f.Close()
			return nil, e
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func shareClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func shareRequest(ctx context.Context, client *http.Client, method, endpoint, token, contentType string, body io.Reader, length int64) error {
	req, e := http.NewRequestWithContext(ctx, method, endpoint, body)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if length >= 0 {
		req.ContentLength = length
	}
	resp, e := client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("share service returned HTTP %d", resp.StatusCode)
	}
	return nil
}
func (s *Store) Publish(ctx context.Context, id string, includeAudio bool) (*ShareState, error) {
	unlock, e := s.recordOperationLock(ctx, id, "share")
	if e != nil {
		return nil, e
	}
	defer unlock()
	r, e := s.Get(id)
	if e != nil {
		return nil, e
	}
	if r.Trashed {
		return nil, errors.New("cannot share a trashed recording")
	}
	if strings.TrimSpace(r.Transcript) == "" && !includeAudio {
		return nil, errors.New("recording has no transcript; include audio or transcribe first")
	}
	endpoint, e := shareEndpoint(s.Config().ShareURL)
	if e != nil {
		return nil, e
	}
	token := os.Getenv("XNOTE_SHARE_TOKEN")
	if len(token) < 32 {
		return nil, errors.New("set XNOTE_SHARE_TOKEN in the private library .env (at least 32 characters)")
	}
	state, e := s.ShareInfo(id)
	if e != nil {
		return nil, e
	}
	if state != nil && state.Status != "revoked" && state.Endpoint != endpoint {
		return nil, errors.New("share_url changed; revoke the existing share before publishing to another service")
	}
	if state == nil || state.Status == "revoked" {
		sid, err := randomHex(24)
		if err != nil {
			return nil, err
		}
		state = &ShareState{ID: sid, Endpoint: endpoint, URL: endpoint + "/s/" + sid, Status: "pending"}
	}
	insights := shareInsights(r, s.Config())
	segments := r.Segments
	if segments == nil {
		segments = []Segment{}
	}
	doc := ShareDocument{Title: displayTitle(r), RecordedAt: r.RecordedAt, Duration: r.Duration, Transcript: r.Transcript, Segments: segments, Insights: insights}
	var audio *os.File
	if includeAudio {
		if r.Audio == "" {
			return nil, errors.New("download audio before sharing")
		}
		resolved, err := filepath.EvalSymlinks(r.Audio)
		if err != nil {
			return nil, err
		}
		root, err := filepath.EvalSymlinks(s.Root)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.Base(resolved) != "audio.mp3" {
			return nil, errors.New("share audio must be an audio.mp3 inside the local library")
		}
		audio, e = os.Open(resolved)
		if e != nil {
			return nil, e
		}
		defer audio.Close()
		stat, err := audio.Stat()
		if err != nil {
			return nil, err
		}
		if !stat.Mode().IsRegular() || stat.Size() == 0 || stat.Size() > maxShareAudio {
			return nil, errors.New("shared audio must be between 1 byte and 95 MiB")
		}
		doc.Audio, e = randomHex(16)
		if e != nil {
			return nil, e
		}
	}
	payload, e := json.Marshal(doc)
	if e != nil {
		return nil, e
	}
	if len(payload) > 2<<20 {
		return nil, errors.New("share document exceeds 2 MiB")
	}
	// Keep the random ID before networking, so retries after a lost response reuse the same URL.
	if e = atomicJSON(s.shareStatePath(id), state); e != nil {
		return nil, e
	}
	client := shareClient()
	api := endpoint + "/api/shares/" + state.ID
	if audio != nil {
		stat, err := audio.Stat()
		if err != nil {
			return nil, err
		}
		if e = shareRequest(ctx, client, "PUT", api+"/audio/"+doc.Audio, token, "audio/mpeg", audio, stat.Size()); e != nil {
			return state, e
		}
	}
	if e = shareRequest(ctx, client, "PUT", api, token, "application/json", bytes.NewReader(payload), int64(len(payload))); e != nil {
		return state, e
	}
	state.Status = "published"
	state.IncludeAudio = includeAudio
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	e = atomicJSON(s.shareStatePath(id), state)
	return state, e
}
func (s *Store) RevokeShare(ctx context.Context, id string) error {
	unlock, e := s.recordOperationLock(ctx, id, "share")
	if e != nil {
		return e
	}
	defer unlock()
	state, e := s.ShareInfo(id)
	if e != nil {
		return e
	}
	if state == nil || state.Status == "revoked" {
		return nil
	}
	endpoint, e := shareEndpoint(state.Endpoint)
	if e != nil {
		return e
	}
	token := os.Getenv("XNOTE_SHARE_TOKEN")
	if len(token) < 32 {
		return errors.New("set XNOTE_SHARE_TOKEN in the private library .env")
	}
	if e = shareRequest(ctx, shareClient(), "DELETE", endpoint+"/api/shares/"+state.ID, token, "", nil, 0); e != nil {
		return e
	}
	state.Status = "revoked"
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return atomicJSON(s.shareStatePath(id), state)
}
