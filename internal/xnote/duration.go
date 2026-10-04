package xnote

import (
	"context"
	"fmt"
	"os"

	"github.com/hajimehoshi/go-mp3"
)

func AudioDuration(path string) (float64, error) {
	f, e := os.Open(path)
	if e != nil {
		return 0, e
	}
	defer f.Close()
	decoder, e := mp3.NewDecoder(f)
	if e != nil {
		return 0, e
	}
	if decoder.Length() <= 0 || decoder.SampleRate() <= 0 {
		return 0, fmt.Errorf("audio duration unavailable")
	}
	return float64(decoder.Length()) / float64(decoder.SampleRate()*4), nil
}
func (s *Store) IndexDurations(ctx context.Context) error {
	rows, e := s.Records()
	if e != nil {
		return e
	}
	for _, r := range rows {
		if e = ctx.Err(); e != nil {
			return e
		}
		if r.Audio == "" || r.Duration > 0 {
			continue
		}
		duration, e := AudioDuration(r.Audio)
		if e != nil {
			continue
		}
		if e = s.Update(r.ID, func(current *Record) {
			if current.Audio == r.Audio && current.Duration == 0 {
				current.Duration = duration
			}
		}); e != nil {
			return e
		}
	}
	return nil
}
func durationLabel(r Record) string {
	if r.Duration > 0 {
		return clockTime(r.Duration)
	}
	return "—"
}
