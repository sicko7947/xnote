package xnote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
	"tinygo.org/x/bluetooth"
)

// AcquireNotify delivers ordered ATT values directly, avoiding BlueZ's cached
// Value property and godbus's concurrent signal delivery. Closing the acquired
// socket releases notification ownership. StartNotify must not be mixed with
// this subscription. StopNotify closes BlueZ's producer socket and is used as
// an explicit drain barrier before publishing a completed download.
func subscribeCharacteristic(ctx context.Context, d *Device, uuid bluetooth.UUID, callback func([]byte), fail chan<- error) (func(), func(context.Context) error, error) {
	setup, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := setup.Err(); err != nil {
		return nil, nil, err
	}
	adapterMAC, err := d.adapter.Address()
	if err != nil {
		return nil, nil, err
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, nil, err
	}
	success := false
	defer func() {
		if !success {
			conn.Close()
		}
	}()
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err = conn.Object("org.bluez", "/").CallWithContext(setup, "org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objects); err != nil {
		return nil, nil, err
	}
	path, err := notificationCharacteristicPath(objects, adapterMAC.String(), d.device.Address.String(), uuid.String())
	if err != nil {
		return nil, nil, err
	}
	var fd dbus.UnixFD
	var mtu uint16
	if err = conn.Object("org.bluez", path).CallWithContext(setup, "org.bluez.GattCharacteristic1.AcquireNotify", 0, map[string]dbus.Variant{}).Store(&fd, &mtu); err != nil {
		return nil, nil, fmt.Errorf("acquire ordered Bluetooth notifications for %s: %w", uuid, err)
	}
	stop, drain, err := consumeAudioSocket(int(fd), int(mtu), callback, fail)
	if err != nil {
		return nil, nil, err
	}
	success = true
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			stop()
			conn.Close()
		})
	}
	finish := func(ctx context.Context) error {
		defer cleanup()
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return drain(ctx, func(ctx context.Context) error {
			return conn.Object("org.bluez", path).CallWithContext(ctx, "org.bluez.GattCharacteristic1.StopNotify", 0).Err
		})
	}
	return cleanup, finish, nil
}

func subscribeAudio(ctx context.Context, d *Device, callback func([]byte), fail chan<- error) (func(), func(context.Context) error, error) {
	return subscribeCharacteristic(ctx, d, bluetooth.New16BitUUID(0xb0b4), callback, fail)
}

func subscribeCommands(ctx context.Context, d *Device, callback func([]byte), fail chan<- error) (func(), error) {
	stop, _, err := subscribeCharacteristic(ctx, d, bluetooth.New16BitUUID(0xb0b2), callback, fail)
	return stop, err
}

func notificationCharacteristicPath(objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant, adapterMAC, deviceMAC, wantedUUID string) (dbus.ObjectPath, error) {
	var adapterPath, devicePath dbus.ObjectPath
	for path, interfaces := range objects {
		address, _ := interfaces["org.bluez.Adapter1"]["Address"].Value().(string)
		if strings.EqualFold(address, adapterMAC) {
			adapterPath = path
			break
		}
	}
	if adapterPath == "" {
		return "", errors.New("notification adapter not found")
	}
	for path, interfaces := range objects {
		address, _ := interfaces["org.bluez.Device1"]["Address"].Value().(string)
		if strings.EqualFold(address, deviceMAC) && strings.HasPrefix(string(path), string(adapterPath)+"/") {
			devicePath = path
			break
		}
	}
	if devicePath == "" {
		return "", errors.New("notification recorder not found on selected adapter")
	}
	var selected dbus.ObjectPath
	for path, interfaces := range objects {
		props := interfaces["org.bluez.GattCharacteristic1"]
		uuid, _ := props["UUID"].Value().(string)
		if strings.EqualFold(uuid, wantedUUID) && strings.HasPrefix(string(path), string(devicePath)+"/") {
			if selected != "" {
				return "", fmt.Errorf("multiple recorder notification characteristics for %s", wantedUUID)
			}
			selected = path
		}
	}
	if selected == "" {
		return "", fmt.Errorf("recorder notification characteristic %s not found", wantedUUID)
	}
	return selected, nil
}

// consumeAudioSocket takes ownership of fd even on error. One reader invokes
// callbacks sequentially, preserving every packet including equal neighbours.
func consumeAudioSocket(fd, mtu int, callback func([]byte), fail chan<- error) (func(), func(context.Context, func(context.Context) error) error, error) {
	kind, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || kind != unix.SOCK_SEQPACKET || mtu < 3 || mtu > 517 {
		unix.Close(fd)
		return nil, nil, fmt.Errorf("invalid acquired notification socket: type=%d mtu=%d error=%v", kind, mtu, err)
	}
	if err = unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), "xnote-notify")
	if file == nil {
		unix.Close(fd)
		return nil, nil, errors.New("could not open acquired notification socket")
	}
	done := make(chan struct{})
	stopping := make(chan struct{})
	var stateMu sync.Mutex
	finishing := false
	var terminalErr error // Written by reader; observed only after done closes.
	go func() {
		defer close(done)
		// Larger than any ATT packet: an invalid oversized datagram is rejected
		// rather than truncated into something that could look valid.
		buf := make([]byte, 65536)
		for {
			n, err := file.Read(buf)
			if err == nil && (n == 0 || n > mtu-3) {
				if n == 0 {
					err = io.EOF
				} else {
					err = fmt.Errorf("notification exceeds negotiated MTU: %d", n)
				}
			}
			if err != nil {
				stateMu.Lock()
				terminalErr = err
				expectedEOF := errors.Is(err, io.EOF) && finishing
				stateMu.Unlock()
				select {
				case <-stopping:
					return
				default:
				}
				if expectedEOF {
					return
				}
				select {
				case fail <- fmt.Errorf("Bluetooth notification stream: %w", err):
				default:
				}
				return
			}
			callback(buf[:n])
		}
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			close(stopping)
			file.Close()
			<-done
		})
	}
	drain := func(ctx context.Context, stopProducer func(context.Context) error) error {
		defer stop()
		// An earlier EOF is an unexpected disconnect, not a successful barrier.
		stateMu.Lock()
		priorErr := terminalErr
		finishing = true
		stateMu.Unlock()
		if priorErr != nil {
			return fmt.Errorf("notification stream ended before completion barrier: %w", priorErr)
		}
		if err := stopProducer(ctx); err != nil {
			return fmt.Errorf("stop notification producer: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("drain notification stream: %w", ctx.Err())
		case <-done:
			if !errors.Is(terminalErr, io.EOF) {
				return fmt.Errorf("drain notification stream: %w", terminalErr)
			}
			return nil
		}
	}
	return stop, drain, nil
}
