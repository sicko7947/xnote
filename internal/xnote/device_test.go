package xnote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOverrunDiscardsStoredAndBufferedPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio.mp3.partial")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.Write([]byte("previously received prefix")); err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriter(f)
	if _, err = writer.Write([]byte("new untrusted bytes")); err != nil {
		t.Fatal(err)
	}
	if err = discardDownloadPrefix(f, writer); err != nil {
		t.Fatal(err)
	}
	// Download's deferred Flush must not restore the discarded bytes.
	if err = writer.Flush(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) != 0 {
		t.Fatalf("unsafe bytes remain available for resume: len=%d, err=%v", len(data), err)
	}
}

func deviceReply(op byte, payload []byte) []byte {
	p := append([]byte{160, 10, 1, op, byte(len(payload))}, payload...)
	return binary.LittleEndian.AppendUint16(p, crc16(p))
}

func TestReceiveFragmentedRepliesAndRejectBadCRC(t *testing.T) {
	d := &Device{replies: make(chan reply, 4), fail: make(chan error, 1)}
	bad := deviceReply(10, []byte{9})
	bad[len(bad)-1] ^= 1
	stream := append([]byte{0, 1, 2}, bad...)
	stream = append(stream, deviceReply(10, []byte{1, 2, 3})...)
	stream = append(stream, deviceReply(11, []byte{2})...)
	for _, b := range stream {
		d.receive([]byte{b})
	}
	if len(d.replies) != 2 {
		t.Fatalf("received %d replies, want two valid frames", len(d.replies))
	}
	first, second := <-d.replies, <-d.replies
	if first.op != 10 || !bytes.Equal(first.data, []byte{1, 2, 3}) || second.op != 11 || !bytes.Equal(second.data, []byte{2}) {
		t.Fatalf("unexpected replies: %+v %+v", first, second)
	}
}

func TestReplyOverflowSignalsFailure(t *testing.T) {
	d := &Device{replies: make(chan reply, 1), fail: make(chan error, 1)}
	d.receive(append(deviceReply(10, []byte{0}), deviceReply(10, []byte{2})...))
	select {
	case err := <-d.fail:
		if err == nil {
			t.Fatal("overflow did not report an error")
		}
	default:
		t.Fatal("reply loss must fail the session")
	}
}

func TestAudioOverflowPreservesContiguousPrefix(t *testing.T) {
	q := &audioQueue{packets: make(chan []byte, 1), fail: make(chan error, 1)}
	first := []byte{1, 2}
	q.receive(first)
	first[0] = 9            // Bluetooth may reuse the callback buffer.
	q.receive([]byte{3, 4}) // Lost packet: no later bytes may be accepted.
	if got := <-q.packets; !bytes.Equal(got, []byte{1, 2}) {
		t.Fatalf("prefix was not copied: %v", got)
	}
	q.receive([]byte{5, 6}) // Queue now has room, but the prefix has ended.
	if len(q.packets) != 0 {
		t.Fatal("accepted bytes after a hole; resume would corrupt audio")
	}
	if len(q.fail) != 1 {
		t.Fatal("overflow must signal a reconnect")
	}
}

func TestConnectCanceledBeforeScanning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Connect(ctx, nil, Config{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation before adapter access", err)
	}
}

func TestDownloadReplyAcceptsOnlyExactDuplicateAck(t *testing.T) {
	ack := []byte{0, 0, 0, 4, 0xa6} // Observed 1190-byte recording acknowledgement.
	for _, tt := range []struct {
		name      string
		data      []byte
		complete  bool
		wantError bool
	}{
		{"duplicate accepted acknowledgement", ack, false, false},
		{"completion", []byte{2}, true, false},
		{"different file size", []byte{0, 0, 0, 4, 0xa7}, false, true},
		{"rejection", []byte{1}, false, true},
		{"truncated acknowledgement", []byte{0}, false, true},
		{"oversized acknowledgement", []byte{0, 0, 0, 4, 0xa6, 0}, false, true},
		{"malformed completion", []byte{2, 0}, false, true},
		{"empty", nil, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			complete, err := downloadReplyComplete(tt.data, ack)
			if complete != tt.complete || (err != nil) != tt.wantError {
				t.Fatalf("got complete=%v err=%v", complete, err)
			}
		})
	}
}
