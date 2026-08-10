# GRD

Adaptive version control: a VCS for adaptive apps.

GRD treats acceptance as repository state. Contributors propose immutable
Versions; the repository evaluates them against accepted guidance, derives
Requirements when action is needed, records Responses, and promotes only when
the current evidence permits it.

Read the canonical [product vision](docs/vision.md) before making product or
architecture decisions.

This public foundation contains the engine-neutral kernel:

- Intent, Change, Version, and promotion identity;
- immutable Evaluations, Requirements, and Responses;
- dependency, amendment, and reconciliation mechanics;
- durable-store and content-engine contracts; and
- provider-neutral evaluation scheduling and policy interpretation.

It also includes `internal/ledgerfs`, a durable local adapter that records the
repository decision history as an exclusively locked, append-only JSONL
journal. It syncs each accepted event before applying it in memory and
validates the complete history during restart replay.

`internal/gitengine` is the first content-engine adapter. It pins each admitted
Version's commit under private `refs/grd/versions/` refs, keeps that namespace
hidden from Git clients, and advances trunk with Git's atomic compare-and-swap
ref update. The repository can therefore reconcile an interrupted promotion
across both the durable ledger and the Git projection after restart.
Opening the adapter persists the private-ref rule in local Git configuration
and refuses configurations that could override it.

`internal/gitreader` supplies the read side of the Git boundary. It reads
accepted UTF-8 guidance from exact commits and produces bounded, deterministic
change evidence without external diff drivers or text conversion.

`internal/evaluatorprotocol` and `internal/evaluatorexec` provide the first
real evaluator boundary: one versioned JSON request on stdin and one versioned
JSON result on stdout. Evaluators are ordinary external commands, receive only
an explicitly configured environment plus `PATH`, and remain independent of
model or provider choice. The wire contract is documented in
[`docs/evaluator-protocol.md`](docs/evaluator-protocol.md).

`grds` is the first runtime composition. It hosts one existing Git repository,
opens its durable filesystem ledger, evaluates pending Versions through an
external evaluator, and reconciles atomic trunk promotion across restarts. The
command keeps repository identity opaque and forwards evaluator environment
variables only when named explicitly; the evaluator adapter also supplies
`PATH` when it was not named.

`internal/controlhttp` is the first transport adapter. When explicitly enabled,
`grds` exposes the accepted Intent of its one repository over a loopback-only
HTTP endpoint. The `grd intent` client validates that versioned fact and writes
one JSON object to stdout. The contract is documented in
[`docs/control-http.md`](docs/control-http.md).

VCS engines own content representation. Evaluators interpret repository
guidance. GRD owns the durable decision history between them.

## Status

This is a locally inspectable single-repository composition milestone, not yet
an operable contribution workflow or networked distribution. It can recover
and process pending Versions already present in its ledger and report accepted
Intent, but it has no supported proposal, list, or watch interface yet. Its
HTTP endpoint is deliberately restricted to loopback and has no authentication.
It does not include hosted control-store adapters, model providers, deployment
configuration, or rehearsal tooling. Packages remain under `internal/` until
their public API has earned a stable shape.

The filesystem ledger is single-host, single-process storage. It replays its
history into memory at open and is not a substitute for shared or distributed
persistence. File locking is currently supported on operating systems with
`flock(2)`; unsupported systems fail before creating the journal.

## Development

Requires Go 1.26.5 or newer. Git-backed tests also require `git` on `PATH`.
Executable integration tests use a POSIX shell where supported.

```sh
go test ./...
```

## Run one repository

The Git branch must already exist, and its accepted commit must contain
`.grd/purpose.md` and `.grd/priorities.md`. The evaluator follows the external
JSON contract linked above.

```sh
go run ./cmd/grds \
  --repository repo_example \
  --git-dir /path/to/repository/.git \
  --ledger /path/to/grd/decision-loop.jsonl \
  --trunk refs/heads/main \
  --evaluator /path/to/evaluator \
  --listen 127.0.0.1:0
```

Use repeatable `--evaluator-env NAME` flags to forward named environment
variables in addition to the evaluator's default `PATH`. `--listen` is
optional and accepts only a numeric loopback address and port; port `0` asks
the operating system to choose a free port. On successful startup, `grds`
writes one `grd.host-ready/v1` JSON object to stdout; when local HTTP is enabled,
its `control` field contains the chosen server URL. Runtime diagnostics go to
stderr. SIGINT and SIGTERM stop the HTTP server and evaluator workers and
release the ledger lock. The readiness schema is documented in
[`docs/host-ready.md`](docs/host-ready.md).

Inspect accepted Intent using the advertised URL:

```sh
go run ./cmd/grd intent --server http://127.0.0.1:12345
```
