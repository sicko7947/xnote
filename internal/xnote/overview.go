package xnote

import "os"

type Overview struct {
	Total                  int     `json:"total"`
	Ready                  int     `json:"ready"`
	NoSpeech               int     `json:"no_speech"`
	PendingDownloads       int     `json:"pending_downloads"`
	PendingTranscriptions  int     `json:"pending_transcriptions"`
	Issues                 int     `json:"issues"`
	RemainingBytes         int64   `json:"remaining_bytes"`
	MeasuredBytesPerSecond float64 `json:"measured_bytes_per_second"`
	DownloadETASeconds     float64 `json:"download_eta_seconds,omitempty"`
}

func (s *Store) Overview() (Overview, error) {
	rows, e := s.Records()
	if e != nil {
		return Overview{}, e
	}
	o := Overview{}
	var bytes int64
	var elapsed float64
	for _, r := range rows {
		if r.Trashed {
			continue
		}
		o.Total++
		if r.DownloadBytes > 0 && r.DownloadSeconds > 0 {
			bytes += r.DownloadBytes
			elapsed += r.DownloadSeconds
		}
		if r.OnDevice && r.Audio == "" && r.Size > 0 {
			o.PendingDownloads++
			remaining := r.Size
			if stat, e := os.Stat(s.Dir(r) + "/audio.mp3.partial"); e == nil && stat.Size() < r.Size {
				remaining -= stat.Size()
			}
			o.RemainingBytes += remaining
		}
		switch r.State {
		case "done":
			o.Ready++
		case "no_speech":
			o.NoSpeech++
		case "error", "download_error":
			o.Issues++
		case "downloaded", "queued", "transcribing":
			o.PendingTranscriptions++
		}
	}
	if elapsed > 0 {
		o.MeasuredBytesPerSecond = float64(bytes) / elapsed
	}
	st := s.Status()
	rate := st.BytesPerSecond
	if rate <= 0 {
		rate = o.MeasuredBytesPerSecond
	}
	if rate > 0 && st.Phase == "downloading" {
		o.DownloadETASeconds = float64(o.RemainingBytes) / rate
	}
	return o, nil
}
