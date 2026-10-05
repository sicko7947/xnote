# Transfer and search investigation

## Linux setup, 2026-10-05

- Linux build and mpv JSON IPC lifecycle validated. Sync runs while the TUI or
  `watch` process stays open, including in a detached tmux session.
- Codex Dictate and ElevenLabs Scribe v2 each successfully transcribed the same
  short bundled demonstration audio. This is not a long-recording benchmark.
- Connected to HD5GA00725 and read 104 recording entries. Complete physical
  Linux download acceptance is still blocked: a 1,190-byte transfer produced
  repeated short audio notifications and exceeded its declared size. A resumed
  transfer from offset 1,024 emitted the same 166-byte value twice. Initial
  acknowledgements were also repeated. Only an exact duplicate of the validated
  acknowledgement is ignored; audio values are never deduplicated heuristically.
- Both command and audio notifications now use BlueZ AcquireNotify and one
  ordered socket reader, bypassing godbus's concurrent signal dispatch. The
  physical repeated audio values also occur through this path, so the underlying
  cause is unresolved. Overruns discard the untrusted partial file rather than
  resuming it. No successful Linux throughput claim is made.
- Fixed a receive-queue overflow hazard: after the first dropped packet the
  queue now rejects later packets, preserving a contiguous partial-file prefix
  for byte-offset resume. A regression test covers the previous gap scenario.
- Keep a single BLE file request at a time. Download and transcription remain
  separate concurrent workers; no new radio-throughput claim is made.
- tinygo Bluetooth's Linux connect call does not enforce ConnectionTimeout.
  A separate bounded BlueZ preconnect runs before tinygo's connection call. The
  subsequent native calls are not all context-bounded, so this is not a strict
  whole-call deadline guarantee. If a native call hangs, restart the TUI/watch
  process manually.
- A manual direct LE connection helped subsequent BlueZ connections, but a
  reliable handoff has not been established. No raw ATT bootstrap or global
  Bluetooth configuration change is installed. Disconnect/resume integrity,
  recorder Wi-Fi and overnight operation remain unverified on Linux.

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

## macOS foreground run, 2026-10-05

- The TUI runs in an existing tmux pane and has completed multiple real BLE
  downloads on HD5GA00725. Observed active transfer rates were about 59–63 KB/s.
  Closing the TUI released the worker; restarting resumed the partial file.
- A local read-only benchmark found metadata enumeration of 104 records took
  about 6–7 ms. A 7.78 MB MP3 took about 4.35 s to fully decode/validate and
  66 ms to read its duration; its radio transfer at 59 KB/s takes about 132 s.
  Overlapping validation could save roughly 3%, not multiply radio throughput.
- BLE remains one file request at a time: audio packets have no file or request
  identifier. No concurrent requests or guessed radio parameters were enabled.
- The original app also exposes USB support (opcode 19 in the 3085s protocol),
  but no USB recording volume was mounted on this Mac. Hardware support and
  import performance remain unverified; this run keeps Bluetooth selected.
- ElevenLabs uploads now stream the original file within the documented size
  and duration limits. This avoids expanding MP3 into many WAV uploads and
  lets Scribe v2 handle long-file parallel processing on the server. This
  improves the upload path, not the BLE radio speed.
  Source: https://elevenlabs.io/docs/overview/capabilities/speech-to-text
- Automatic transcription and cloud transcript import are opt-in. The default
  foreground workflow only downloads recordings, with no provider fallback.

## Follow-up host-path audit, 2026-10-05

- The single-recorder transfer still uses one shared audio characteristic and
  one opcode-11 completion stream. Raw audio notifications contain no filename,
  transfer ID or offset. Adding multiple download goroutines would give them
  competing consumers of the same bytes and acknowledgements; it is not a safe
  parallel-download protocol. The existing single owner also prevents a second
  XNote process from opening another transfer against the same library.
- `BenchmarkBLEReceiveDrain` makes the host notification-copy, queue and 64 KiB
  buffering overhead reproducible. On this Mac (Apple M1), three one-second
  runs measured 80–87 ns per 244-byte packet, one 256-byte allocation, and
  0.003723 underlying writes per packet (about 269 packets per write). Its sink
  counts bytes in memory: these figures deliberately exclude CoreBluetooth,
  scheduling across callback threads, disk I/O, fsync and the radio. They are
  **not** BLE throughput measurements. At the previously observed ~250 packets
  per second, optimizing this particular copy/queue path has little headroom.
- The current queue holds 4,096 notifications (about 976 KiB at 244 bytes each),
  while progress updates occur at most twice per second. A larger queue or extra
  host threads does not request more radio bandwidth. Queue overflow remains a
  transfer failure that preserves only a contiguous prefix for resume.
- A fresh read-only measurement on a retained 7,781,074-byte MP3 found full
  decode validation took 4.50–5.26 seconds across three runs; reading duration
  took 66–69 ms. At the earlier 59 KB/s radio observation that file would spend
  about 132 seconds in transit. Overlapping validation with the next download
  could theoretically save around 3–4% on a similar backlog, before accounting
  for pipeline costs; this is an estimate, not a tested speedup. Validation
  remains serialized so incomplete/unvalidated files are not published.
- The native macOS dependency (`tinygo.org/x/bluetooth` v0.16.0) implements
  `RequestConnectionParams` as a no-op. `GetMTU` reports the maximum write value
  length; it is not an API for increasing incoming notification bandwidth.
  Neither extra download threads nor an MTU setting is exposed as a misleading
  speed control. USB/Wi-Fi capability tests remain the route to investigate a
  materially faster transport, subject to this recorder's firmware support.

Run the host benchmark without a recorder:

```sh
go test ./internal/xnote -run '^$' -bench '^BenchmarkBLEReceiveDrain$' -benchmem -benchtime=1s -count=3
```

### Read-only device verification (2026-10-05)

With the TUI briefly stopped to release its exclusive connection, a signed Mac
probe listed the device and downloaded one retained recording into a temporary
directory. It did not change library metadata or the original recording.

- 603,402 bytes transferred in 10.166 seconds: 59,354 bytes/s (about 58 KiB/s).
- Full MP3 validation took 0.452 seconds.
- SHA-256 matched the existing local original exactly; temporary audio removed.

This confirms current single-stream throughput and integrity for one recording.
No before/after radio speed improvement is claimed by the UI/background changes.
