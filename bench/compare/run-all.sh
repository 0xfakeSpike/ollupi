#!/bin/sh
set -e
cd "$(dirname "$0")"
node="node --expose-gc --no-warnings"
L=./locked.sh
all="ollupi pi-sdk pi-durable dsh"

for h in $all; do
	$L $node parked.mjs --harness $h --n 1000
done
$L $node parked.mjs --harness pi-durable --n 1000 --retention 0
$L $node parked.mjs --harness ollupi --n 10000
for per in 2000 64; do
	$L $node parked.mjs --harness ollupi --n 2000 --perUser $per
done
$L $node parked.mjs --harness pi-sdk --n 10000
$L $node parked.mjs --harness pi-durable --n 10000
$L $node parked.mjs --harness dsh --n 5000

for c in 64 256 1024 2048; do
	for h in $all; do
		$L $node steady.mjs --harness $h --c $c
	done
done

for h in $all; do
	$L $node burst.mjs --harness $h --n 3000 --limit 256
done

$L $node crash-ollupi.mjs --n 50 --turns 20 --kills 30
$L $node crash.mjs --n 50 --turns 20 --kills 30
$L $node crash-sdk.mjs --n 200 --turns 5 --at 1500
$L $node crash-dsh.mjs --mode closed --n 50 --turns 20 --kills 30
$L $node crash-dsh.mjs --mode queued --n 50 --turns 20 --kills 30

for h in ollupi dsh; do
	$L $node background.mjs --harness $h --n 200 --seconds 20
	$L $node background.mjs --harness $h --n 200 --seconds 20 --kill 8
done

$L $node isolated-dsh.mjs --k 10
