#!/bin/sh
dir="$(cd "$(dirname "$0")" && pwd)/.locks"
mkdir -p "$dir"
while [ -e "$dir/gobench.lock" ]; do sleep 5; done
echo "$$ $*" > "$dir/compare.lock"
trap 'rm -f "$dir/compare.lock"' EXIT INT TERM
"$@"
