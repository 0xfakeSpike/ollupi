#!/bin/sh
# Builds the eval command for the Linux containers benchmark tasks run in, static so that it runs on any of them,
# into eval/build. ollupi's SQLite driver needs cgo, so the build runs in Docker. Run from anywhere.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
version=$(git -C "$root" describe --tags --always --dirty)
mkdir -p "$root/eval/build"
for arch in ${ARCHES:-amd64 arm64}; do
	docker run --rm --platform "linux/$arch" -v "$root:/src" -v ollupi-eval-gocache:/root/go -w /src golang:1.25-alpine sh -c "
		apk add --no-cache build-base >/dev/null &&
		CGO_ENABLED=1 go build -buildvcs=false -tags sqlite_fts5 \
			-ldflags '-s -w -linkmode external -extldflags -static -X main.version=$version' \
			-o eval/build/ollupi-eval-linux-$arch ./eval"
	echo "eval/build/ollupi-eval-linux-$arch ($version)"
done
