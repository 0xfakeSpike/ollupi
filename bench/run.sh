#!/bin/sh
# Runs the bench's experiments one at a time and keeps each one's output in bench/results, where bench/README.md
# reads its numbers from.
#
#   bench/run.sh [experiment...]    all of them, or those named
#   bench/run.sh -l                 lists them
#
# Before each experiment it waits until nothing else could skew it: Docker Desktop is not running, the lid is open,
# the CPU is at least 60% idle, and no comparison holds the machine (bench/compare/locked.sh). While it measures it
# holds bench/compare/.locks/gobench.lock, so no comparison starts, and keeps the Mac awake. A run the Mac slept
# through is thrown away and run again. Takes some two hours for all of them.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
results=$root/bench/results
locks=$root/bench/compare/.locks

all="parked isolated steady-512 steady-1024 steady-1536 steady-2048 steady-3072 steady-4096 steady-faults
steady-lognormal steady-workspace-write steady-full-access burst resume crash crash-lease mix fanout soak users
users-slots users-limit conns conns-router"

# args prints what an experiment runs as; experiments that keep a timeline write it beside their output.
args() {
	case $1 in
	parked) echo "parked -n 50000 -state waiting -latency 5s -rounds 3" ;;
	isolated) echo "isolated -k 20" ;;
	steady-faults) echo "steady -c 1024 -fail 0.05 -others 256" ;;
	steady-lognormal) echo "steady -c 1536 -sigma 0.6" ;;
	steady-workspace-write) echo "steady -c 1024 -command workspace-write" ;;
	steady-full-access) echo "steady -c 1024 -command full-access" ;;
	steady-*) echo "steady -c ${1#steady-}" ;;
	burst) echo "burst -n 3000 -slots 512 -calls 256 -latency 5s -jitter 2s -tool 1s -timeline $results/burst.csv" ;;
	resume) echo "resume" ;;
	crash) echo "crash -n 500 -kills 30" ;;
	crash-lease) echo "crash -n 400 -kills 8 -lease 1500ms" ;;
	mix) echo "mix" ;;
	fanout) echo "fanout" ;;
	soak) echo "soak -c 256 -d 8m -every 30s -latency 1s -jitter 200ms -tool 200ms -csv $results/soak.csv" ;;
	users) echo "users" ;;
	users-slots) echo "users -user-slots 64" ;;
	users-limit) echo "users -user-slots 64 -heavy-calls 64 -heavy-limit 32" ;;
	conns) echo "conns -n 1000,2000,4000,8000 -storm" ;;
	conns-router) echo "conns -n 1000,2000,4000 -router -storm" ;;
	*) return 1 ;;
	esac
}

if [ "${1:-}" = -l ]; then
	for id in $all; do printf '%-24s bench %s\n' "$id" "$(args "$id")"; done
	exit 0
fi
ids=${*:-$all}
for id in $ids; do
	args "$id" >/dev/null || {
		echo "bench/run.sh: no experiment $id; bench/run.sh -l lists them" >&2
		exit 1
	}
done

mac=false
[ "$(uname)" = Darwin ] && mac=true

# busy says what would skew a measurement now, or nothing.
busy() {
	if [ -e "$locks/compare.lock" ]; then
		echo "a comparison is running: $(cat "$locks/compare.lock")"
		return
	fi
	$mac || return 0
	if pgrep -qx com.docker.backend; then
		echo "Docker Desktop is running; quit it"
		return
	fi
	if ioreg -r -k AppleClamshellState -d 4 | grep -q '"AppleClamshellState" = Yes'; then
		echo "the lid is closed"
		return
	fi
	idle=$(top -l 2 -n 0 -s 1 | awk '/CPU usage/ { v = $7 } END { print int(v) }')
	if [ "$idle" -lt 60 ]; then
		echo "the CPU is only $idle% idle"
	fi
}

# slept says whether the Mac went to sleep since $1, a time as date '+%Y-%m-%d %H:%M:%S' prints it.
slept() {
	$mac || return 1
	pmset -g log | awk -v since="$1" '/^[0-9][0-9][0-9][0-9]-/ && $1 " " $2 >= since && $4 == "Sleep" { found = 1 } END { exit !found }'
}

machine() {
	if $mac; then
		echo "$(sysctl -n machdep.cpu.brand_string), $(sysctl -n hw.ncpu) cores, $(($(sysctl -n hw.memsize) / 1073741824)) GB"
	else
		echo "$(uname -m), $(getconf _NPROCESSORS_ONLN) cores"
	fi
}

work=$(mktemp -d)
mkdir -p "$locks" "$results"
trap 'rm -rf "$work"; rm -f "$locks/gobench.lock"' EXIT INT TERM
cd "$root"
go build -tags sqlite_fts5 -o "$work/bench" ./bench
commit=$(git rev-parse --short HEAD)
[ -z "$(git status --porcelain)" ] || commit="$commit with uncommitted changes"
if $mac; then
	caffeinate -i -w $$ &
fi

for id in $ids; do
	for try in 1 2; do
		said=
		while reason=$(busy) && [ -n "$reason" ]; do
			[ "$reason" = "$said" ] || echo "bench/run.sh: $id waits: $reason" >&2
			said=$reason
			sleep 30
		done
		echo "$$ $id" >"$locks/gobench.lock"
		since=$(date '+%Y-%m-%d %H:%M:%S')
		echo "bench/run.sh: $id: bench $(args "$id")" >&2
		{
			echo "# bench $(args "$id")"
			echo "# $commit, $since, $(machine)"
			# shellcheck disable=SC2046 # the arguments are words on purpose.
			"$work/bench" $(args "$id")
		} >"$work/$id.txt"
		rm -f "$locks/gobench.lock"
		if slept "$since"; then
			echo "bench/run.sh: $id: the Mac slept during the run; its result is thrown away" >&2
			continue
		fi
		mv "$work/$id.txt" "$results/$id.txt"
		break
	done
	[ -e "$results/$id.txt" ] && [ ! -e "$work/$id.txt" ] || {
		echo "bench/run.sh: $id: the Mac slept through both runs" >&2
		exit 1
	}
done
