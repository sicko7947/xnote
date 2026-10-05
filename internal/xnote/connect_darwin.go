package xnote

import (
	"context"

	"tinygo.org/x/bluetooth"
)

func preconnect(ctx context.Context, _ *bluetooth.Adapter, _ bluetooth.Address) error {
	return ctx.Err()
}
