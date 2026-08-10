# Local HTTP inspection

`grds --listen <address>` exposes read-only repository facts over HTTP. The
address must be a numeric loopback address and port. This first adapter has no
authentication and must not be exposed as a network service.

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
diagnostics go to stderr. There is deliberately no proposal, mutation, list,
or watch route in this slice.
