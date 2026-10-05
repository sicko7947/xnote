package xnote

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// The private socket directory prevents other local users controlling playback.
// All process and IPC access is serialized, including calls from the redraw loop.
var linuxPlayer struct {
	sync.Mutex
	process  *exec.Cmd
	done     chan struct{}
	dir      string
	conn     net.Conn
	decoder  *json.Decoder
	sequence uint64
}

func PlayerOpen(path string) error {
	linuxPlayer.Lock()
	defer linuxPlayer.Unlock()
	closeLinuxPlayer()
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("audio path must be a regular file")
	}
	binary, err := exec.LookPath("mpv")
	if err != nil {
		return errors.New("audio playback requires mpv (install it with your Linux package manager)")
	}
	dir, err := os.MkdirTemp("", "xnote-player-")
	if err != nil {
		return err
	}
	linuxPlayer.dir = dir
	socket := filepath.Join(dir, "ipc")
	cmd := exec.Command(binary, "--no-config", "--no-terminal", "--no-video", "--audio-display=no", "--keep-open=yes", "--input-ipc-server="+socket, "--", absolute)
	if err := cmd.Start(); err != nil {
		closeLinuxPlayer()
		return fmt.Errorf("start mpv: %w", err)
	}
	linuxPlayer.process = cmd
	linuxPlayer.done = make(chan struct{})
	done := linuxPlayer.done
	go func() { _ = cmd.Wait(); close(done) }()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			closeLinuxPlayer()
			return errors.New("mpv exited before opening audio")
		default:
		}
		if linuxPlayer.conn == nil {
			conn, dialErr := net.DialTimeout("unix", socket, 100*time.Millisecond)
			if dialErr == nil {
				linuxPlayer.conn = conn
				linuxPlayer.decoder = json.NewDecoder(conn)
			}
		}
		if linuxPlayer.conn != nil {
			var duration float64
			if err := linuxPlayerCommand([]any{"get_property", "duration"}, &duration); err == nil && duration > 0 {
				return nil
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	closeLinuxPlayer()
	return errors.New("mpv could not load audio within 4 seconds")
}

func closeLinuxPlayer() {
	if linuxPlayer.conn != nil {
		_ = linuxPlayer.conn.Close()
	}
	if linuxPlayer.process != nil {
		// Kill is bounded and Wait always runs in the reaper goroutine.
		_ = linuxPlayer.process.Process.Kill()
		<-linuxPlayer.done
	}
	if linuxPlayer.dir != "" {
		_ = os.RemoveAll(linuxPlayer.dir)
	}
	linuxPlayer.process, linuxPlayer.done = nil, nil
	linuxPlayer.conn, linuxPlayer.decoder, linuxPlayer.dir = nil, nil, ""
}

func PlayerClose() { linuxPlayer.Lock(); defer linuxPlayer.Unlock(); closeLinuxPlayer() }

// mpv sends asynchronous events interleaved with replies. Request IDs prevent a
// delayed response from being mistaken for the next command's result.
func linuxPlayerCommand(command []any, result any) error {
	if linuxPlayer.conn == nil {
		return errors.New("player is closed")
	}
	linuxPlayer.sequence++
	id := linuxPlayer.sequence
	if err := linuxPlayer.conn.SetDeadline(time.Now().Add(350 * time.Millisecond)); err != nil {
		closeLinuxPlayer()
		return err
	}
	if err := json.NewEncoder(linuxPlayer.conn).Encode(struct {
		Command []any  `json:"command"`
		ID      uint64 `json:"request_id"`
	}{command, id}); err != nil {
		closeLinuxPlayer()
		return err
	}
	for {
		var response struct {
			ID    uint64          `json:"request_id"`
			Error string          `json:"error"`
			Data  json.RawMessage `json:"data"`
		}
		if err := linuxPlayer.decoder.Decode(&response); err != nil {
			closeLinuxPlayer()
			return err
		}
		if response.ID != id {
			continue
		}
		if response.Error != "success" {
			return fmt.Errorf("mpv: %s", response.Error)
		}
		if result != nil {
			return json.Unmarshal(response.Data, result)
		}
		return nil
	}
}

func PlayerToggle() {
	linuxPlayer.Lock()
	defer linuxPlayer.Unlock()
	var eof bool
	_ = linuxPlayerCommand([]any{"get_property", "eof-reached"}, &eof)
	if eof {
		_ = linuxPlayerCommand([]any{"seek", 0, "absolute+exact"}, nil)
		_ = linuxPlayerCommand([]any{"set_property", "pause", false}, nil)
	} else {
		_ = linuxPlayerCommand([]any{"cycle", "pause"}, nil)
	}
}
func PlayerSeek(seconds float64) {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return
	}
	linuxPlayer.Lock()
	defer linuxPlayer.Unlock()
	_ = linuxPlayerCommand([]any{"seek", math.Max(0, seconds), "absolute+exact"}, nil)
}
func PlayerRate(rate float64) {
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 {
		return
	}
	linuxPlayer.Lock()
	defer linuxPlayer.Unlock()
	_ = linuxPlayerCommand([]any{"set_property", "speed", rate}, nil)
}
func PlayerPosition() (position, duration float64, playing bool) {
	linuxPlayer.Lock()
	defer linuxPlayer.Unlock()
	if linuxPlayer.conn == nil {
		return
	}
	var paused, eof bool
	for _, property := range []struct {
		name  string
		value any
	}{
		{"time-pos", &position}, {"duration", &duration}, {"pause", &paused}, {"eof-reached", &eof},
	} {
		if err := linuxPlayerCommand([]any{"get_property", property.name}, property.value); err != nil {
			return 0, 0, false
		}
	}
	return position, duration, !paused && !eof
}
