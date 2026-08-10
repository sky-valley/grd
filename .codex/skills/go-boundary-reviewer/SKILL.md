---
name: go-boundary-reviewer
description: Review Go diffs for ownership boundaries, package placement, command binaries, internal package ownership, interfaces, config/secrets scope, persistence and transport boundaries, and lifecycle fit. Use when a Go change adds or moves packages, commands, subcommands, services, maintenance jobs, exported APIs, interfaces, config, persistence, or transport code, or when another review needs a boundary verdict.
---

# Go Boundary Reviewer

Use this skill as a focused ownership-boundary review lens for Go changes. Review only; do not implement unless explicitly asked.

## Workflow

1. Identify every non-trivial new responsibility in the diff.
2. Map each responsibility to its current package, binary, service, transport layer, config layer, persistence layer, or test helper.
3. Decide whether that location owns the responsibility. Check lifecycle, config, secrets, permissions, durability, failure behavior, operator intent, dependency direction, and caller coupling.
4. Emit a boundary verdict for each material responsibility, even when the verdict is "stay".
5. Promote a verdict into a finding when the current location hides lifecycle differences, spreads config/secrets, reverses dependency direction, forces questionable exports, or makes future callers depend on the wrong package.

## Go Boundary Tripwires

- `cmd/`: Long-running services, one-shot maintenance jobs, migrations, repairs, admin tools, and GC jobs usually deserve separate `cmd/<binary>` packages when they have different lifecycle, runtime config, secrets, permissions, or operator intent.
- `internal/*`: Avoid generic packages that hide domain behavior. Prefer an existing domain owner when the behavior belongs to a pipeline, workspace, artifact, queue, or control-plane boundary.
- Interfaces: Prefer consumer-shaped interfaces near the caller. Flag provider-shaped abstractions or exported interfaces added only to cross a questionable package boundary.
- Config and secrets: Flag code that imports broad service config or secrets for a component that only needs local paths or narrow options.
- Transport and persistence: Flag domain behavior pushed into HTTP handlers, queue consumers, CLI glue, migration code, or wire DTOs.
- Tests: Flag tests that reach into internals or duplicate path conventions because production ownership is unclear.

## Boundary Verdict Format

For each material responsibility, include:

- Responsibility:
- Current location:
- Claimed owner:
- Lifecycle/config/secrets/permissions/durability/failure fit:
- Dependency direction:
- Verdict: stay, move package, split binary, split package, keep local helper, or needs evidence
- Severity:
- Smallest fix:

## Output

Return findings first, ordered by severity, with file and line references when possible. Then include the boundary verdicts. If there are no material boundary findings, say that clearly and still include the verdicts.
