# TUI responsiveness

The library UI keeps the last successful recording snapshot in memory. A single
background worker loads metadata, and refresh requests coalesce in a bounded
queue. Search, filters and opening a recording use the current snapshot while
new data loads; only the UI goroutine updates widgets. External changes appear
on the next periodic refresh (normally about one second).

Navigating the list no longer formats the hidden transcript. Transcript rendering
is deferred until opening a recording. Layout is rebuilt only when its dimensions,
view or library contents change. Translations use the current UI locale instead
of reopening config.json for each label.

Search uses a strings.Builder for searchable speaker labels and avoids building
that text entirely for an empty query. Previously, repeated concatenation copied
the full transcript once per segment, including for ordinary list refreshes.

A local Apple M1 measurement on 2026-10-05, using 105 recording metadata files and
three iterations, compared the previous disk-backed empty-query search with the
cached search used by the UI:

| Operation | Time/op | Allocated bytes/op |
| --- | ---: | ---: |
| Previous library search | 83.1 ms | 505.3 MB |
| Cached library search | 2.46 ms | 3.30 MB |

This measures library search, not full terminal input-to-display latency. Results
vary with transcript length, segment count, filesystem and terminal. The private
recordings and temporary measurement harness are not committed.

Regression coverage checks that navigation, opening and searching still work from
the last successful snapshot when a metadata file becomes unreadable, that hidden
transcripts are not eagerly rendered, and that refresh requests remain bounded.

## Follow-up: scroll stability and background work

- Table layout measures visible rows on each draw. Fixed-column widths are
  computed when library content changes, preventing columns from moving while
  scrolling. Synthetic 153 × 50-cell table draws: 90 rows 2.83 → 2.52 ms;
  1,000 rows 10.03 → 2.52 ms; 10,000 rows 107.26 → 3.20 ms. These exclude terminal
  transport and do not promise the same gains for the current 90-record library.
- Metadata refresh preserves a wheel-scrolled viewport. Calling tview.Select on
  an unchanged selection had forced the viewport back to an offscreen selected
  recording on the next draw.
- Periodic UI refresh only draws when visible data changes, audio is playing, a
  notice expires, or the live connection panel is open. Status heartbeat timestamps
  alone do not repaint the library.
- Store polling checks metadata inode, size and modification time and only decodes
  changed files. Returned records own their mutable slices. A synthetic 100-record
  poll with 100 segments per recording measured 24.43 → 2.64 ms and 9.92 → 1.09 MB
  allocation. Other CLI processes remain visible; files are not blindly cached.
- Transcription/summary status changes publish immediately. Identical status now
  uses a 10-second heartbeat, reducing the two idle schedulers' combined atomic
  fsync writes from roughly 4 per second to 0.2 per second.
- Markdown serialization uses a builder; tests verify byte-for-byte compatibility.

The performance suite also covers cross-process metadata replacement, addition,
deletion and corrupt files; concurrent readers/writers; idle status publishing;
and wheel scrolling through a background metadata update.

## Terminal repaint fix and live measurements

The remaining flicker was not just library search. tview clears its logical
screen before painting widgets; tcell's SetContent marks intermediate rune
changes dirty, even when the final frame is unchanged. `frameScreen` stages the
frame and submits only final changed cells to the underlying screen. Input,
cursor, terminal lifecycle, explicit Sync and default-style invalidation remain
handled by tcell. Tests cover moving wide Chinese glyphs, combining marks,
removal, resizing, styles and explicit resynchronization.

A 153 × 50 simulation with 90 rows measured unchanged glyph cells marked dirty
at 3,159 before and zero after. Moving selection changed 3,370 dirty cells before
and 298 after. Frame processing itself measured 3.09 → 2.78 ms; the larger gain
is avoiding unnecessary terminal traffic, not a claim of 10× faster layout.

Live measurements in the same Mac tmux pane and 90-record library:

| Measurement | Before | After |
| --- | ---: | ---: |
| Idle terminal output over 10 seconds | 744,800 bytes | 0 bytes |
| Twenty Down key events, starting at Home | 743,028 bytes | 26,883 bytes |
| Idle process CPU time over about 10 seconds | 2.95 s | 0.34 s |

The idle comparison includes all background/cache/redraw changes. The key-event
comparison isolates the final-frame screen layer on top of the earlier changes.
CPU samples are short observations (about 29.5% → 3.4% of one core), not a bound
under transfer, transcription or playback. Pipe-pane measurements establish byte
output, not subjective flicker on every terminal or remote connection.
