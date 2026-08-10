# GRD

**Adaptive version control for adaptive apps.**

GRD (pronounced “grid”) makes acceptance part of repository state. Instead of
forcing every change through the same fixed workflow, it evaluates the exact
proposed Version against guidance already accepted by the repository and
decides what evidence or action is needed next.

```text
propose → evaluate → respond or revise → promote → reconcile
```

A contributor proposes an immutable commit. GRD evaluates it, promotes it when
the evidence permits, or creates a Requirement for a person or agent to answer.
Every Evaluation, Requirement, Response, and Promotion becomes durable history.
Concurrent work can then be reconciled against the newly accepted Intent
without losing how the decision was made.

Git is the first content engine, but the decision model is not Git-specific.
Evaluators are ordinary external commands and are not tied to a model provider.
The deeper product and architecture principles live in the
[vision](docs/vision.md).

## Try it

The fastest way to see the current loop is the disposable local rehearsal:

```sh
./scripts/smoke-adaptive-loop.sh
```

It creates a real Git repository, starts `grds`, submits two concurrent Changes,
holds one for a Requirement, records a Response, promotes both in turn, and
reconciles the stale workspace. Everything runs locally under one configured
principal. See the [local playground](docs/local-playground.md) for the story
and expected output.

Requires Go 1.26.5 or newer, Git on `PATH`, and a POSIX shell.

## What works today

- `grds` hosts one existing Git repository with a durable append-only ledger.
- `grd` can submit work and inspect Intent, Versions, Changes, and workspace
  status.
- Requirements and idempotent Responses provide the human-or-agent action loop.
- Durable history can be listed or watched using resumable cursors.
- Exact commits are evaluated by an external, provider-neutral command.
- Promotion advances trunk atomically and recovers safely across restarts.
- Held work can be reconciled onto current Intent with `grd sync`.

The settled vocabulary is small: **Intent** is the accepted repository state;
a **Change** is a line of proposed work; a **Version** is one exact candidate;
an **Evaluation** records judgement; a **Requirement** asks for action; a
**Response** answers it; **Promotion** accepts a Version; and
**reconciliation** adapts held work to newer Intent.

## Run one repository

The accepted commit must already contain `.grd/purpose.md` and
`.grd/priorities.md`, and candidate commit objects must exist in the server's
Git object database. Start the server with an external evaluator:

```sh
go run ./cmd/grds \
  --repository repo_example \
  --git-dir /path/to/repository/.git \
  --ledger /path/to/grd/decision-loop.jsonl \
  --trunk refs/heads/main \
  --evaluator /path/to/evaluator \
  --listen 127.0.0.1:0 \
  --producer principal:player
```

On startup, `grds` writes one `grd.host-ready/v1` JSON object to stdout. Its
`control` field contains the chosen local URL. Use that URL with the client:

```sh
go run ./cmd/grd intent --server http://127.0.0.1:12345
go run ./cmd/grd status --server http://127.0.0.1:12345
go run ./cmd/grd submit --server http://127.0.0.1:12345
go run ./cmd/grd requirements --server http://127.0.0.1:12345
go run ./cmd/grd history --server http://127.0.0.1:12345
```

Use repeatable `--evaluator-env NAME` flags to forward named environment
variables in addition to `PATH`. `--listen` accepts only a numeric loopback
address; port `0` chooses a free port. SIGINT and SIGTERM stop the server and
workers cleanly.

Protocol details:

- [evaluator protocol](docs/evaluator-protocol.md)
- [local control protocol](docs/control-http.md)
- [host readiness receipt](docs/host-ready.md)

## Current limits

This is a local, single-repository milestone—not yet a networked distribution.
It supports concurrent Changes, but all local writes currently use one
configured principal. The control endpoint is loopback-only and unauthenticated.
GRD does not yet transfer Git objects, provide shared or hosted persistence,
establish network identity and authorization, ship model-provider adapters,
support persistent divergent personal versions, or offer a polished human
interface.

The filesystem ledger is single-host, single-process storage and replays its
history into memory when opened. File locking requires an operating system with
`flock(2)`. More complex reconciliation remains available through lower-level
commands while the common held-Version path is developed into the primary user
experience.

## Architecture

GRD core owns the adaptive decision loop and its durable history. Content
engines, persistence, transports, evaluators, and contributor workspaces are
adapters. That boundary keeps repository judgement portable while allowing Git,
local files, HTTP, and executable evaluators to serve the current milestone.

See the [vision](docs/vision.md) for the north star and the protocol documents
above for current wire contracts.

## Development

```sh
go test ./...
```
