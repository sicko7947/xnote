package xnote

// QueueDownloaded queues only the confirmed IDs that are still downloaded.
// Records added after confirmation, completed jobs and failures are untouched.
func (s *Store) QueueDownloaded(ids []string) (int, error) {
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		selected[id] = true
	}
	queued := 0
	err := s.mutate(func() error {
		rows, err := s.Records()
		if err != nil {
			return err
		}
		for _, r := range rows {
			if !selected[r.ID] || !canQueueDownloaded(r) {
				continue
			}
			r.State, r.Error = "queued", ""
			if err := s.save(r); err != nil {
				return err
			}
			queued++
		}
		return nil
	})
	return queued, err
}

func canQueueDownloaded(r Record) bool {
	return r.State == "downloaded" && !r.Trashed && r.Audio != ""
}
