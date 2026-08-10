# Local HTTP inspection

`grds --listen <address> --producer <subject>` exposes one repository over
HTTP. The address must be a numeric loopback address and port. This first
adapter has no authentication and must not be exposed as a network service.
The server-configured producer is recorded on every admitted Version; clients
cannot claim a different producer.

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
