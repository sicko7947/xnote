package xnote

import "testing"

func TestTranscriptionConcurrencyDefaultsAndBounds(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := s.Config()
	if c.TranscriptionConcurrency != 4 || c.TranscriptionPaused {
		t.Fatalf("unexpected default queue configuration: %+v", c)
	}
	for _, tc := range []struct {
		stored, effective int
		valid             bool
	}{
		{0, 4, true}, {1, 1, true}, {4, 4, true}, {16, 16, true}, {-1, 1, false}, {17, 16, false},
	} {
		c.TranscriptionConcurrency = tc.stored
		if got := EffectiveTranscriptionConcurrency(c); got != tc.effective {
			t.Fatalf("stored %d: effective %d, want %d", tc.stored, got, tc.effective)
		}
		if err := s.SaveConfig(c); (err == nil) != tc.valid {
			t.Fatalf("stored %d: SaveConfig error %v", tc.stored, err)
		}
	}
}
