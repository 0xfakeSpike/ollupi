#!/bin/sh
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
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
[ -n "${OLLUPI_API_KEY:-}" ] || fail "set OLLUPI_API_KEY to the provider's key"

if [ ! -d "$tasks_dir" ]; then
	echo "Downloading terminal-bench@2.0 into $tasks_dir"
	tmp=$(mktemp -d)
	harbor download terminal-bench@2.0 -o "$tmp"
	mkdir -p "$(dirname "$tasks_dir")"
	mv "$tmp/terminal-bench" "$tasks_dir"
	rmdir "$tmp"
fi

echo "Checking that containers reach the internet"
docker run --rm python:3.12-slim python -c '
import sys, urllib.request
for url in ("http://deb.debian.org/debian/dists/bookworm/main/binary-amd64/Packages.xz",
            "https://github.com/astral-sh/uv/releases/download/0.9.5/uv-x86_64-unknown-linux-gnu.tar.gz"):
    try:
        with urllib.request.urlopen(url, timeout=30) as r:
            r.read(1 << 20)  # a proxy that stalls on large downloads stalls well before 1 MB
    except Exception as e:
        sys.exit(f"{url}: {e}")
' || fail "a container could not download what verifiers need; see eval/README.md on networks behind a proxy"

named=
for arg in "$@"; do
	case $arg in -i | --include-task-name | --include-task-name=*) named=1 ;; esac
done
if [ -n "$named" ] || [ "$subset" = all ]; then
	includes=
else
	[ -f "$root/eval/subsets/$subset.txt" ] || fail "no subset eval/subsets/$subset.txt"
	includes=$(sed -e 's/#.*//' -e '/^[[:space:]]*$/d' -e 's/^/-i /' "$root/eval/subsets/$subset.txt")
fi

"$root/eval/build.sh" --if-stale

cd "$root"
# shellcheck disable=SC2086
PYTHONPATH="$root/eval/harbor${PYTHONPATH:+:$PYTHONPATH}" \
	harbor run -p "$tasks_dir" -m "$model" -a ollupi_agent:Ollupi $includes -n "${N:-2}" --max-retries 1 \
	-o eval/jobs "$@"
python3 "$root/eval/report.py"
