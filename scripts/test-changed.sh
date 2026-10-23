#!/bin/sh
# test-changed.sh runs the race detector on the packages a change touches and
# on every package that depends on them, tests included (rule 20): the check
# to run while working. The full `go test -race ./...` runs once, before a
# push.
#
# Usage: scripts/test-changed.sh [base] [go test flags...]
#
# The change is what differs from base (origin/main when left out): commits
# since it and the working tree, untracked files included. A change to go.mod
# or go.sum tests everything; one with no Go file tests nothing. DRYRUN=1
# prints the packages instead of testing them.
set -eu

base=${1:-origin/main}
[ $# -gt 0 ] && shift

root=$(git rev-parse --show-toplevel)
cd "$root"
module=$(go list -m)

changed=$( {
	git diff --name-only "$base"...HEAD 2>/dev/null || git diff --name-only "$base"
	git diff --name-only HEAD
	git ls-files --others --exclude-standard
} | sort -u)
# Nested modules, as test/conformance, are not fit's: their files are left
# out.
for m in $(git ls-files --cached --others --exclude-standard '*/go.mod'); do
	changed=$(printf '%s\n' "$changed" | grep -v "^$(dirname "$m")/" || true)
done

if printf '%s\n' "$changed" | grep -qE '^go\.(mod|sum)$'; then
	echo "test-changed: go.mod or go.sum changed; testing everything"
	if [ "${DRYRUN:-}" = 1 ]; then
		echo ./...
		exit 0
	fi
	exec go test -race "$@" ./...
fi

# The packages whose directories hold a changed Go file.
dirs=$(printf '%s\n' "$changed" | grep -E '\.go$' | while read -r f; do dirname "$f"; done | sort -u)
if [ -z "$dirs" ]; then
	echo "test-changed: no Go file changed since $base; nothing to test"
	exit 0
fi
seeds=$(for d in $dirs; do
	if [ -d "$d" ]; then
		if [ "$d" = "." ]; then echo "$module"; else echo "$module/$d"; fi
	fi
done)

# Every package of the module whose tests depend on a seed. With -test, the
# test binary's entry ("p.test") lists everything the package and its tests
# import, transitively.
pkgs=$(go list -test -f '{{.ImportPath}} {{join .Deps " "}}' ./... | SEEDS="$seeds" awk '
	BEGIN { n = split(ENVIRON["SEEDS"], s, "\n"); for (i = 1; i <= n; i++) if (s[i] != "") seed[s[i]] = 1 }
	{
		p = $1
		# Skip the variants built for a test binary, "p [p.test]" and an
		# external test package "p_test [p.test]": "p.test" stands for them.
		if ($2 ~ /^\[/) next
		if (p ~ /\.test$/) sub(/\.test$/, "", p)
		else if (p ~ /_test$/) next
		hit = (p in seed)
		for (i = 2; i <= NF && !hit; i++) if ($i in seed) hit = 1
		if (hit) print p
	}' | sort -u)

# test/e2e builds and runs the fit binary rather than importing what it is
# built from, so it goes with cmd/fit.
if printf '%s\n' "$pkgs" | grep -qx "$module/cmd/fit"; then
	pkgs=$(printf '%s\n%s\n' "$pkgs" "$module/test/e2e" | sort -u)
fi

if [ -z "$pkgs" ]; then
	echo "test-changed: no package of the module depends on the change"
	exit 0
fi
echo "test-changed: $(printf '%s\n' "$pkgs" | wc -l | tr -d ' ') packages"
if [ "${DRYRUN:-}" = 1 ]; then
	printf '%s\n' "$pkgs"
	exit 0
fi
# shellcheck disable=SC2086
exec go test -race "$@" $pkgs
