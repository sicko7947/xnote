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
