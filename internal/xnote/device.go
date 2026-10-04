package xnote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/hajimehoshi/go-mp3"
	"tinygo.org/x/bluetooth"
)

type DeviceFile struct {
	Name string
	Size int64
}
type reply struct {
	op   byte
	data []byte
}
type Device struct {
	device       bluetooth.Device
	write, audio bluetooth.DeviceCharacteristic
	replies      chan reply
	fail         chan error
	buffer       []byte
	mu           sync.Mutex
}

func crc16(b []byte) uint16 {
	var crc uint16
	for _, v := range b {
		crc ^= uint16(v)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xa001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}
func frame(op byte, b []byte) []byte {
	p := append([]byte{160, 10, 1, op, byte(len(b))}, b...)
	return binary.BigEndian.AppendUint16(p, crc16(p))
}
func (d *Device) receive(b []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buffer = append(d.buffer, b...)
	for len(d.buffer) >= 5 {
		if !bytes.Equal(d.buffer[:3], []byte{160, 10, 1}) {
			d.buffer = d.buffer[1:]
			continue
		}
		n := 7 + int(d.buffer[4])
		if len(d.buffer) < n {
			return
		}
		p := d.buffer[:n]
		d.buffer = d.buffer[n:]
		if crc16(p[:n-2]) != binary.LittleEndian.Uint16(p[n-2:]) {
			continue
		}
		r := reply{p[3], append([]byte(nil), p[5:n-2]...)}
		select {
		case d.replies <- r:
		default:
			select {
			case d.fail <- errors.New("device reply overflow"):
			default:
			}
		}
	}
}
func (d *Device) send(op byte, b []byte) error {
	_, e := d.write.WriteWithoutResponse(frame(op, b))
	return e
}
func (d *Device) await(ctx context.Context, op byte) ([]byte, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case e := <-d.fail:
			return nil, e
		case r := <-d.replies:
			if r.op == op {
				return r.data, nil
			}
		}
	}
}
func (d *Device) request(ctx context.Context, op byte, b []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if e := d.send(op, b); e != nil {
		return nil, e
	}
	return d.await(ctx, op)
}
func matches(result bluetooth.ScanResult, c Config) bool {
	conflict := false
	for _, m := range result.ManufacturerData() {
		data := bytes.TrimRight(m.Data, "\x00")
		if bytes.HasSuffix(data, []byte(c.Serial)) {
			return true
		}
		if bytes.Contains(data, []byte("HD5G")) {
			conflict = true
		}
	}
	return !conflict && c.DeviceID != "" && stringsEqual(result.Address.String(), c.DeviceID)
}
func stringsEqual(a, b string) bool { return bytes.EqualFold([]byte(a), []byte(b)) }
func Connect(ctx context.Context, a *bluetooth.Adapter, c Config) (*Device, error) {
	found := make(chan bluetooth.ScanResult, 1)
	done := make(chan error, 1)
	go func() {
		done <- a.Scan(func(_ *bluetooth.Adapter, r bluetooth.ScanResult) {
			if matches(r, c) {
				select {
				case found <- r:
				default:
				}
			}
		})
	}()
	var result bluetooth.ScanResult
	select {
	case result = <-found:
		_ = a.StopScan()
		<-done
	case err := <-done:
		return nil, err
	case <-ctx.Done():
		_ = a.StopScan()
		<-done
		return nil, ctx.Err()
	}
	dev, e := a.Connect(result.Address, bluetooth.ConnectionParams{ConnectionTimeout: bluetooth.NewDuration(12 * time.Second)})
	if e != nil {
		return nil, e
	}
	d := &Device{device: dev, replies: make(chan reply, 4096), fail: make(chan error, 1)}
	success := false
	defer func() {
		if !success {
			_ = dev.Disconnect()
		}
	}()
	services, e := dev.DiscoverServices([]bluetooth.UUID{bluetooth.New16BitUUID(0xb0b0)})
	if e != nil {
		return nil, e
	}
	chars := map[string]bluetooth.DeviceCharacteristic{}
	for _, service := range services {
		cc, err := service.DiscoverCharacteristics(nil)
		if err != nil {
			return nil, err
		}
		for _, ch := range cc {
			chars[ch.UUID().String()] = ch
		}
	}
	var ok bool
	if d.write, ok = chars[bluetooth.New16BitUUID(0xb0b1).String()]; !ok {
		return nil, errors.New("missing command characteristic")
	}
	if d.audio, ok = chars[bluetooth.New16BitUUID(0xb0b4).String()]; !ok {
		return nil, errors.New("missing audio characteristic")
	}
	notify, ok := chars[bluetooth.New16BitUUID(0xb0b2).String()]
	if !ok {
		return nil, errors.New("missing notification characteristic")
	}
	if e = notify.EnableNotifications(d.receive); e != nil {
		return nil, e
	}
	owner, e := d.request(ctx, 2, nil)
	if e != nil {
		return nil, e
	}
	number, parseErr := strconv.ParseUint(string(bytes.TrimRight(owner, "\x00")), 10, 64)
	if len(owner) != 16 || parseErr != nil || number == 0 {
		return nil, errors.New("device needs its existing DOWAY binding")
	}
	confirmation, e := d.request(ctx, 3, append(owner, 0))
	if e != nil {
		return nil, e
	}
	if !bytes.Equal(confirmation, []byte{0}) {
		return nil, errors.New("device rejected session")
	}
	success = true
	return d, nil
}
func (d *Device) Close() { _ = d.device.Disconnect() }
func (d *Device) List(ctx context.Context) ([]DeviceFile, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if e := d.send(10, nil); e != nil {
		return nil, e
	}
	files := []DeviceFile{}
	seen := map[string]bool{}
	for {
		p, e := d.await(ctx, 10)
		if e != nil {
			return nil, e
		}
		if bytes.Equal(p, []byte{2}) {
			return files, nil
		}
		if len(p) < 19 || p[0] != 0 {
			return nil, errors.New("directory rejected")
		}
		name := string(p[1:15])
		if !validName(name) {
			return nil, errors.New("invalid device file name")
		}
		if !seen[name] {
			files = append(files, DeviceFile{name, int64(binary.BigEndian.Uint32(p[15:19]))})
			seen[name] = true
		}
	}
}
func (d *Device) Download(ctx context.Context, r Record, path string, progress func(int64)) error {
	if !validName(r.DeviceName) || r.Size <= 0 || r.Size > int64(^uint32(0)) {
		return errors.New("invalid recording size or identity")
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	stat, e := f.Stat()
	if e != nil {
		return e
	}
	offset := stat.Size()
	if offset >= r.Size {
		if e = f.Truncate(0); e != nil {
			return e
		}
		offset = 0
	}
	if _, e = f.Seek(offset, io.SeekStart); e != nil {
		return e
	}
	writer := bufio.NewWriterSize(f, 64*1024)
	defer writer.Flush() // Preserve the contiguous received prefix for resume on cancellation.
	packets := make(chan []byte, 4096)
	fail := make(chan error, 1)
	if e = d.audio.EnableNotifications(func(p []byte) {
		select {
		case packets <- append([]byte(nil), p...):
		default:
			select {
			case fail <- errors.New("audio buffer overflow"):
			default:
			}
		}
	}); e != nil {
		return e
	}
	defer func() { _ = d.send(12, nil); _ = d.audio.EnableNotifications(nil) }()
	payload := binary.BigEndian.AppendUint32([]byte(r.DeviceName), uint32(offset))
	ack, e := d.request(ctx, 11, payload)
	if e != nil {
		return e
	}
	if len(ack) != 5 || ack[0] != 0 || int64(binary.BigEndian.Uint32(ack[1:])) != r.Size {
		return errors.New("download rejected or file changed")
	}
	received := offset
	complete := false
	idle := time.NewTimer(20 * time.Second)
	defer idle.Stop()
	for !complete || received != r.Size {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-idle.C:
			return errors.New("transfer stalled; will resume on reconnect")
		case e := <-fail:
			return e
		case e := <-d.fail:
			return e
		case p := <-packets:
			if received+int64(len(p)) > r.Size {
				return errors.New("audio exceeds expected size")
			}
			if _, e = writer.Write(p); e != nil {
				return e
			}
			received += int64(len(p))
			progress(received)
			idle.Reset(20 * time.Second)
		case r := <-d.replies:
			if r.op == 11 {
				if !bytes.Equal(r.data, []byte{2}) {
					return errors.New("transfer failed")
				}
				complete = true
			}
		}
	}
	if e = writer.Flush(); e != nil {
		return e
	}
	return f.Sync()
}
func (d *Device) Delete(ctx context.Context, name string) error {
	if !validName(name) {
		return errors.New("invalid filename")
	}
	p, e := d.request(ctx, 13, []byte(name))
	if e != nil {
		return e
	}
	if !bytes.Equal(p, []byte{0}) {
		return errors.New("device did not confirm deletion")
	}
	return nil
}
func ValidateMP3(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	decoder, e := mp3.NewDecoder(f)
	if e != nil {
		return e
	}
	n, e := io.Copy(io.Discard, decoder)
	if e != nil {
		return e
	}
	if n == 0 {
		return fmt.Errorf("empty decoded MP3")
	}
	return nil
}
