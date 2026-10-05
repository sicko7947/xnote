package xnote

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxPlayerProtocolIgnoresEventsAndStaleReplies(t *testing.T) {
	PlayerClose()
	client, server := net.Pipe()
	linuxPlayer.conn = client
	linuxPlayer.decoder = json.NewDecoder(client)
	t.Cleanup(PlayerClose)
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		var request struct {
			Command []any  `json:"command"`
			ID      uint64 `json:"request_id"`
		}
		if err := json.NewDecoder(server).Decode(&request); err != nil {
			done <- err
			return
		}
		encoder := json.NewEncoder(server)
		for _, response := range []any{
			map[string]any{"event": "property-change"},
			map[string]any{"request_id": request.ID + 100, "error": "success", "data": 100},
			map[string]any{"request_id": request.ID, "error": "success", "data": 12.5},
		} {
			if err := encoder.Encode(response); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	var position float64
	if err := linuxPlayerCommand([]any{"get_property", "time-pos"}, &position); err != nil {
		t.Fatal(err)
	}
	if position != 12.5 {
		t.Fatalf("position = %v", position)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestLinuxPlayerLifecycle(t *testing.T) {
	mpv, err := exec.LookPath("mpv")
	if err != nil {
		t.Skip("mpv not installed")
	}
	PlayerClose()
	t.Cleanup(PlayerClose)
	bin := t.TempDir()
	// Force null audio to exercise a real player without opening audio hardware.
	wrapper := "#!/bin/sh\nexec '" + strings.ReplaceAll(mpv, "'", "'\\''") + "' --ao=null \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "mpv"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	path := filepath.Join(t.TempDir(), "--audio 'quoted' recording.wav")
	// Two seconds of silent 16-bit mono PCM.
	wav := make([]byte, 44+32000)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 8000)
	binary.LittleEndian.PutUint32(wav[28:], 16000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], 32000)
	if err := os.WriteFile(path, wav, 0600); err != nil {
		t.Fatal(err)
	}
	if err := PlayerOpen(path); err != nil {
		t.Fatal(err)
	}
	_, duration, playing := PlayerPosition()
	if duration != 2 || !playing {
		t.Fatalf("duration=%v playing=%v", duration, playing)
	}
	PlayerToggle()
	if _, _, playing := PlayerPosition(); playing {
		t.Fatal("toggle did not pause")
	}
	PlayerSeek(1)
	PlayerRate(1.5)
	var rate float64
	if err := linuxPlayerCommand([]any{"get_property", "speed"}, &rate); err != nil || rate != 1.5 {
		t.Fatalf("rate=%v error=%v", rate, err)
	}
	dir, done := linuxPlayer.dir, linuxPlayer.done
	PlayerClose()
	select {
	case <-done:
	default:
		t.Fatal("mpv was not reaped")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("socket directory still exists: %v", err)
	}
	PlayerClose() // Idempotent, including before any process was started.
	if err := PlayerOpen(filepath.Join(bin, "missing.wav")); err == nil {
		t.Fatal("missing audio accepted")
	}
}

func TestLinuxPlayerIPCFailureClosesConnection(t *testing.T) {
	PlayerClose()
	client, server := net.Pipe()
	linuxPlayer.conn = client
	linuxPlayer.decoder = json.NewDecoder(client)
	t.Cleanup(PlayerClose)
	_ = server.Close()
	if err := linuxPlayerCommand([]any{"get_property", "time-pos"}, nil); err == nil {
		t.Fatal("closed IPC connection succeeded")
	}
	if linuxPlayer.conn != nil {
		t.Fatal("broken connection retained for future redraws")
	}
}
