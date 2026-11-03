#!/bin/sh
# Runs every comparison experiment in turn; each holds .locks/compare.lock (see locked.sh) while it measures.
# Results are appended to results/results.txt. Takes roughly an hour and a half.
set -e
cd "$(dirname "$0")"
node="node --expose-gc --no-warnings"
L=./locked.sh

for h in pi-sdk pi-durable dsh; do
	$L $node parked.mjs --harness $h --n 1000
done
$L $node parked.mjs --harness pi-durable --n 1000 --retention 0
$L $node parked.mjs --harness pi-sdk --n 10000
$L $node parked.mjs --harness pi-durable --n 10000
$L $node parked.mjs --harness dsh --n 5000

for c in 64 256 1024 2048; do
	for h in pi-sdk pi-durable dsh; do
		$L $node steady.mjs --harness $h --c $c
	done
done

for h in pi-sdk pi-durable dsh; do
	$L $node burst.mjs --harness $h --n 3000 --limit 256
done

$L $node crash.mjs --n 50 --turns 20 --kills 30
$L $node crash-sdk.mjs --n 200 --turns 5 --at 1500
$L $node crash-dsh.mjs --mode closed --n 50 --turns 20 --kills 30
$L $node crash-dsh.mjs --mode queued --n 50 --turns 20 --kills 30
$L $node isolated-dsh.mjs --k 10
