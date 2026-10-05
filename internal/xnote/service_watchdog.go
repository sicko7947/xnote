package xnote

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// runServiceWatchdog is enabled only by systemd's watchdog environment. It must
// run under the engine owner's lifetime, never under a TUI or secondary client.
// A timer alone is not evidence of health: only engine status progress earns a
// notification, so a blocked Bluetooth operation eventually restarts the process.
func runServiceWatchdog(ctx context.Context, s *Store) {
	socket, interval, ok := serviceWatchdogConfig(os.Getenv, os.Getpid())
	if !ok {
		return
	}
	serviceWatchdogLoop(ctx, interval, s.Status, time.Now, func(ctx context.Context) error {
		return notifyServiceWatchdog(ctx, socket)
	})
}

func serviceWatchdogConfig(getenv func(string) string, pid int) (string, time.Duration, bool) {
	socket := getenv("NOTIFY_SOCKET")
	if len(socket) < 2 || (!strings.HasPrefix(socket, "/") && !strings.HasPrefix(socket, "@")) {
		return "", 0, false
	}
	if target := getenv("WATCHDOG_PID"); target != "" {
		parsed, err := strconv.Atoi(target)
		if err != nil || parsed != pid {
			return "", 0, false
		}
	}
	micros, err := strconv.ParseInt(getenv("WATCHDOG_USEC"), 10, 64)
	if err != nil || micros <= 0 || micros > int64((1<<63-1)/time.Microsecond) {
		return "", 0, false
	}
	interval := time.Duration(micros) * time.Microsecond / 3
	if interval > 15*time.Second {
		interval = 15 * time.Second
	}
	return socket, interval, true
}

func serviceWatchdogFresh(st Status, now time.Time) bool {
	updated, err := time.Parse(time.RFC3339Nano, st.UpdatedAt)
	if err != nil {
		return false
	}
	age := now.Sub(updated)
	return age >= 0 && age <= 45*time.Second
}

func serviceWatchdogLoop(ctx context.Context, interval time.Duration, status func() Status, now func() time.Time, notify func(context.Context) error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		st := status()
		if serviceWatchdogFresh(st, now()) {
			if ctx.Err() != nil {
				return
			}
			_ = notify(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func notifyServiceWatchdog(ctx context.Context, socket string) error {
	// Go's Unix networking accepts @name for Linux abstract namespace sockets.
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(ctx, "unixgram", socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}
	_, err = conn.Write([]byte("WATCHDOG=1"))
	return err
}
