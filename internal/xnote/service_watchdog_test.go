package xnote

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestServiceWatchdogConfig(t *testing.T) {
	for _, tt := range []struct {
		name, socket, usec, pid string
		want                    bool
	}{
		{"unset", "", "", "", false},
		{"filesystem", "/run/notify", "90000000", "", true},
		{"abstract", "@notify", "90000000", "42", true},
		{"wrong process", "/run/notify", "90000000", "99", false},
		{"bad process", "/run/notify", "90000000", "oops", false},
		{"relative socket", "notify", "90000000", "", false},
		{"zero", "/run/notify", "0", "", false},
		{"negative", "/run/notify", "-1", "", false},
		{"overflow", "/run/notify", "9223372036854775807", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{"NOTIFY_SOCKET": tt.socket, "WATCHDOG_USEC": tt.usec, "WATCHDOG_PID": tt.pid}
			_, interval, ok := serviceWatchdogConfig(func(k string) string { return env[k] }, 42)
			if ok != tt.want {
				t.Fatalf("enabled = %v; want %v", ok, tt.want)
			}
			if ok && interval != 15*time.Second {
				t.Fatalf("interval = %s", interval)
			}
		})
	}
}

func TestServiceWatchdogFresh(t *testing.T) {
	now := time.Now()
	for _, tt := range []struct {
		name  string
		stamp string
		want  bool
	}{
		{"missing", "", false}, {"malformed", "broken", false},
		{"recent", now.Add(-30 * time.Second).Format(time.RFC3339Nano), true},
		{"expired", now.Add(-46 * time.Second).Format(time.RFC3339Nano), false},
		{"future", now.Add(time.Second).Format(time.RFC3339Nano), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := serviceWatchdogFresh(Status{UpdatedAt: tt.stamp}, now); got != tt.want {
				t.Fatalf("fresh = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestServiceWatchdogStopsNotifyingWithoutProgress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	var elapsed atomic.Int64
	var calls atomic.Int64
	first := make(chan struct{})
	expired := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		serviceWatchdogLoop(ctx, time.Millisecond, func() Status {
			if elapsed.Load() > 0 {
				select {
				case expired <- struct{}{}:
				default:
				}
			}
			return Status{UpdatedAt: start.Format(time.RFC3339Nano)}
		}, func() time.Time { return start.Add(time.Duration(elapsed.Load())) }, func(context.Context) error {
			if calls.Add(1) == 1 {
				elapsed.Store(int64(46 * time.Second))
				close(first)
			}
			return nil
		})
	}()
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("no initial heartbeat")
	}
	select {
	case <-expired:
	case <-time.After(time.Second):
		t.Fatal("expired status not checked")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop watchdog")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("stale engine still notified: %d calls", got)
	}
}

func TestNotifyServiceWatchdogUnixgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets required")
	}
	sockets := []string{filepath.Join(t.TempDir(), "notify")}
	if runtime.GOOS == "linux" {
		sockets = append(sockets, fmt.Sprintf("@xnote-watchdog-test-%d", time.Now().UnixNano()))
	}
	for _, socket := range sockets {
		t.Run(socket, func(t *testing.T) {
			listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socket, Net: "unixgram"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := notifyServiceWatchdog(context.Background(), socket); err != nil {
				t.Fatal(err)
			}
			if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 64)
			n, _, err := listener.ReadFromUnix(buf)
			if err != nil {
				t.Fatal(err)
			}
			if string(buf[:n]) != "WATCHDOG=1" {
				t.Fatalf("unexpected notification %q", buf[:n])
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := notifyServiceWatchdog(ctx, socket); err == nil {
				t.Fatal("canceled notification succeeded")
			}
		})
	}
}
