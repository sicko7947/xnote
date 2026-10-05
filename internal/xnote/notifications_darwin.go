package xnote

import (
	"context"
	"fmt"
	"sync"

	"tinygo.org/x/bluetooth"
)

func subscribeCharacteristic(ctx context.Context, d *Device, uuid bluetooth.UUID, callback func([]byte), _ chan<- error) (func(), func(context.Context) error, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	var characteristic *bluetooth.DeviceCharacteristic
	switch uuid {
	case bluetooth.New16BitUUID(0xb0b4):
		characteristic = &d.audio
	case bluetooth.New16BitUUID(0xb0b2):
		characteristic = &d.notify
	default:
		return nil, nil, fmt.Errorf("unsupported notification characteristic %s", uuid)
	}
	if err := characteristic.EnableNotifications(callback); err != nil {
		return nil, nil, err
	}
	var once sync.Once
	var stopErr error
	stop := func() { once.Do(func() { stopErr = characteristic.EnableNotifications(nil) }) }
	finish := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// tinygo waits for CoreBluetooth's confirmed notification-state change
		// before returning; it retains the callback until that confirmation.
		stop()
		if stopErr != nil {
			return stopErr
		}
		return ctx.Err()
	}
	return stop, finish, nil
}

func subscribeAudio(ctx context.Context, d *Device, callback func([]byte), fail chan<- error) (func(), func(context.Context) error, error) {
	return subscribeCharacteristic(ctx, d, bluetooth.New16BitUUID(0xb0b4), callback, fail)
}

func subscribeCommands(ctx context.Context, d *Device, callback func([]byte), fail chan<- error) (func(), error) {
	stop, _, err := subscribeCharacteristic(ctx, d, bluetooth.New16BitUUID(0xb0b2), callback, fail)
	return stop, err
}
