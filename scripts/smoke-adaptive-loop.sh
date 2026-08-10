#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
playground=$(mktemp -d "${TMPDIR:-/tmp}/grd-adaptive.XXXXXX")
server_pid=
watch_pid=

cleanup() {
	if [ -n "$watch_pid" ]; then
		kill "$watch_pid" 2>/dev/null || true
		wait "$watch_pid" 2>/dev/null || true
	fi
	if [ -n "$server_pid" ]; then
		kill "$server_pid" 2>/dev/null || true
		wait "$server_pid" 2>/dev/null || true
	fi
	rm -rf "$playground"
}
trap cleanup EXIT INT TERM

fail() {
	printf '%s\n' "$1" >&2
	if [ -s "$diagnostics" ]; then
		sed -n '1,160p' "$diagnostics" >&2
	fi
	exit 1
}

bin="$playground/bin"
repo="$playground/repository"
state="$playground/state"
ready="$state/ready.json"
diagnostics="$state/grds.log"
watch_output="$state/watch.jsonl"
mkdir -p "$bin" "$repo" "$state"

go build -o "$bin/grd" "$root/cmd/grd"
go build -o "$bin/grds" "$root/cmd/grds"

git -C "$repo" init -b main >/dev/null
git -C "$repo" config user.name 'GRD Adaptive Playground'
git -C "$repo" config user.email 'grd-adaptive@example.invalid'
mkdir -p "$repo/.grd"
printf '%s\n' 'Keep independent contributions intact while adapting them to accepted change.' >"$repo/.grd/purpose.md"
printf '%s\n' \
	'## collaboration' \
	'Assignee: principal:playground' \
	'Instruction: Does this contribution preserve the work already accepted?' \
	>"$repo/.grd/priorities.md"
printf '%s\n' '# Adaptive playground' >"$repo/README.md"
git -C "$repo" add .grd README.md
git -C "$repo" commit -m 'Initialize accepted Intent' >/dev/null
base=$(git -C "$repo" rev-parse HEAD)

git -C "$repo" switch -c change-two >/dev/null
printf '%s\n' 'Contribution two.' >"$repo/contributor-two.txt"
git -C "$repo" add contributor-two.txt
git -C "$repo" commit -m 'Add contribution two' >/dev/null

git -C "$repo" switch -c change-one "$base" >/dev/null
printf '%s\n' 'Contribution one.' >"$repo/contributor-one.txt"
git -C "$repo" add contributor-one.txt
git -C "$repo" commit -m 'Add contribution one' >/dev/null

"$bin/grds" \
	--repository playground \
	--git-dir "$repo/.git" \
	--ledger "$state/ledger.jsonl" \
	--trunk refs/heads/main \
	--evaluator "$root/examples/evaluator-requires-response.sh" \
	--listen 127.0.0.1:0 \
	--producer principal:playground \
	--poll-interval 20ms \
	>"$ready" 2>"$diagnostics" &
server_pid=$!

attempt=0
while [ ! -s "$ready" ]; do
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 100 ]; then
		fail 'adaptive playground server did not become ready'
	fi
	sleep 0.05
done

ready_json=$(sed -n '1p' "$ready")
server=$(printf '%s\n' "$ready_json" | sed -n 's/.*"control":"\([^"]*\)".*/\1/p')
if [ -z "$server" ]; then
	fail 'readiness receipt has no control URL'
fi

"$bin/grd" watch --server "$server" --poll-interval 20ms >"$watch_output" 2>>"$diagnostics" &
watch_pid=$!

git -C "$repo" switch change-two >/dev/null
two_receipt=$("$bin/grd" submit --server "$server" --workdir "$repo")
two_version=$(printf '%s\n' "$two_receipt" | sed -n 's/.*"version":{"id":"\([^"]*\)".*/\1/p')
if [ -z "$two_version" ]; then
	fail 'second Change submit receipt has no Version'
fi

git -C "$repo" switch change-one >/dev/null
one_receipt=$("$bin/grd" submit --server "$server" --workdir "$repo")
one_version=$(printf '%s\n' "$one_receipt" | sed -n 's/.*"version":{"id":"\([^"]*\)".*/\1/p')
if [ -z "$one_version" ]; then
	fail 'first Change submit receipt has no Version'
fi

attempt=0
while :; do
	one_inspection=$("$bin/grd" version --server "$server" --id "$one_version")
	case "$one_inspection" in
		*'"requirements":[{'*) break ;;
	esac
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 100 ]; then
		fail 'first Change did not reach an explicit Requirement'
	fi
	sleep 0.05
done

pending=$("$bin/grd" requirements --server "$server" --limit 100)
case "$pending" in
	*"\"version\":\"$one_version\""*"\"version\":\"$two_version\""*|*"\"version\":\"$two_version\""*"\"version\":\"$one_version\""*) ;;
	*) fail 'assignee Requirement inbox does not contain both held Versions' ;;
esac

one_response=$("$bin/grd" respond \
	--server "$server" \
	--idempotency-key change-one-response \
	--version "$one_version" \
	--policy collaboration \
	--decision satisfied \
	--rationale 'Contribution one preserves the accepted base.')
one_retry=$("$bin/grd" respond \
	--server "$server" \
	--idempotency-key change-one-response \
	--version "$one_version" \
	--policy collaboration \
	--decision satisfied \
	--rationale 'Contribution one preserves the accepted base.')
if [ "$one_response" != "$one_retry" ]; then
	fail 'idempotent Requirement Response retry returned a different receipt'
fi

attempt=0
while :; do
	one_inspection=$("$bin/grd" version --server "$server" --id "$one_version")
	case "$one_inspection" in
		*'"promotion":{'*) break ;;
	esac
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 100 ]; then
		fail 'first Change was not promoted after its Response'
	fi
	sleep 0.05
done

git -C "$repo" switch change-two >/dev/null
sync=$("$bin/grd" sync \
	--server "$server" \
	--workdir "$repo" \
	--version "$two_version" \
	--rationale 'Replay contribution two after contribution one.')
two_rebased=$(printf '%s\n' "$sync" | sed -n 's/.*"toVersion":"\([^"]*\)".*/\1/p')
if [ -z "$two_rebased" ] || [ "$two_rebased" = "$two_version" ]; then
	fail 'sync did not record a replacement Version for the second Change'
fi

attempt=0
while :; do
	two_inspection=$("$bin/grd" version --server "$server" --id "$two_rebased")
	case "$two_inspection" in
		*'"requirements":[{'*) break ;;
	esac
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 100 ]; then
		fail 'rebased contribution two did not reach an explicit Requirement'
	fi
	sleep 0.05
done

two_response=$("$bin/grd" respond \
	--server "$server" \
	--idempotency-key change-two-response \
	--version "$two_rebased" \
	--policy collaboration \
	--decision satisfied \
	--rationale 'Rebased contribution two preserves contribution one.')

attempt=0
while :; do
	two_inspection=$("$bin/grd" version --server "$server" --id "$two_rebased")
	case "$two_inspection" in
		*'"promotion":{'*) break ;;
	esac
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 100 ]; then
		fail 'rebased contribution two was not promoted after its Response'
	fi
	sleep 0.05
done

accepted=$("$bin/grd" intent --server "$server")
rebased_revision=$(git -C "$repo" rev-parse HEAD)
main_revision=$(git -C "$repo" rev-parse refs/heads/main)
if [ "$main_revision" != "$rebased_revision" ]; then
	fail 'Git trunk projection does not identify the final accepted Version'
fi
case "$accepted" in
	*"\"revision\":\"$rebased_revision\""*) ;;
	*) fail 'accepted Intent does not identify the final rebased content' ;;
esac
git -C "$repo" show "$main_revision:contributor-one.txt" >/dev/null
git -C "$repo" show "$main_revision:contributor-two.txt" >/dev/null

status=$("$bin/grd" status --server "$server" --workdir "$repo")
case "$status" in
	*'"relation":"accepted"'*) ;;
	*) fail 'final Change workspace is not accepted' ;;
esac

history=$("$bin/grd" history --server "$server" --limit 100)
for kind in version_proposed evaluation_recorded requirement_responded version_promoted held_version_rebased; do
	case "$history" in
		*"\"kind\":\"$kind\""*) ;;
		*) fail "durable history is missing $kind" ;;
	esac
done

pending=$("$bin/grd" requirements --server "$server" --limit 100)
case "$pending" in
	*'"requirements":[]'*) ;;
	*) fail 'resolved Requirements remain in the assignee inbox' ;;
esac

attempt=0
while [ ! -s "$watch_output" ]; do
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 100 ]; then
		fail 'cursorable history watch emitted no durable facts'
	fi
	sleep 0.05
done

printf '%s\n' \
	"$ready_json" \
	"$two_receipt" \
	"$one_receipt" \
	"$one_response" \
	"$sync" \
	"$two_response" \
	"$accepted" \
	"$status" \
	"$history"
