package xnote

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"tinygo.org/x/bluetooth"
)

type bluezCall func(context.Context, dbus.ObjectPath, string, ...interface{}) ([]interface{}, error)

// preconnect bounds BlueZ's connection attempt before entering tinygo's Linux
// Connect implementation, which otherwise waits without a deadline and queues
// signals while waiting. No signal subscriptions are needed here. A disconnect
// between this successful preconnect and tinygo's Connected check can still
// enter its unbounded branch; the service watchdog remains the final fallback.
func preconnect(ctx context.Context, adapter *bluetooth.Adapter, address bluetooth.Address) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mac, err := adapter.Address()
	if err != nil {
		return err
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("Bluetooth preconnect bus: %w", err)
	}
	defer conn.Close()
	return preconnectBlueZ(ctx, mac.String(), address.String(), func(ctx context.Context, path dbus.ObjectPath, method string, args ...interface{}) ([]interface{}, error) {
		call := conn.Object("org.bluez", path).CallWithContext(ctx, method, 0, args...)
		return call.Body, call.Err
	})
}

func preconnectBlueZ(ctx context.Context, adapterMAC, deviceMAC string, call bluezCall) error {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := call(ctx, "/", "org.freedesktop.DBus.ObjectManager.GetManagedObjects")
	if err != nil {
		return fmt.Errorf("Bluetooth preconnect discovery: %w", err)
	}
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err = dbus.Store(body, &objects); err != nil {
		return fmt.Errorf("Bluetooth preconnect objects: %w", err)
	}
	var adapterPath dbus.ObjectPath
	for path, interfaces := range objects {
		if address, ok := interfaces["org.bluez.Adapter1"]["Address"].Value().(string); ok && strings.EqualFold(address, adapterMAC) {
			adapterPath = path
			break
		}
	}
	if adapterPath == "" {
		return errors.New("Bluetooth preconnect adapter not found")
	}
	var devicePath dbus.ObjectPath
	for path, interfaces := range objects {
		if address, ok := interfaces["org.bluez.Device1"]["Address"].Value().(string); ok && strings.EqualFold(address, deviceMAC) && strings.HasPrefix(string(path), string(adapterPath)+"/") {
			devicePath = path
			break
		}
	}
	if devicePath == "" {
		return errors.New("Bluetooth preconnect recorder not found on selected adapter")
	}
	body, err = call(ctx, devicePath, "org.freedesktop.DBus.Properties.Get", "org.bluez.Device1", "Connected")
	if err != nil {
		return fmt.Errorf("Bluetooth preconnect state: %w", err)
	}
	var connected dbus.Variant
	if err = dbus.Store(body, &connected); err != nil {
		return fmt.Errorf("Bluetooth preconnect state: %w", err)
	}
	active, ok := connected.Value().(bool)
	if !ok {
		return errors.New("Bluetooth preconnect returned invalid connection state")
	}
	if active {
		return nil // Do not disturb an existing connection, even on cancellation.
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	_, err = call(ctx, devicePath, "org.bluez.Device1.Connect")
	if err == nil {
		return nil
	}
	var busErr dbus.Error
	if errors.As(err, &busErr) {
		if busErr.Name == "org.bluez.Error.AlreadyConnected" {
			return nil
		}
		if busErr.Name == "org.bluez.Error.InProgress" {
			return fmt.Errorf("Bluetooth preconnect already in progress: %w", err)
		}
	}
	// Cancel the attempt we started, using a fresh deadline after ctx expired.
	cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	_, cleanupErr := call(cleanup, devicePath, "org.bluez.Device1.Disconnect")
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if cleanupErr != nil {
		return fmt.Errorf("Bluetooth preconnect failed: %w (disconnect cleanup: %v)", err, cleanupErr)
	}
	return fmt.Errorf("Bluetooth preconnect failed: %w", err)
}
