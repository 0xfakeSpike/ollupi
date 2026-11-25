#!/bin/sh
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
mkdir -p "$root/eval/build"
for arch in $arches; do
	stamp=$root/eval/build/STAMP-$arch
	if [ "${1:-}" = "--if-stale" ] && [ "$(cat "$stamp" 2>/dev/null)" = "$version" ]; then
		echo "eval/build/ollupi-eval-linux-$arch is up to date ($version)"
		continue
	fi
	docker run --rm --platform "linux/$arch" -v "$root:/src" -v ollupi-eval-gocache:/root/go -w /src golang:1.26-alpine sh -c "
		apk add --no-cache build-base >/dev/null &&
		CGO_ENABLED=1 go build -buildvcs=false -tags sqlite_fts5 \
			-ldflags '-s -w -linkmode external -extldflags -static -X main.version=$version' \
			-o eval/build/ollupi-eval-linux-$arch ./eval"
	echo "$version" > "$stamp"
	echo "eval/build/ollupi-eval-linux-$arch ($version)"
done
