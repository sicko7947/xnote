package xnote

import (
	"context"
	"errors"
	"time"

	"tinygo.org/x/bluetooth"
)

// ProbeWiFi briefly enables the recorder's access point, observes the app's
// WiFi-info report, then disables it. It never changes the Mac's network.
// Opcode 27 is openWifi(bool); opcode 31 reports 10-byte SSID + 8-byte password.
func ProbeWiFi(ctx context.Context, s *Store) (map[string]any, error) {
	owner, e := s.Owner()
	if e != nil {
		return nil, e
	}
	defer owner.Close()
	a := bluetooth.DefaultAdapter
	if e = a.Enable(); e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	d, e := Connect(ctx, a, s.Config())
	if e != nil {
		return nil, e
	}
	defer d.Close()
	defer func() { _, _ = d.request(context.Background(), 27, []byte{0}) }()
	if e = d.send(27, []byte{1}); e != nil {
		return nil, e
	}
	report := map[string]any{"wifi_info_received": false, "mac_network_changed": false, "source_endpoint": "192.168.200.1:8475"}
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return report, ctx.Err()
		case <-deadline.C:
			return report, errors.New("no WiFi info report; capability not confirmed")
		case e := <-d.fail:
			return report, e
		case p := <-d.replies:
			if p.op == 27 {
				report["command_reply_bytes"] = len(p.data)
			}
			if p.op == 31 {
				report["wifi_info_received"] = true
				report["report_bytes"] = len(p.data)
				report["credential_lengths_match"] = len(p.data) == 18
				return report, nil
			}
		}
	}
}
