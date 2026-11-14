#!/bin/sh
# Builds eval, static, for the Linux containers tasks run in, into eval/build; see eval/README.md.
#
#   eval/build.sh [--if-stale]
#
# ARCHES picks the architectures (default amd64). ollupi's SQLite driver needs cgo, so the build runs in Docker.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
version=$(git -C "$root" describe --tags --always --dirty)
case $version in
*-dirty)
	changes=$( (git -C "$root" diff HEAD; git -C "$root" ls-files --others --exclude-standard -z | (cd "$root" && xargs -0 cat 2>/dev/null)) | shasum | cut -c1-8)
	version="$version+$changes"
	;;
esac
arches=${ARCHES:-amd64}
stamp="$version $arches"
if [ "${1:-}" = "--if-stale" ] && [ "$(cat "$root/eval/build/STAMP" 2>/dev/null)" = "$stamp" ]; then
	echo "eval/build is up to date ($version)"
	exit 0
fi
mkdir -p "$root/eval/build"
for arch in $arches; do
	docker run --rm --platform "linux/$arch" -v "$root:/src" -v ollupi-eval-gocache:/root/go -w /src golang:1.26-alpine sh -c "
		apk add --no-cache build-base >/dev/null &&
		CGO_ENABLED=1 go build -buildvcs=false -tags sqlite_fts5 \
			-ldflags '-s -w -linkmode external -extldflags -static -X main.version=$version' \
			-o eval/build/ollupi-eval-linux-$arch ./eval"
	echo "eval/build/ollupi-eval-linux-$arch ($version)"
done
echo "$stamp" > "$root/eval/build/STAMP"
