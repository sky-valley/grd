# Host readiness

After Git, the decision ledger, and the evaluator have opened successfully,
`grds` writes exactly one JSON object to stdout. Diagnostics are written to
stderr.

## `grd.host-ready/v1`

```json
{"schema":"grd.host-ready/v1","repository":"repo_example","intent":"intent_...","content":{"engine":"git","revision":"0123456789abcdef0123456789abcdef01234567"},"control":"http://127.0.0.1:49152"}
```

- `repository` is the configured opaque repository ID.
- `intent` is the durable accepted Intent ID recovered from the ledger.
- `content` is the exact accepted content reference verified against Git trunk
  during startup.
- `control` is optional. When present, it is the loopback HTTP server created by
  the explicit `--listen` flag.

Consumers must reject unknown schema revisions. This is a one-shot readiness
receipt, not a resumable event feed. Durable repository facts remain in the
decision ledger; a future list or watch interface must project them from there.
