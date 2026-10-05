package xnote

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

const testRecorderPath dbus.ObjectPath = "/org/bluez/hci7/dev_50_C0_F0_13_A2_AA"

func connectionFixture(active bool, action bluezCall) bluezCall {
	return func(ctx context.Context, path dbus.ObjectPath, method string, args ...interface{}) ([]interface{}, error) {
		switch method {
		case "org.freedesktop.DBus.ObjectManager.GetManagedObjects":
			return []interface{}{map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
				"/org/bluez/hci7":                       {"org.bluez.Adapter1": {"Address": dbus.MakeVariant("11:22:33:44:55:66")}},
				testRecorderPath:                        {"org.bluez.Device1": {"Address": dbus.MakeVariant("50:C0:F0:13:A2:AA")}},
				"/org/bluez/hci0/dev_50_C0_F0_13_A2_AA": {"org.bluez.Device1": {"Address": dbus.MakeVariant("50:C0:F0:13:A2:AA")}},
			}}, nil
		case "org.freedesktop.DBus.Properties.Get":
			return []interface{}{dbus.MakeVariant(active)}, nil
		default:
			return action(ctx, path, method, args...)
		}
	}
}

func TestPreconnectCancellationCleansUpOwnAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls []string
	call := connectionFixture(false, func(ctx context.Context, path dbus.ObjectPath, method string, _ ...interface{}) ([]interface{}, error) {
		if path != testRecorderPath {
			t.Fatalf("selected wrong adapter: %s", path)
		}
		calls = append(calls, method)
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("DBus method lacks deadline")
		}
		switch method {
		case "org.bluez.Device1.Connect":
			if time.Until(deadline) > 12*time.Second {
				t.Fatal("connect deadline exceeds twelve seconds")
			}
			cancel()
			return nil, ctx.Err()
		case "org.bluez.Device1.Disconnect":
			if ctx.Err() != nil || time.Until(deadline) > 2*time.Second {
				t.Fatal("cleanup must have a fresh bounded context")
			}
			return nil, context.DeadlineExceeded
		default:
			t.Fatalf("unexpected subscription or call: %s", method)
			return nil, nil
		}
	})
	err := preconnectBlueZ(ctx, "11:22:33:44:55:66", "50:c0:f0:13:a2:aa", call)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "disconnect cleanup") {
		t.Fatalf("lost cancellation or cleanup failure: %v", err)
	}
	if len(calls) != 2 || calls[1] != "org.bluez.Device1.Disconnect" {
		t.Fatalf("unexpected calls: %v", calls)
	}
}

func TestPreconnectLeavesExistingConnectionAlone(t *testing.T) {
	call := connectionFixture(true, func(context.Context, dbus.ObjectPath, string, ...interface{}) ([]interface{}, error) {
		t.Fatal("existing link must not be connected or disconnected")
		return nil, nil
	})
	if err := preconnectBlueZ(context.Background(), "11:22:33:44:55:66", "50:C0:F0:13:A2:AA", call); err != nil {
		t.Fatal(err)
	}
}

func TestPreconnectCanceledBeforeStartDoesNotTouchBus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := preconnectBlueZ(ctx, "", "", func(context.Context, dbus.ObjectPath, string, ...interface{}) ([]interface{}, error) {
		t.Fatal("canceled request accessed the bus")
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPreconnectDoesNotCancelAnotherPendingConnection(t *testing.T) {
	count := 0
	call := connectionFixture(false, func(_ context.Context, _ dbus.ObjectPath, method string, _ ...interface{}) ([]interface{}, error) {
		count++
		if method != "org.bluez.Device1.Connect" {
			t.Fatal("must not disconnect an already-pending attempt")
		}
		return nil, dbus.Error{Name: "org.bluez.Error.InProgress"}
	})
	if err := preconnectBlueZ(context.Background(), "11:22:33:44:55:66", "50:C0:F0:13:A2:AA", call); err == nil || count != 1 {
		t.Fatalf("got error %v, calls %d", err, count)
	}
}

func TestPreconnectSuccessHonorsShorterParentDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	parentDeadline, _ := ctx.Deadline()
	calls := 0
	call := connectionFixture(false, func(ctx context.Context, _ dbus.ObjectPath, method string, _ ...interface{}) ([]interface{}, error) {
		calls++
		if method != "org.bluez.Device1.Connect" {
			t.Fatalf("successful connection triggered %s", method)
		}
		deadline, ok := ctx.Deadline()
		if !ok || deadline.After(parentDeadline) {
			t.Fatal("connect extended the caller deadline")
		}
		return nil, nil
	})
	if err := preconnectBlueZ(ctx, "11:22:33:44:55:66", "50:C0:F0:13:A2:AA", call); err != nil || calls != 1 {
		t.Fatalf("got error %v, calls %d", err, calls)
	}
}
