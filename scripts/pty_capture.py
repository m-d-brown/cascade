#!/usr/bin/env python3
"""Run a command under a real pty and capture its raw output to a file.

Used by scripts/screenshots.sh instead of freeze's own -x for two reasons:
freeze's --lines flag panics once it has to skip more than a handful of
lines into ANSI output this complex (freeze v0.2.2), and freeze's -x has no
way to send the child process any input at all — so a bubbletea display
that waits for a keypress before exiting (the live tree's "finished, hold
for browsing" state) can never be captured through freeze directly. This
captures the real pty session to a file first; freeze then renders that
file with `freeze -x "cat FILE"`, which needs neither --lines nor input.

Usage:
  pty_capture.py OUTFILE [--stop-after MARKER] -- CMD [ARGS...]

Without --stop-after, this just waits for CMD to exit on its own (the
plain, non-interactive case). With it, capture stops — and CMD is killed,
not asked to exit — the instant MARKER (a byte string) appears in the
output: by the time a read() delivers those bytes, the frame containing it
is already fully captured, so there's nothing to gain by waiting further,
and every extra moment risks capturing part of the *next* redraw too. This
is deliberately not "send a keypress and wait for a clean exit": a
dismissed bubbletea display erases its own inline output before the
process exits (so the finished frame would never make it into the file),
and this way it never gets the chance to.
"""
import os
import pty
import select
import sys
import time

DEADLINE_SECONDS = 30


def main() -> None:
    args = sys.argv[1:]
    outfile = args.pop(0)
    marker = None
    if args and args[0] == "--stop-after":
        marker = args[1]
        args = args[2:]
    if args and args[0] == "--":
        args = args[1:]
    cmd = args

    master, slave = pty.openpty()
    pid = os.fork()
    if pid == 0:
        os.close(master)
        os.setsid()
        os.dup2(slave, 0)
        os.dup2(slave, 1)
        os.dup2(slave, 2)
        os.close(slave)
        os.execvp(cmd[0], cmd)
        os._exit(127)  # pragma: no cover - only reached if execvp fails
    os.close(slave)

    buf = b""
    deadline = time.time() + DEADLINE_SECONDS
    with open(outfile, "wb") as f:
        while time.time() < deadline:
            r, _, _ = select.select([master], [], [], 0.5)
            if master in r:
                try:
                    chunk = os.read(master, 65536)
                except OSError:
                    break
                if not chunk:
                    break
                buf += chunk
                f.write(chunk)
                if marker is not None and marker.encode() in buf:
                    break
            wpid, _ = os.waitpid(pid, os.WNOHANG)
            if wpid != 0:
                return

    try:
        os.kill(pid, 9)
        os.waitpid(pid, 0)
    except (ProcessLookupError, ChildProcessError):
        pass


if __name__ == "__main__":
    main()
