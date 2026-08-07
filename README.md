# GRD

Adaptive version control: a VCS for adaptive apps.

GRD treats acceptance as repository state. Contributors propose immutable
Versions; the repository evaluates them against accepted guidance, derives
Requirements when action is needed, records Responses, and promotes only when
the current evidence permits it.

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

VCS engines own content representation. Evaluators interpret repository
guidance. GRD owns the durable decision history between them.

## Status

This is a kernel and local-ledger snapshot, not yet a runnable distribution.
It deliberately does not include the client, server, Git adapter, hosted
control-store adapters, model providers, deployment configuration, or
rehearsal tooling. Packages remain under `internal/` until their public API has
earned a stable shape.

The filesystem ledger is single-host, single-process storage. It replays its
history into memory at open and is not a substitute for shared or distributed
persistence. File locking is currently supported on operating systems with
`flock(2)`; unsupported systems fail before creating the journal.

## Development

Requires Go 1.26.5 or newer.

```sh
go test ./...
```
