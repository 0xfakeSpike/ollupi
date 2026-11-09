#!/bin/sh
# Runs Terminal-Bench 2.0 tasks through Harbor for one agent, then prints eval/report.py's comparison.
#
#   eval/run.sh <agent> [subset] [harbor run flags...]
#
# agent is ollupi or any agent Harbor ships, such as terminus-2. subset names a file in eval/subsets (default
# smoke), or is all. Further flags go to harbor run, such as --n-attempts 5 or -i one-task to run only that one.
#
# Before it starts it checks what would otherwise score every task 0 or test the wrong code: that Docker runs,
# that containers download over both HTTP and HTTPS, and, for ollupi, that eval/build holds the current code.
#
# Environment: MODEL (default deepseek/deepseek-flash), N trials at once (default 2), TASKS_DIR (default
# ~/harbor-tasks/terminal-bench-2.0, downloaded when missing). ollupi takes OLLUPI_API_KEY and OLLUPI_BASE_URL
# (see harbor/ollupi_agent.py); Harbor's agents read the provider's own variables, such as DEEPSEEK_API_KEY.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
agent=${1:?usage: eval/run.sh <agent> [subset] [harbor run flags...]}
shift
subset=smoke
case ${1:-} in
"" | -*) ;;
*)
	subset=$1
	shift
	;;
esac
model=${MODEL:-deepseek/deepseek-flash}
tasks_dir=${TASKS_DIR:-$HOME/harbor-tasks/terminal-bench-2.0}
export PATH="$HOME/.local/bin:$PATH"

fail() {
	echo "eval/run.sh: $*" >&2
	exit 1
}

command -v harbor >/dev/null || fail "harbor is not installed: uv tool install --python 3.12 harbor==0.25.0"
docker info >/dev/null 2>&1 || fail "Docker is not running"

# harbor download exports into the folder it is given, under terminal-bench/; runs read the tasks from there, so
# they need no clone from GitHub.
if [ ! -d "$tasks_dir" ]; then
	echo "Downloading terminal-bench@2.0 into $tasks_dir"
	tmp=$(mktemp -d)
	harbor download terminal-bench@2.0 -o "$tmp"
	mkdir -p "$(dirname "$tasks_dir")"
	mv "$tmp/terminal-bench" "$tasks_dir"
	rmdir "$tmp"
fi

# Verifiers install their tools with apt over HTTP and fetch uv over HTTPS before they test anything, and an
# agent reaches its model over HTTPS: a container that cannot do both scores 0 whatever the agent did.
echo "Checking that containers reach the internet"
docker run --rm python:3.12-slim python -c '
import sys, urllib.request
for url in ("http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages.xz",
            "https://github.com/astral-sh/uv/releases/download/0.9.5/uv-x86_64-unknown-linux-gnu.tar.gz"):
    try:
        with urllib.request.urlopen(url, timeout=30) as r:
            while r.read(1 << 16):
                pass
    except Exception as e:
        sys.exit(f"{url}: {e}")
' || fail "a container could not download what verifiers need; see eval/README.md on networks behind a proxy"

case $subset in
all) includes= ;;
*)
	[ -f "$root/eval/subsets/$subset.txt" ] || fail "no subset eval/subsets/$subset.txt"
	includes=$(sed -e 's/#.*//' -e '/^[[:space:]]*$/d' -e 's/^/-i /' "$root/eval/subsets/$subset.txt")
	;;
esac

case $agent in
ollupi)
	[ -n "${OLLUPI_API_KEY:-}" ] || fail "set OLLUPI_API_KEY to the provider's key"
	"$root/eval/build.sh" --if-stale
	export PYTHONPATH="$root/eval/harbor${PYTHONPATH:+:$PYTHONPATH}"
	harbor_agent=ollupi_agent:Ollupi
	;;
*) harbor_agent=$agent ;;
esac

cd "$root"
# Task names hold no spaces, so $includes splits into its -i flags.
# shellcheck disable=SC2086
harbor run -p "$tasks_dir" -m "$model" -a "$harbor_agent" $includes -n "${N:-2}" --max-retries 1 \
	-o "eval/jobs/$agent" "$@"
python3 "$root/eval/report.py" "$root/eval/jobs"
