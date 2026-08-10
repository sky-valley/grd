# Local control protocol

`grds --listen <address> --producer <subject>` exposes one repository over
HTTP. The address must be a numeric loopback address and port. This first
adapter has no authentication and must not be exposed as a network service.
The server-configured principal is recorded as producer, assignee, reporter, or
resolver on writes as appropriate; clients cannot claim a different identity.

## Accepted Intent

```http
GET /v1/intent
```

The response is one `application/json` value:

```json
{"schema":"grd.intent/v1","repository":"repo_example","intent":"intent_current","previousIntent":"intent_previous","content":{"engine":"git","revision":"0123456789abcdef0123456789abcdef01234567"}}
```

- `repository` is the server's configured opaque repository ID.
- `intent` is the durable accepted Intent ID.
- `previousIntent` is omitted for the initial Intent.
- `content` identifies the exact accepted engine revision.

Clients must reject unknown `schema` revisions. The response is a projection of
durable repository state, not an event or a mutable status record.

`grd intent --server <url>` validates this contract and writes the fact as one
JSON object to stdout. The client accepts only numeric loopback server URLs;
diagnostics go to stderr.

## Propose a Version

```http
POST /v1/proposals
Content-Type: application/json
Idempotency-Key: player-change-1

{"schema":"grd.proposal/v1","baseIntent":"intent_current","content":{"engine":"git","revision":"0123456789abcdef"}}
```

The durable response contains no mutable status:

```json
{"schema":"grd.proposal-receipt/v1","repository":"repo_example","change":{"id":"change_one"},"version":{"id":"version_one","change":"change_one","baseIntent":"intent_current","content":{"engine":"git","revision":"0123456789abcdef"},"producer":"local:player"}}
```

Retries with the same key and logical proposal return the same receipt. Reuse
with different input returns `409 Conflict`. Admission does not imply
Evaluation or Promotion.

The CLI accepts the same schema from a file or standard input:

```sh
printf '%s\n' '{"schema":"grd.proposal/v1","baseIntent":"intent_current","content":{"engine":"git","revision":"0123456789abcdef"}}' |
  grd propose --server http://127.0.0.1:8787 \
    --idempotency-key player-change-1 --input -
```

## Inspect a Version

```http
GET /v1/versions/{version}
```

The `grd.version/v1` response always contains the immutable Version. It adds an
Evaluation, its Requirements and latest Responses, and Promotion when those
durable facts exist. It deliberately contains no mutable state field. `grd version
--server <url> --id <version>` validates and prints the same response.

## Requirement inbox and Responses

```http
GET /v1/requirements?cursor=<opaque>&limit=50
POST /v1/requirement-responses
Content-Type: application/json
Idempotency-Key: response-1
```

`grd.requirements/v1` pages unresolved Requirements assigned to the
server-configured principal. The cursor is opaque and bound to the exact
Version and policy. A Response body uses `grd.requirement-response/v1` and
identifies its Version, policy, `satisfied` or `revision_requested` decision,
and rationale. Identity is not accepted from the body: the server records its
configured principal as assignee.

```json
{"schema":"grd.requirement-response/v1","version":"version_one","policy":"architecture","decision":"satisfied","rationale":"The migration rehearsal passed."}
```

The receipt schema is `grd.requirement-response-receipt/v1`. Retry semantics
match proposals: the same idempotency key and logical Response returns the same
receipt; conflicting reuse returns `409 Conflict`.

## Durable history

```http
GET /v1/history?cursor=<opaque>&limit=50
```

`grd.history/v1` is the cursorable semantic history derived from the durable
ledger. Each fact has an opaque monotonic cursor and one kind-specific payload.
Kinds currently include Intent initialization, Version proposal, Evaluation,
Requirement Response, Promotion, Amendment, dependent reconciliation, held
Version rebase, conflict, and conflict resolution. Journal implementation
records are not exposed.

`grd history` prints one page. `grd watch` repeatedly resumes from the last
cursor and writes one `grd.history-fact/v1` envelope per JSON line; every line
contains repository identity and one fact, so stored or combined streams remain
self-describing. It is a polling projection of durable history, not an
ephemeral event bus, so a client can stop and resume without losing
authoritative facts. Cursors are bound to the ledger's initial Intent and
cannot be replayed against a replacement repository stream.

## Change and reconciliation

```http
GET  /v1/changes/{change}
POST /v1/amendments
POST /v1/held-version-rebases
POST /v1/dependent-reconciliations
POST /v1/reconciliation-conflicts
POST /v1/reconciliation-resolutions
```

Every write requires `application/json`, the matching versioned schema, and an
`Idempotency-Key`. Producers, reporters, and resolvers are supplied by the
server configuration. Receipts preserve the old and replacement Version IDs,
governing Intent, and rationale rather than mutating earlier facts.

The low-level CLI commands are `change`, `amend`, `rebase-held`,
`reconcile-dependent`, `record-conflict`, and `resolve-conflict`. Mutations
accept their JSON body through `--input -` or a bounded file.

## Git workspace projection

`submit`, `status`, and `sync` are local Git adapter commands built on the
control protocol; they are not additional HTTP resources. `submit` proposes
the exact clean `HEAD`. `status` combines Git ancestry with durable history.
`sync` constructs a held Version's rebased content in a detached temporary
worktree, records the replacement through `/v1/held-version-rebases`, and only
then projects the clean contributor workspace. A retry can finish that local
projection when a prior control response was lost.

This milestone assumes the commit objects are already present in the Git
repository hosted by `grds`. It does not yet provide Git object publication,
remote authentication, or a non-loopback server contract.
