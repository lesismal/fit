#!/bin/sh
# dist.sh cross-compiles fit for every platform it is released for and packs
# each binary with the READMEs: a .tar.gz per Unix target and a .zip per
# Windows one, in dist/, with their SHA-256 sums in dist/checksums.txt. release.yml runs it for a tag; `make dist` runs it here.
#
# Usage: scripts/dist.sh [version]
#
# The version (git describe when left out) is what `fit version` prints and
# goes in each archive's name. TARGETS, space separated GOOS/GOARCH pairs,
# narrows the platforms.
set -eu

root=$(git rev-parse --show-toplevel)
cd "$root"

version=${1:-$(git describe --tags --always --dirty)}
targets=${TARGETS:-"linux/amd64 linux/arm64 linux/arm linux/386 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 freebsd/amd64 freebsd/arm64"}

rm -rf dist
mkdir -p dist/.stage

for t in $targets; do
	goos=${t%/*}
	goarch=${t#*/}
	name=fit_${version#v}_${goos}_${goarch}
	stage=dist/.stage/$name
	bin=fit
	[ "$goos" = windows ] && bin=fit.exe

	echo "dist: $name"
	mkdir -p "$stage"
	# GOARM=7: 32-bit ARM boards that still run Linux, as the Raspberry Pi 2
	# and later, all have ARMv7.
	CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch GOARM=7 \
		go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$stage/$bin" ./cmd/fit
	cp LICENSE README.md README.zh-CN.md "$stage/"

	if [ "$goos" = windows ]; then
		(cd dist/.stage && zip -qr "../$name.zip" "$name")
	else
		tar -C dist/.stage -czf "dist/$name.tar.gz" "$name"
	fi
done

rm -rf dist/.stage
cd dist
if command -v sha256sum >/dev/null; then
	sha256sum fit_* > checksums.txt
else
	shasum -a 256 fit_* > checksums.txt
fi
cat checksums.txt
