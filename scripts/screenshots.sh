#!/usr/bin/env bash
# Regenerates the images embedded in README.md, from real, isolated runs —
# not mocked-up text. Run this after a change to the CLI's output (the live
# tree's layout or colors, the flamegraph's shape, …) so the pictures in the
# README stay honest.
#
# Requires the `freeze` CLI (https://github.com/charmbracelet/freeze), which
# rasterizes a terminal session into a PNG:
#
#   go install github.com/charmbracelet/freeze@latest
#
# ...and python3, for pty_capture.py and last_frame.py (see the comments
# below for why plain freeze -x isn't enough on its own).
#
# Usage, from the repo root:
#
#   scripts/screenshots.sh
#
# Agents: re-run this whenever you change something that would change what
# these images show (the live tree's layout or colors, examples/cascade's
# actions, examples/engine/complete's calls), then look at the results under
# docs/img/ before committing — this script cannot check that they still
# look right, only that they were regenerated.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

if ! command -v freeze >/dev/null 2>&1; then
	echo "error: freeze is not on PATH — go install github.com/charmbracelet/freeze@latest" >&2
	exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# An isolated state journal and log directory per run, so this script's own
# output never depends on — or pollutes — anything a real run of `cascade` or
# examples/engine/complete on this machine has left behind.
export XDG_STATE_HOME="$work/state"
export TMPDIR="$work/tmp"
mkdir -p "$XDG_STATE_HOME" "$TMPDIR"

out="$repo_root/docs/img"
mkdir -p "$out"

freeze_flags=(
	--window
	--width 1160
	--wrap 100
	--padding 26,32
	--border.radius 10
	--shadow.blur 24
	--shadow.x 0
	--shadow.y 10
	--font.size 15
)

## --- cascade run, twice, as the live tree -----------------------------

# The tree, not cmd/cascade's own --plain summary: cmd/cascade sets
# App.ExitWhenDone (see cmd/cascade/main.go) so a real user gets their shell
# back immediately, but that also means the finished tree is never on
# screen long enough to photograph. tree_demo.go is the same Flow without
# that field, so the tree holds itself open for browsing the way a
# hand-authored workflow's does.
go build -o "$work/tree_demo" scripts/tree_demo.go

demo="$work/demo"
mkdir -p "$demo"
cp examples/cascade/cascade.yaml "$demo/"

# freeze's own -x has no way to send the child input at all, so dismissing
# the finished tree (which waits for a keypress) isn't possible through
# freeze directly — and freeze's --lines flag panics once asked to skip
# more than a handful of lines into ANSI output this complex (both freeze
# v0.2.2 issues, not something this script can fix). pty_capture.py works
# around both: it captures the real pty session (so colors and the tree's
# layout render exactly as a person watching would see) to a file, stopping
# the instant the finished tree's footer appears — which also sidesteps a
# third thing: a dismissed inline bubbletea display erases its own output
# before the process exits, so waiting for a clean exit would lose the
# frame entirely. last_frame.py then isolates that one finished frame from
# the redraw history in the capture, and freeze renders just that.
capture_tree() {
	local out_file="$1"
	( cd "$demo" && timeout 15 python3 "$repo_root/scripts/pty_capture.py" \
		"$work/t.raw" --stop-after "q quit" -- "$work/tree_demo" run )
	python3 "$repo_root/scripts/last_frame.py" "$work/t.raw" "$work/frame.raw"
	timeout 15 freeze -x "cat $work/frame.raw" "${freeze_flags[@]}" -o "$out_file"
}

# Screenshot 1: a real run — every action actually executes.
capture_tree "$out/cascade-run.png"

# Screenshot 2: run it again, unchanged — the freshness checks this whole
# project is for. Same binary, same directory, so the fingerprint that
# decides "already done" hasn't moved.
capture_tree "$out/cascade-run-again.png"

echo "wrote $out/cascade-run.png and $out/cascade-run-again.png"

## --- flamegraph -----------------------------------------------------------

# examples/cascade barely overlaps anything (one long warmup, then a handful
# of near-instant actions), so it makes a poor advertisement for a
# *concurrency* timeline. examples/engine/complete does — three platforms
# building at once, two test suites, two announce channels — so the
# flamegraph comes from that instead. --speed keeps the wait under a few
# seconds without collapsing every bar to a sliver.
go build -o "$work/release" ./examples/engine/complete
(
	cd "$work"
	./release run --speed 0.3 --plain >/dev/null </dev/null
	run_id="$(./release runs | head -1 | awk '{print $1}')"
	./release flamegraph "$run_id" >"$work/trace.json"
)
go run "$repo_root/scripts/flamegraph_svg.go" "$work/trace.json" >"$out/flamegraph.svg"

echo "wrote $out/flamegraph.svg"
