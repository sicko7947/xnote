package xnote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// DOWAY RecordSentence stores milliseconds. The app's play manager converts
// these to Duration microseconds by multiplying by 1000 before AudioPlayer.seek.
func ParseDOWAYTranscript(raw json.RawMessage) (TranscriptResult, error) {
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = []byte(encoded)
	}
	var source struct {
		Content   string `json:"content"`
		Sentences []struct {
			Start       *int64          `json:"start"`
			End         *int64          `json:"end"`
			Text        string          `json:"text"`
			Speaker     json.RawMessage `json:"speaker"`
			SpeakerName string          `json:"speakerName"`
		} `json:"sentences"`
	}
	if e := json.Unmarshal(raw, &source); e != nil {
		return TranscriptResult{}, fmt.Errorf("DOWAY transcript: %w", e)
	}
	result := TranscriptResult{Text: source.Content}
	texts := []string{}
	for i, s := range source.Sentences {
		if s.Start == nil || s.End == nil || *s.Start < 0 || *s.End < *s.Start {
			return TranscriptResult{}, fmt.Errorf("DOWAY sentence %d has invalid timing", i+1)
		}
		speaker := s.SpeakerName
		if speaker == "" && len(s.Speaker) > 0 && string(s.Speaker) != "null" {
			if json.Unmarshal(s.Speaker, &speaker) != nil {
				var n json.Number
				if e := json.Unmarshal(s.Speaker, &n); e != nil {
					return TranscriptResult{}, errors.New("invalid DOWAY speaker")
				}
				speaker = n.String()
			}
		}
		result.Segments = append(result.Segments, Segment{Start: float64(*s.Start) / 1000, End: float64(*s.End) / 1000, Text: s.Text, Speaker: speaker, Timing: "provider"})
		texts = append(texts, s.Text)
	}
	if strings.TrimSpace(result.Text) == "" {
		result.Text = strings.Join(texts, "\n")
	}
	if strings.TrimSpace(result.Text) == "" {
		return result, errors.New("DOWAY recording has no completed transcript")
	}
	return result, nil
}

func (s *Store) CloudInfo(ctx context.Context, uid string) (map[string]json.RawMessage, error) {
	var session map[string]any
	if e := readJSON(s.path(".work/doway-session.json"), &session); e != nil {
		return nil, errors.New("DOWAY login required")
	}
	data, e := cloudPost(ctx, "/api/cloud/get_file_info", map[string]any{"audioFileUid": uid, "playerId": session["playerId"], "token": session["token"]})
	if e != nil {
		return nil, e
	}
	var record map[string]json.RawMessage
	if e = json.Unmarshal(data, &record); e != nil {
		return nil, e
	}
	return record, nil
}

// Import requires an explicit local ID and cloud UID, never a date-based guess.
// Preserve the previous result before replacing the local transcription.
func (s *Store) ImportCloud(ctx context.Context, localID, uid string) error {
	data, e := s.CloudInfo(ctx, uid)
	if e != nil {
		return e
	}
	result, e := ParseDOWAYTranscript(data["transcriptJson"])
	if e != nil {
		return e
	}
	var title string
	_ = json.Unmarshal(data["fileName"], &title)
	return s.mutate(func() error {
		r, e := s.Get(localID)
		if e != nil {
			return e
		}
		if r.State == "transcribing" || r.State == "queued" {
			return errors.New("wait for the active transcription before importing")
		}
		archive := filepath.Join(s.Dir(r), "history", time.Now().Format("20060102T150405.000000000"))
		if e = atomicJSON(archive+".json", r); e != nil {
			return e
		}
		if e = atomicJSON(archive+"-doway.json", data); e != nil {
			return e
		}
		r.Transcript = result.Text
		r.Segments = result.Segments
		r.Provider = "doway"
		r.CloudUID = uid
		r.State = "done"
		r.Error = ""
		if strings.TrimSpace(title) != "" && r.TitleSource != "local" {
			r.Title = title
			r.TitleSource = "doway"
		}
		return s.save(r)
	})
}
