package xnote

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
)

// Match only an existing explicit binding or an exact audio checksum, never
// timestamps, display names, or Bluetooth UUIDs that may vary across platforms.
func (s *Store) SyncCloud(ctx context.Context) (int, error) {
	rows, e := s.Cloud(ctx)
	if e != nil {
		return 0, e
	}
	locals, e := s.Records()
	if e != nil {
		return 0, e
	}
	byHash := map[string][]string{}
	byUID := map[string][]string{}
	for _, r := range locals {
		if r.Trashed {
			continue
		}
		if r.CloudUID != "" {
			byUID[r.CloudUID] = append(byUID[r.CloudUID], r.ID)
			continue
		}
		if r.Audio == "" {
			continue
		}
		if e = ctx.Err(); e != nil {
			return 0, e
		}
		f, e := os.Open(r.Audio)
		if e != nil {
			continue
		}
		h := md5.New()
		_, e = io.Copy(h, f)
		f.Close()
		if e != nil {
			continue
		}
		key := hex.EncodeToString(h.Sum(nil))
		byHash[key] = append(byHash[key], r.ID)
	}
	count := 0
	for _, row := range rows {
		uid, _ := row["audioFileUID"].(string)
		if uid == "" {
			continue
		}
		ids := byUID[uid]
		if len(ids) == 0 {
			hash, _ := row["audioFileMd5"].(string)
			if hash != "" {
				ids = byHash[strings.ToLower(hash)]
			}
		}
		if len(ids) != 1 {
			continue
		}
		title, _ := row["fileName"].(string)
		raw, _ := json.Marshal(row["transcriptJson"])
		result, parseErr := ParseDOWAYTranscript(raw)
		current, e := s.Get(ids[0])
		if e != nil {
			return count, e
		}
		if current.State == "transcribing" || current.State == "queued" {
			continue
		}
		if parseErr == nil && (current.Provider != "doway" || current.Transcript != result.Text || !reflect.DeepEqual(current.Segments, result.Segments)) {
			if e = s.ImportCloud(ctx, current.ID, uid); e != nil {
				return count, e
			}
			count++
			continue
		}
		if current.CloudUID != uid || title != "" && current.TitleSource != "local" && current.Title != title {
			if e = s.Update(current.ID, func(r *Record) {
				r.CloudUID = uid
				if title != "" && r.TitleSource != "local" {
					r.Title = title
					r.TitleSource = "doway"
				}
			}); e != nil {
				return count, e
			}
			count++
		}
	}
	return count, nil
}
