# GRD

Adaptive version control: a VCS for adaptive apps.

GRD treats acceptance as repository state. Contributors propose immutable
Versions; the repository evaluates them against accepted guidance, derives
Requirements when action is needed, records Responses, and promotes only when
the current evidence permits it.

This first public slice is the engine-neutral kernel:

- Intent, Change, Version, and promotion identity;
- immutable Evaluations, Requirements, and Responses;
- dependency, amendment, and reconciliation mechanics;
- durable-store and content-engine contracts; and
- provider-neutral evaluation scheduling and policy interpretation.

VCS engines own content representation. Evaluators interpret repository
guidance. GRD owns the durable decision history between them.

## Status

This is a kernel snapshot, not yet a runnable distribution. It deliberately
does not include the client, server, Git adapter, persistence adapters, model
providers, deployment configuration, or rehearsal tooling. Packages remain
under `internal/` until their public API has earned a stable shape.

## Development

Requires Go 1.26.5 or newer.

```sh
go test ./...
```
