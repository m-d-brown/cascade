#!/usr/bin/env python3
"""Extract the last real frame from a captured bubbletea inline-render pty
session (see pty_capture.py).

Bubbletea redraws an inline (non-alt-screen) view by moving the cursor up N
lines and rewriting them (ESC[<n>A). On exit it does one *more* such
redraw that erases its own output before restoring the terminal, and after
that the process's own remaining stdout (cascade's summary table) is simply
appended — so neither "content after the last ESC[<n>A" nor "the longest
such block" reliably finds the finished tree a person watching would
actually see. What's unique to it is the "▸" cursor marker viewBrowse
draws on the selected row, which appears nowhere else (not in the erasure,
not in the summary table): this takes the last redraw block that contains
it.

Usage: last_frame.py INFILE OUTFILE
"""
import re
import sys

CURSOR_UP = re.compile(rb"\x1b\[(\d+)A")
CURSOR_MARKER = "▸".encode()


def main() -> None:
    infile, outfile = sys.argv[1], sys.argv[2]
    with open(infile, "rb") as f:
        data = f.read()

    bounds = [m.start() for m in CURSOR_UP.finditer(data)] + [len(data)]
    frame = data
    for start, end in zip(reversed(bounds[:-1]), reversed(bounds[1:])):
        if CURSOR_MARKER in data[start:end]:
            frame = data[start:end]
            break

    # A run short enough to finish before the display ever redraws (nothing
    # to skip past above) leaves cli's own "run <id> · logs: …" banner line,
    # printed before the display starts, at the front — plain text with no
    # escape codes, unlike everything the display itself ever prints.
    esc = frame.find(b"\x1b")
    if esc > 0:
        frame = frame[esc:]

    with open(outfile, "wb") as f:
        f.write(frame)


if __name__ == "__main__":
    main()
