---
name: baby-steps
description: Red-green, contract-first development workflow for implementing code in small truthful steps. Use when adding behavior, fixing bugs, writing tests, implementing API clients, integrating services, or when the user asks for baby steps, red-green, TDD, truthful tests, honest tests, non-performative tests, or tests that should fail before implementation. Stops before committing unless explicitly asked.
---

# Baby Steps

Use this skill to move from unknown behavior to verified behavior without fake confidence. Work in the smallest meaningful loop: define the contract, inspect the seam, make it red for the right reason, make it green with the smallest useful implementation, then check that the test would catch a real regression.

## Workflow

1. State the behavior contract.
   Identify the input, output, side effect, boundary, and failure mode being tested. If the contract is unclear, inspect the code or authoritative docs before writing the test.

2. Inspect the current seam.
   Read the relevant code before designing. Identify the owning module, existing patterns, and the smallest place the behavior should live.

3. Shape the narrow API when crossing a boundary.
   If the change introduces or alters an interface, DTO, wire shape, persistence record, protocol, or cross-package contract, sketch the smallest public shape before implementation. Avoid speculative abstraction.

4. Pick the narrowest valuable test.
   Prefer a test that exercises real logic. Use fake implementations only at true process boundaries, such as HTTP servers, filesystem temp dirs, subprocess wrappers, or token sources.

5. Write the red test first when behavior is not implemented.
   The test should fail because the behavior is missing or wrong, not because setup is broken. Avoid broad suites until the narrow test proves the intended failure.

6. Run the narrow test and read the failure.
   Confirm the failure message points at the contract. If it fails for a setup mistake, fix the test setup before touching production code.

7. Implement the smallest useful change.
   Make the behavior pass without adding speculative abstractions, extra providers, or unrelated cleanup.

8. Run the narrow test again.
   Confirm it is green. If it passes without exercising the new behavior, strengthen the assertion instead of accepting the result.

9. Check the API shape.
   Before widening the test run, ask whether the implementation stores derived state, exposes construction details, or makes callers reconstruct internal conventions. Tighten the public API before the shape spreads.

10. Run the relevant wider suite.
   Use package-level tests first, then repository-level tests when the change crosses packages or shared behavior.

11. Do an honesty pass.
   Ask: would this test fail if the real behavior broke? Does it assert outputs, side effects, error handling, or integration contract instead of only proving a stub was called?

12. Stop before committing unless asked.
   Report the verified behavior, files changed, tests run, and any skipped verification. Do not commit changes unless the user explicitly requests a commit.

## Scope And Repository Boundaries

- Treat the active repository as the implementation boundary unless the user explicitly widens scope.
- If a seam points into another repository, inspect the other repository read-only only when needed to understand the contract. Do not edit, commit, deploy, or reconfigure the other repository without asking first.
- Before crossing a repository boundary, state which repository owns the other side of the seam, why it is relevant, what can be done inside the active repository, and what would require explicit permission.
- Shape cross-repository contracts from the active side first when possible: DTOs, client behavior, interface seams, fakes, contract tests, or scratch plans.
- Do not convert "the other side owns the real endpoint" into permission to implement that side. Repository ownership and user scope override architectural convenience.

## Test Quality Rules

- Prefer contract tests over implementation-detail tests.
- Prefer `httptest` servers for HTTP clients instead of live services.
- Gate live integration tests behind explicit env vars.
- Verify status codes, request paths, headers, request bodies, response parsing, and error behavior for API clients.
- Verify filesystem outputs and actual checked-out contents for git/workspace behavior.
- Verify subprocess environment and arguments when they are the contract.
- Avoid tests that only assert a mock or stub was called.
- Avoid tests that would still pass if the real feature were deleted.

## API Shape Check

Use this check before accepting green. The first green implementation often reveals whether the public surface is too leaky.

- Store source state, not every deterministic derived value.
- Expose derived values through behavior when that hides implementation details.
- Keep constants private unless callers truly need them as part of the contract.
- Avoid tests that require callers to reconstruct internal conventions.
- If a test asserts many fields on one struct, ask whether those should be methods, helpers, or a smaller value object.
- If two fields can become inconsistent because one is derived from the other, prefer deriving it inside the owning module.

## Red-Green Discipline

When a test is expected to be red, run it and record the reason mentally before implementing. The red phase is only useful if the failure proves the test is aimed at the intended contract.

When making green, keep the change direct. Do not use the green phase to redesign adjacent modules unless the failing contract requires it.

When behavior depends on an external service, make the local test model the service contract honestly. Use live tests only as explicit confirmation, not as the regular safety net.

## Final Report

In the final answer, include:

- the behavior covered
- the files changed
- the test commands run
- any live test gating or skipped verification
