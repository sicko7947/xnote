package xnote

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hajimehoshi/go-mp3"
)

type Segment struct {
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Speaker string  `json:"speaker,omitempty"`
	Text    string  `json:"text"`
	Timing  string  `json:"timing"`
}
type TranscriptResult struct {
	Text     string    `json:"text"`
	Segments []Segment `json:"segments,omitempty"`
}

func TranscribeRecording(ctx context.Context, c Config, path string) (TranscriptResult, error) {
	client := &http.Client{Timeout: 20 * time.Minute, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if c.Provider == "offline" || c.Provider == "doway" {
		return requestTranscription(ctx, c, path, client)
	}
	f, e := os.Open(path)
	if e != nil {
		return TranscriptResult{}, e
	}
	defer f.Close()
	decoder, e := mp3.NewDecoder(f)
	if e != nil {
		return requestTranscription(ctx, c, path, client)
	}
	bytesPerSecond := decoder.SampleRate() * 4 // go-mp3 outputs stereo signed 16-bit PCM.
	chunkSize := min(bytesPerSecond*180, 18<<20)
	chunkSize -= chunkSize % 4
	if decoder.Length() > 0 && decoder.Length() <= int64(chunkSize) {
		return requestTranscription(ctx, c, path, client)
	}
	hash := sha256.New()
	original, e := os.Open(path)
	if e != nil {
		return TranscriptResult{}, e
	}
	_, e = io.Copy(hash, original)
	original.Close()
	if e != nil {
		return TranscriptResult{}, e
	}
	configBytes, _ := json.Marshal([]string{c.Provider, c.APIURL, c.APIKeyEnv, c.Model, c.Language, "chunks-v1"})
	hash.Write(configBytes)
	cache := filepath.Join(filepath.Dir(path), ".transcription", fmt.Sprintf("%x", hash.Sum(nil))[:20])
	if e = os.MkdirAll(cache, 0700); e != nil {
		return TranscriptResult{}, e
	}
	result := TranscriptResult{}
	consumed := int64(0)
	carry := []byte{}
	for index := 0; ; index++ {
		if e = ctx.Err(); e != nil {
			return result, e
		}
		block := make([]byte, chunkSize)
		copied := copy(block, carry)
		n, readErr := io.ReadFull(decoder, block[copied:])
		n += copied
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return result, readErr
		}
		if n == 0 {
			break
		}
		block = block[:n]
		cut := n
		// Prefer a quiet boundary in the final ten seconds rather than a hard cutoff.
		if readErr == nil {
			cut = quietCut(block, bytesPerSecond)
			carry = append([]byte(nil), block[cut:]...)
		} else {
			carry = nil
		}
		start := float64(consumed) / float64(bytesPerSecond)
		end := float64(consumed+int64(cut)) / float64(bytesPerSecond)
		cached := filepath.Join(cache, fmt.Sprintf("%04d.json", index))
		var part TranscriptResult
		if e = readJSON(cached, &part); e != nil {
			wav := filepath.Join(cache, fmt.Sprintf("%04d.wav", index))
			if e = writeWAV(wav, block[:cut], decoder.SampleRate()); e != nil {
				return result, e
			}
			part, e = requestTranscription(ctx, c, wav, client)
			_ = os.Remove(wav)
			if e != nil {
				return result, fmt.Errorf("segment %d at %s: %w", index+1, clockTime(start), e)
			}
			if e = atomicJSON(cached, part); e != nil {
				return result, e
			}
		}
		if part.Text != "" {
			if result.Text != "" {
				result.Text += "\n\n"
			}
			result.Text += part.Text
			if len(part.Segments) == 0 {
				result.Segments = append(result.Segments, Segment{Start: start, End: end, Text: part.Text, Timing: "chunk"})
			} else {
				for _, segment := range part.Segments {
					segment.Start += start
					segment.End += start
					if segment.Speaker != "" {
						segment.Speaker = fmt.Sprintf("part%d/%s", index+1, segment.Speaker)
					}
					result.Segments = append(result.Segments, segment)
				}
			}
		}
		consumed += int64(cut)
		_ = atomicJSON(filepath.Join(cache, "progress.json"), map[string]any{"completed_segments": index + 1, "processed_seconds": end})
		if readErr != nil {
			break
		}
	}
	result.Text = strings.TrimSpace(result.Text)
	return result, nil
}
func quietCut(pcm []byte, bytesPerSecond int) int {
	window := max(4, bytesPerSecond/10)
	window -= window % 4
	start := max(len(pcm)-10*bytesPerSecond, len(pcm)/2)
	start -= start % 4
	best := len(pcm)
	lowest := int64(1 << 62)
	for pos := start; pos+window <= len(pcm); pos += window {
		var energy int64
		for i := pos; i < pos+window; i += 2 {
			v := int64(int16(binary.LittleEndian.Uint16(pcm[i : i+2])))
			energy += v * v
		}
		if energy < lowest {
			lowest = energy
			best = pos + window
		}
	}
	return best
}
func writeWAV(path string, pcm []byte, rate int) error {
	header := make([]byte, 44)
	copy(header, "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+len(pcm)))
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 2)
	binary.LittleEndian.PutUint32(header[24:], uint32(rate))
	binary.LittleEndian.PutUint32(header[28:], uint32(rate*4))
	binary.LittleEndian.PutUint16(header[32:], 4)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(len(pcm)))
	return atomicWrite(path, append(header, pcm...))
}
