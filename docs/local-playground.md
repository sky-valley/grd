# Local playground

This walkthrough runs one `grds` server and uses the `grd` client to propose,
evaluate, inspect, and promote a real Git commit. It requires Go and Git. `jq`
is used only to make shell extraction readable.

The included evaluator clears every policy. It proves the GRD plumbing; it is
not a semantic review and must not be used for real acceptance decisions.

## Build the client and server

From the GRD checkout:

```sh
mkdir -p /tmp/grd-bin
go build -o /tmp/grd-bin/grd ./cmd/grd
go build -o /tmp/grd-bin/grds ./cmd/grds
```

## Create an accepted repository

```sh
PLAY=$(mktemp -d /tmp/grd-play.XXXXXX)
STATE=$(mktemp -d /tmp/grd-play-state.XXXXXX)
mkdir -p "$PLAY/.grd"
git -C "$PLAY" init -b main
git -C "$PLAY" config user.name 'GRD Player'
git -C "$PLAY" config user.email 'grd-player@example.invalid'
printf '%s\n' 'Keep this playground understandable.' > "$PLAY/.grd/purpose.md"
printf '%s\n' \
  '## simplicity' \
  'Assignee: principal:playground' \
  'Instruction: Does this keep the playground simple?' \
  > "$PLAY/.grd/priorities.md"
printf '%s\n' '# GRD playground' > "$PLAY/README.md"
git -C "$PLAY" add .
git -C "$PLAY" commit -m 'Initialize accepted Intent'
```

The guidance in `.grd/` belongs to the accepted commit. Candidate changes do
not get to replace the authority used to evaluate themselves.

## Start `grds`

From the GRD checkout, start it in the background so the shell variables above
remain available:

```sh
/tmp/grd-bin/grds \
  --repository playground \
  --git-dir "$PLAY/.git" \
  --ledger "$STATE/ledger.jsonl" \
  --trunk refs/heads/main \
  --evaluator "$PWD/examples/evaluator-always-clear.sh" \
  --listen 127.0.0.1:8787 \
  --producer local:player \
  --poll-interval 100ms \
  > "$STATE/ready.json" 2> "$STATE/grds.log" &
GRDS_PID=$!
while [ ! -s "$STATE/ready.json" ]; do sleep 0.05; done
cat "$STATE/ready.json"
```

`grds` prints one readiness object. `producer` is server-configured provenance,
not a client assertion. The HTTP surface is unauthenticated and loopback-only.

## Propose a candidate

In the same shell:

```sh
SERVER=http://127.0.0.1:8787
BASE=$(/tmp/grd-bin/grd intent --server "$SERVER" | jq -r .intent)

git -C "$PLAY" switch -c experiment
printf '%s\n' 'A proposed line.' >> "$PLAY/README.md"
git -C "$PLAY" add README.md
git -C "$PLAY" commit -m 'Propose a playground change'
REVISION=$(git -C "$PLAY" rev-parse HEAD)

RECEIPT=$(/tmp/grd-bin/grd propose \
  --server "$SERVER" \
  --base-intent "$BASE" \
  --engine git \
  --revision "$REVISION" \
  --idempotency-key player-change-1)
printf '%s\n' "$RECEIPT" | jq
VERSION=$(printf '%s\n' "$RECEIPT" | jq -r .version.id)
```

The candidate branch is important: `main` is GRD's projection of accepted
Intent, so ordinary candidate commits must not advance it directly.

Retrying the same request with `player-change-1` returns the same durable
receipt. Reusing that key for different content fails with a conflict.

## Inspect the judgement

```sh
/tmp/grd-bin/grd version --server "$SERVER" --id "$VERSION" | jq
/tmp/grd-bin/grd intent --server "$SERVER" | jq
git -C "$PLAY" log --oneline --decorate --all --graph
```

Evaluation and Promotion are separate immutable facts. Immediately after
proposal, one or both may be absent; repeat `grd version` to observe them.
After Promotion, accepted Intent and `refs/heads/main` identify the candidate
commit even though the working tree remains on `experiment`.

To see a held Version, copy the evaluator and change `requiresAction` to
`true`. The resulting Requirement appears in `grd version`; this milestone does
not yet expose the Response command needed to resolve it.

## Automated smoke rehearsal

The repository also carries the same journey as a disposable smoke test:

```sh
./scripts/smoke-playground.sh
```

It prints five JSONL facts: readiness, initial Intent, proposal receipt,
Version inspection, and final Intent.

Stop the manual server when finished:

```sh
kill "$GRDS_PID"
wait "$GRDS_PID"
```
