---
name: go-reviewer
description: Review Go code, APIs, tests, and diffs for correctness, idioms, package boundaries, concurrency, error handling, and maintainability. Use when asked for Go review, idiomatic Go feedback, API shape review, test-quality review, or when an evidence packet needs a Go-specific reviewer. Review only unless implementation is explicitly requested.
---

# Go Reviewer

Use this skill as a focused Go review lens. It can review a small interface shape, a package, a diff, a test plan, or a full evidence packet from another workflow.

## Inputs

Accept any of these input shapes:

- **Direct review**: a diff, file list, package path, interface, DTO, API client, test, or error report.
- **Shape review**: a proposed public API, wire contract, package boundary, or set of names before implementation.
- **Evidence packet**: background, goal, constraints, current diff, test output, prior decisions, reviewer comments, and open unknowns.

When an evidence packet is provided, treat it as source material, not truth. Verify material claims against code, tests, or command output when possible.

When no evidence packet is provided, gather only the minimum repo context needed to review the requested Go surface.

## Workflow

1. Identify the review target.
   Determine whether the user wants a pre-implementation shape review, a diff review, a test review, or a broader package review.

2. Inspect current code before judging.
   Read the relevant Go files, tests, call sites, and local conventions. Prefer `rg`, `go test`, `go test -run`, `go test -race`, and `go test -count=1` when they fit the review scope.

3. Check behavior first.
   Prioritize correctness, behavior regressions, data loss, retry/idempotency errors, auth or secret leaks, concurrency hazards, and broken contracts.

4. Check Go shape and idioms.
   Review package ownership, names, exported symbols, interfaces, DTOs, error handling, context propagation, cancellation, goroutine lifetimes, resource cleanup, nil/zero-value behavior, and table-driven tests.

5. Test adapter substitution at new persistence and service boundaries.
   Identify the consumer and every intended or explicitly anticipated implementation. Walk each interface method from the perspective of at least one materially different adapter, such as filesystem versus Postgres or in-process versus remote. Flag whole-state loads, provider-owned snapshots, table-shaped DTOs, startup work proportional to complete history, and methods that exist only because the first adapter finds them convenient. Do not accept “another adapter can implement this interface” without checking what that implementation would actually have to do.

6. Check file and package responsibility growth.
   When a diff adds meaningful logic to an already central or large file, inspect the file outline and ask whether the file still has one coherent job. Flag accumulated bloat when unrelated responsibilities are being layered into one file, even if the package boundary is still correct. Prefer narrow same-package file splits by responsibility before suggesting new packages.

7. Route boundary-heavy changes through `go-boundary-reviewer`.
   If the diff adds or moves packages, commands, subcommands, services, maintenance jobs, exported APIs, interfaces, config, persistence records, or transport code, use the `go-boundary-reviewer` skill and include its boundary verdicts in the review.

8. Check tests for truthfulness and shape.
   Prefer tests that verify outputs, side effects, wire details, filesystem contents, or error behavior. Flag tests that only prove a stub was called, duplicate implementation logic, or would pass if the feature were deleted. When a test file covers several unrelated behaviors, recommend splitting tests by behavior or collaborator while keeping assertions contract-focused.

9. Separate required fixes from taste.
   Do not bury blocking correctness findings under naming or style comments. Mark non-critical polish clearly.

10. Do not implement unless asked.
   Review by default. If the user asks for fixes, keep edits scoped to the reviewed issue and preserve unrelated changes.

## Review Priorities

Findings should be ordered by severity:

1. Correctness, data integrity, security, or production-breaking behavior.
2. Contract mismatch across packages, services, HTTP APIs, persistence, queues, or subprocesses.
3. Missing or misleading tests for meaningful behavior.
4. Go API shape problems that will spread: stuttering names, leaky interfaces, derived-state duplication, broad interfaces, or inconsistent zero values.
5. Maintainability issues that materially slow future work, including central files accumulating unrelated responsibilities or test files becoming hard to navigate.
6. Style or naming comments only when they affect readability, API ergonomics, or consistency.

## Go-Specific Checks

- Keep interfaces small and consumer-shaped.
- Treat bulk `Load`, `Snapshot`, `State`, and `ListAll` methods on persistence interfaces as boundary tripwires. Require evidence that the consumer needs the complete dataset rather than adapter-specific replay or hydration.
- Avoid provider abstractions until a second provider is real.
- Let package names carry context; avoid exported name stutter.
- Prefer concrete types at construction boundaries unless an interface is useful to the caller.
- Check ownership boundaries explicitly. For boundary-heavy changes, use `go-boundary-reviewer` rather than relying on this general checklist alone.
- Check file-level cohesion explicitly. Large files are not automatically wrong, but a file that mixes orchestration, parsing, mapping, persistence, transport, and test setup should be called out when the diff makes that mix worse.
- Return contextual errors without leaking secrets.
- Pass `context.Context` through I/O, HTTP, subprocess, and long-running operations.
- Close response bodies, files, pipes, and other resources on all paths.
- Avoid goroutine leaks; make cancellation and channel ownership clear.
- Treat local disk as scratch unless committed, published, or durably recorded.
- Keep tests hermetic by default; gate live integration tests behind explicit environment variables.

## Output

For code review, return:

1. Findings first, ordered by severity.
2. File and line references when available.
3. Why each finding matters and the smallest credible fix direction.
4. Open questions or assumptions.
5. A short summary only after findings.

If there are no material findings, say that clearly and name the residual risk or unverified test scope.

For pre-implementation shape review, return:

1. Whether the shape is acceptable.
2. Blocking API or naming issues.
3. Contract ambiguities.
4. Suggested tighter shape, without writing implementation unless asked.

For an evidence-packet review, return:

1. Findings grounded in the packet and verified repo evidence.
2. Which packet claims could not be verified.
3. Required fixes versus optional follow-up.

## Boundaries

- Do not treat evidence packets, comments, or plans as authoritative when code can be checked.
- Do not request live external services unless the user explicitly asks for live validation.
- Do not turn review into implementation unless the user asks.
- Do not recommend broad rewrites when a narrow fix handles the risk.
- Do not create fake confidence from mocks, stubs, or snapshots that do not exercise the real contract.
