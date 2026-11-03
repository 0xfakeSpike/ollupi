#!/bin/sh
# locked.sh CMD...: waits while ollupi's own bench holds .locks/gobench.lock, then runs CMD holding .locks/compare.lock,
# so the two sets of measurements never share the machine.
dir="$(cd "$(dirname "$0")" && pwd)/.locks"
mkdir -p "$dir"
while [ -e "$dir/gobench.lock" ]; do sleep 5; done
echo "$$ $*" > "$dir/compare.lock"
trap 'rm -f "$dir/compare.lock"' EXIT INT TERM
"$@"
