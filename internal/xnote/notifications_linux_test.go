package xnote

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

func TestAudioSocketPreservesOrderBoundariesAndEqualPackets(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[1])
	packets := make(chan []byte, 20)
	fail := make(chan error, 1)
	stop, _, err := consumeAudioSocket(fds[0], 247, func(p []byte) { packets <- bytes.Clone(p) }, fail)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	want := [][]byte{bytes.Repeat([]byte{1}, 244), bytes.Repeat([]byte{2}, 244), bytes.Repeat([]byte{3}, 24), bytes.Repeat([]byte{3}, 24), []byte{4, 5, 6, 7, 8, 9}}
	for _, p := range want {
		if _, err := unix.Write(fds[1], p); err != nil {
			t.Fatal(err)
		}
	}
	for _, expected := range want {
		select {
		case got := <-packets:
			if !bytes.Equal(got, expected) {
				t.Fatalf("packet changed or reordered: got len %d, want len %d", len(got), len(expected))
			}
		case err := <-fail:
			t.Fatal(err)
		case <-time.After(time.Second):
			t.Fatal("packet lost")
		}
	}
	closed := make(chan struct{})
	go func() { stop(); stop(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not unblock the idle reader")
	}
	select {
	case err := <-fail:
		t.Fatalf("normal cleanup reported failure: %v", err)
	default:
	}
}

func TestAudioSocketDisconnectAndOversizeFail(t *testing.T) {
	for _, oversize := range []bool{false, true} {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		fail := make(chan error, 1)
		stop, _, err := consumeAudioSocket(fds[0], 247, func([]byte) { t.Error("invalid packet was delivered") }, fail)
		if err != nil {
			t.Fatal(err)
		}
		if oversize {
			_, err = unix.Write(fds[1], make([]byte, 245))
			if err != nil {
				t.Fatal(err)
			}
		} else {
			unix.Close(fds[1])
		}
		select {
		case <-fail:
		case <-time.After(time.Second):
			t.Fatal("invalid stream did not report failure")
		}
		stop()
		if oversize {
			unix.Close(fds[1])
		}
	}
}

func TestAudioCharacteristicSelectsRecorderAndAdapter(t *testing.T) {
	objects := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
		"/org/bluez/hci7":                                        {"org.bluez.Adapter1": {"Address": dbus.MakeVariant("11:22:33:44:55:66")}},
		testRecorderPath:                                         {"org.bluez.Device1": {"Address": dbus.MakeVariant("50:C0:F0:13:A2:AA")}},
		testRecorderPath + "/service01/char04":                   {"org.bluez.GattCharacteristic1": {"UUID": dbus.MakeVariant("0000b0b4-0000-1000-8000-00805f9b34fb")}},
		"/org/bluez/hci0/dev_50_C0_F0_13_A2_AA/service01/char04": {"org.bluez.GattCharacteristic1": {"UUID": dbus.MakeVariant("0000b0b4-0000-1000-8000-00805f9b34fb")}},
	}
	path, err := notificationCharacteristicPath(objects, "11:22:33:44:55:66", "50:c0:f0:13:a2:aa", "0000b0b4-0000-1000-8000-00805f9b34fb")
	if err != nil || path != testRecorderPath+"/service01/char04" {
		t.Fatalf("wrong audio characteristic: %s, %v", path, err)
	}
	commandPath := testRecorderPath + "/service01/char02"
	objects[commandPath] = map[string]map[string]dbus.Variant{"org.bluez.GattCharacteristic1": {"UUID": dbus.MakeVariant("0000b0b2-0000-1000-8000-00805f9b34fb")}}
	path, err = notificationCharacteristicPath(objects, "11:22:33:44:55:66", "50:c0:f0:13:a2:aa", "0000b0b2-0000-1000-8000-00805f9b34fb")
	if err != nil || path != commandPath {
		t.Fatalf("wrong command characteristic: %s, %v", path, err)
	}
}

func TestAudioFinishDrainsQueuedLateDuplicate(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	peerClosed := false
	defer func() {
		if !peerClosed {
			unix.Close(fds[1])
		}
	}()
	packets := make(chan []byte, 4)
	entered := make(chan struct{})
	release := make(chan struct{})
	first := true
	fail := make(chan error, 1)
	stop, drain, err := consumeAudioSocket(fds[0], 247, func(p []byte) {
		if first {
			first = false
			close(entered)
			<-release // Delay callback until after completion/finish began.
		}
		packets <- bytes.Clone(p)
	}, fail)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	payload := bytes.Repeat([]byte{9}, 166)
	for i := 0; i < 2; i++ {
		if _, err := unix.Write(fds[1], payload); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("reader did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = drain(ctx, func(context.Context) error {
		err := unix.Close(fds[1])
		peerClosed = true
		close(release)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) != 2 {
		t.Fatalf("drain lost queued duplicate: got %d packets", len(packets))
	}
	for len(packets) > 0 {
		if !bytes.Equal(<-packets, payload) {
			t.Fatal("changed queued packet")
		}
	}
	select {
	case err := <-fail:
		t.Fatalf("expected EOF reported failure: %v", err)
	default:
	}
}

func TestAudioFinishFailsWithoutProducerEOF(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[1])
	stop, drain, err := consumeAudioSocket(fds[0], 247, func([]byte) {}, make(chan error, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := drain(ctx, func(context.Context) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("missing producer EOF must fail: %v", err)
	}
	if _, err := unix.FcntlInt(uintptr(fds[0]), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
		t.Fatalf("reader fd leaked: %v", err)
	}
}

func TestAudioFinishPreservesEarlierDisconnectAndStopFailure(t *testing.T) {
	for _, earlierEOF := range []bool{false, true} {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		fail := make(chan error, 1)
		stop, drain, err := consumeAudioSocket(fds[0], 247, func([]byte) {}, fail)
		if err != nil {
			t.Fatal(err)
		}
		if earlierEOF {
			unix.Close(fds[1])
			select {
			case <-fail:
			case <-time.After(time.Second):
				t.Fatal("no earlier EOF failure")
			}
		}
		stopErr := errors.New("StopNotify rejected")
		err = drain(context.Background(), func(context.Context) error {
			if earlierEOF {
				t.Error("stop called after stream already failed")
			}
			return stopErr
		})
		if err == nil || (!earlierEOF && !errors.Is(err, stopErr)) {
			t.Fatalf("lost finish failure: %v", err)
		}
		stop()
		if !earlierEOF {
			unix.Close(fds[1])
		}
	}
}
