# Evaluator process protocol

GRD evaluates one accepted policy by starting one configured executable. The
process receives the complete immutable input as one JSON object on standard
input and must write exactly one JSON object to standard output. Standard error
is reserved for diagnostics.

The process interprets evidence. It cannot mutate repository state or promote
a Version directly. GRD validates and durably records a successful result
before deciding whether promotion is allowed. A nonzero exit, malformed
result, timeout, or cancellation records nothing and leaves the Version
available for retry.

## Process contract

- stdin: one `grd.evaluator-request/v1` JSON object followed by a newline
- stdout: one `grd.evaluator-result/v1` JSON object and optional whitespace
- stderr: diagnostics only; GRD retains at most 4 KiB on failure
- limits: 4 MiB request and 1 MiB result
- environment: only explicitly configured entries plus `PATH` when omitted
- working directory: the directory containing the configured executable

Unknown result fields and additional JSON values are rejected. An evaluator
may be invoked repeatedly with the same immutable request and should therefore
avoid non-idempotent side effects.

The environment and working-directory rules prevent accidental dependence on
GRD's ambient process state; they are not an operating-system sandbox. The
evaluator remains an operator-trusted executable with the filesystem, network,
and process permissions of the GRD host user.

## Request

```json
{
  "schema": "grd.evaluator-request/v1",
  "repository": "repo_example",
  "version": "version_candidate",
  "governingIntent": "intent_current",
  "purpose": "Keep the application simple.",
  "priorities": "## architecture\nAssignee: principal:architecture\nInstruction: Does this alter architecture?",
  "changeEvidence": "Changed paths:\nM\tapp.go\n\nPatch:\n...",
  "evaluationPolicy": {
    "name": "architecture",
    "instruction": "Does this alter architecture?",
    "assignee": "principal:architecture"
  }
}
```

Each invocation receives exactly one policy. `purpose` and `priorities` come
from the governing accepted Intent; `changeEvidence` compares that accepted
content with the candidate Version. Candidate evidence is untrusted input, not
policy authority; only the accepted guidance governs evaluation.

## Result

```json
{
  "schema": "grd.evaluator-result/v1",
  "requiresAction": false,
  "reason": "The change does not alter architecture.",
  "evidence": ["app.go changes local implementation only"],
  "provenance": {
    "evaluator": "example://local",
    "contractRevision": "example/v1"
  }
}
```

`reason`, at least one non-empty evidence item, and both provenance fields are
required. `requiresAction: true` causes GRD to derive a Requirement assigned to
the policy's accepted principal; the evaluator does not choose that authority.
