#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
playground=$(mktemp -d "${TMPDIR:-/tmp}/grd-playground.XXXXXX")
server_pid=

cleanup() {
	if [ -n "$server_pid" ]; then
		kill "$server_pid" 2>/dev/null || true
		wait "$server_pid" 2>/dev/null || true
	fi
	rm -rf "$playground"
}
trap cleanup EXIT INT TERM

bin="$playground/bin"
repo="$playground/repository"
state="$playground/state"
ready="$playground/ready.json"
diagnostics="$playground/grds.log"
mkdir -p "$bin" "$repo" "$state"

go build -o "$bin/grd" "$root/cmd/grd"
go build -o "$bin/grds" "$root/cmd/grds"

git -C "$repo" init -b main >/dev/null
git -C "$repo" config user.name 'GRD Playground'
git -C "$repo" config user.email 'grd-playground@example.invalid'
mkdir -p "$repo/.grd"
printf '%s\n' 'Keep this playground understandable and safe to change.' >"$repo/.grd/purpose.md"
printf '%s\n' '## simplicity' 'Assignee: principal:playground' 'Instruction: Does this keep the playground simple?' >"$repo/.grd/priorities.md"
printf '%s\n' '# GRD playground' >"$repo/README.md"
git -C "$repo" add .grd README.md
git -C "$repo" commit -m 'Initialize accepted Intent' >/dev/null

"$bin/grds" \
	--repository playground \
	--git-dir "$repo/.git" \
	--ledger "$state/ledger.jsonl" \
	--trunk refs/heads/main \
	--evaluator "$root/examples/evaluator-always-clear.sh" \
	--listen 127.0.0.1:0 \
	--producer local:playground \
	--poll-interval 20ms \
	>"$ready" 2>"$diagnostics" &
server_pid=$!

attempt=0
while [ ! -s "$ready" ]; do
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 100 ]; then
		printf '%s\n' 'playground server did not become ready' >&2
		if [ -f "$diagnostics" ]; then
			sed -n '1,120p' "$diagnostics" >&2
		fi
		exit 1
	fi
	sleep 0.05
done

ready_json=$(sed -n '1p' "$ready")
server=$(printf '%s\n' "$ready_json" | sed -n 's/.*"control":"\([^"]*\)".*/\1/p')
if [ -z "$server" ]; then
	printf '%s\n' 'readiness receipt has no control URL' >&2
	exit 1
fi

initial=$("$bin/grd" intent --server "$server")
base_intent=$(printf '%s\n' "$initial" | sed -n 's/.*"intent":"\([^"]*\)".*/\1/p')
git -C "$repo" switch -c experiment >/dev/null
printf '%s\n' 'A proposed line.' >>"$repo/README.md"
git -C "$repo" add README.md
git -C "$repo" commit -m 'Propose a playground change' >/dev/null
revision=$(git -C "$repo" rev-parse HEAD)

receipt=$("$bin/grd" propose \
	--server "$server" \
	--base-intent "$base_intent" \
	--engine git \
	--revision "$revision" \
	--idempotency-key playground-proposal-1)
retry=$("$bin/grd" propose \
	--server "$server" \
	--base-intent "$base_intent" \
	--engine git \
	--revision "$revision" \
	--idempotency-key playground-proposal-1)
if [ "$retry" != "$receipt" ]; then
	printf '%s\n' 'idempotent proposal retry returned a different receipt' >&2
	exit 1
fi
version=$(printf '%s\n' "$receipt" | sed -n 's/.*"version":{"id":"\([^"]*\)".*/\1/p')
if [ -z "$version" ]; then
	printf '%s\n' 'proposal receipt has no Version id' >&2
	exit 1
fi

attempt=0
inspection=
while :; do
	inspection=$("$bin/grd" version --server "$server" --id "$version")
	case "$inspection" in
		*'"promotion":{'*) break ;;
	esac
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 100 ]; then
		printf '%s\n' 'Version was not promoted' >&2
		printf '%s\n' "$inspection" >&2
		if [ -f "$diagnostics" ]; then
			sed -n '1,120p' "$diagnostics" >&2
		fi
		exit 1
	fi
	sleep 0.05
done

accepted=$("$bin/grd" intent --server "$server")
case "$accepted" in
	*"\"revision\":\"$revision\""*) ;;
	*)
		printf '%s\n' 'accepted Intent does not contain the proposed revision' >&2
		printf '%s\n' "$accepted" >&2
		exit 1
		;;
esac
main_revision=$(git -C "$repo" rev-parse refs/heads/main)
if [ "$main_revision" != "$revision" ]; then
	printf '%s\n' 'Git trunk projection did not advance to accepted Intent' >&2
	exit 1
fi

printf '%s\n' "$ready_json" "$initial" "$receipt" "$inspection" "$accepted"
