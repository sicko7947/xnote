# Transfer and search investigation

## Verified, 2026-10-04

- Go native CoreBluetooth connection, owner handshake, directory and real MP3
  download work on HD5GA00725. Automatic download and Codex transcription run
  concurrently as two independent workers.
- BLE audio characteristic B0B4 emits raw MP3 chunks (observed up to 244 bytes).
  There is no per-chunk filename or transfer ID. Opcode 11 selects the file and
  byte offset; completion is opcode 11/status 2. Multiple simultaneous requests
  are not a verified multiplexing protocol and are not enabled.
- Shortest outstanding files are downloaded first during backlog catch-up.
  Completed files start transcription without waiting for the whole backlog.
- Notification callbacks only enqueue copied bytes. Disk writes and decoding run
  outside the CoreBluetooth callback. UI progress updates are throttled to 2 Hz.
- Unchanged catalogs no longer rewrite every metadata/Markdown file. UI refreshes
  retain selection. JSON/Markdown writes are atomic and library mutations are
  serialized across processes. Only one worker owns Bluetooth for a library.
- Search benchmark: 100 recordings, each with 1,000 repeated mixed Chinese/English
  phrases, AND keyword query, Apple M1: 58.77 ms/op (5 measured iterations).
  This is a synthetic filesystem-search benchmark, not a million-file claim.
- AVAudioPlayer physical check: MP3 open, pause, duration, seek to midpoint, and
  1.5x rate succeed. Mouse positions use terminal cell widths, not UTF-8 byte offsets.

## Wi-Fi evidence / limit

Source: local 3.7.7 ARM64 AOT flow, latest 3.7.9 static artifact. Exact firmware
support is a separate matter.

- SettingKey3085s::openWifi = opcode 27, one-byte Boolean payload.
- deviceToAppWifiInfo = opcode 31; source parser reads 10-byte SSID + 8-byte password.
- DoWayWifiMgr connects to 192.168.200.1 TCP port 8475 after joining recorder Wi-Fi.
- Wi-Fi wraps the app request in additional framing, including a 32-bit length and
  `XnoteWifiTail   ` trailer; it stops the Bluetooth sync flow before starting Wi-Fi.
- Physical probe completed existing-owner handshake, sent Wi-Fi on, waited 8s,
  and sent Wi-Fi off during cleanup. No opcode 27/31 report was received. Support
  is NOT confirmed; no high-speed claim is made.
- The Mac's Wi-Fi/network route was not changed. A recorder hotspot could replace
  the Internet-bearing Wi-Fi connection; this must be tested before a seamless
  download-and-cloud-transcribe mode can use it.

The public BLE GATT structure can be read directly. Device-private opcodes, file
framing and session requirements still need app traces/source or controlled
real-device observations. Guessing extra GATT commands is not a performance fix.

Remaining: long-file throughput/transfer metrics, physical disconnect resume,
Wi-Fi model/firmware capability identification, overnight/sleep-wake validation.


Follow-up measured baseline: 49 transfers, 92.9 MiB total, weighted 46.2 KiB/s.
Added 64 KiB buffered writes instead of one filesystem write per BLE packet;
normal completion flushes and syncs before publishing the file, cancellation
flushes the received prefix for resume. Speed now uses actual received byte
counts rather than rounded percentage. These reduce client overhead and improve
measurement; an increase in radio throughput is not established. macOS backend
RequestConnectionParams is unimplemented in tinygo Bluetooth v0.16.0; invoking
it is not evidence of tuning the radio. Overview/CLI report remaining bytes and
a download-only ETA while actively transferring.
