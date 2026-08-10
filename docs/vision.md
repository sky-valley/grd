# GRD: Adaptive Version Control

## Authority

This is GRD's product constitution. Read it before making product or
architecture decisions. Implementations are evidence, not authority.

Settled principles may change only explicitly. Open questions must not be
resolved by silently weakening the mission.

## Mission

**Create adaptive version control.**

GRD is a modular version-control system in which Changes become accepted Intent
through Evaluation.

A repository evaluates each proposed Version against accepted guidance, records
the Evaluation and any Requirements or Responses, and advances accepted Intent
only through explicit Promotion.

Git, other VCS engines, transports, storage systems, model providers, CI tools,
and user interfaces may participate. None defines the product.

## Problem

Branches, pull requests, ownership files, protection settings, and static CI
force contributors to predict a change's consequences in advance. They give
different changes the same ceremony and express developer priorities as
exhaustive rules.

A typo and a data-model migration should not automatically receive the same
treatment. The repository should choose proportionate evidence and action from
its purpose, priorities, accepted state, and the actual change.

## Native loop

> propose → evaluate → respond or revise → promote → reconcile

A contributor proposes a Change and keeps working. The repository evaluates an
immutable Version. Evaluation may promote it, gather evidence, derive a
Requirement, or cause a replacement Version to be proposed. Repository-side
evolution returns to continuing work through reconciliation.

The proposal does not prescribe its ceremony. The repository determines the
consequence.

## Non-negotiable principles

### Acceptance is repository state

Intent is the repository's accepted content. Trunk is a VCS projection of that
Intent, not the source of truth. Nothing becomes accepted accidentally.

### Evaluation is native

Every proposed Version is evaluated before Promotion. Evaluation is not a bot
comment added after another system has already decided acceptance.

### Authority comes from accepted Intent

Purpose, priorities, and operating guidance come from the Version's governing
accepted Intent. Candidate content cannot authorize itself by changing the
guidance used to evaluate that same candidate.

Authentication, authorization, resource limits, and fail-safe behavior remain
deterministic authority boundaries.

### Facts are exact and immutable

An Evaluation concerns one exact Version under one governing Intent. A
replacement Version receives a new Evaluation. Requirements and Responses do
not transfer silently. Earlier facts remain history.

### Different changes deserve different consequences

Immediate Promotion is a valid positive outcome. So are deeper inspection,
simulation, notification, a Requirement, amendment, or rejection. Fixed
ceremony is not the product language.

### Contributors keep working

Proposal must not require waiting for integration. Dependencies and
reconciliation preserve how continuing work relates to held, amended, or
promoted Versions.

### Outcomes are explainable

GRD must answer what happened, why, under whose authority, against which
Version and governing Intent, using what evidence, and what happened next.

### Modularity preserves semantics

GRD owns adaptive repository semantics. VCS, storage, transport, auth,
evaluators, CI, notifications, and UI remain honest capability-aware adapters.
Modularity must not reduce the core to the lowest common denominator or let the
first adapter dictate its vocabulary.

### Compatibility serves the mission

Preserve compatibility wherever it does not weaken adaptive version control.
When compatibility cannot represent native identity, authority, or
reconciliation honestly, expose a reduced experience or use a richer protocol.

### Interfaces are agent-native

Authoritative facts must be discoverable, inspectable, composable, retryable,
and resumable through stable textual interfaces. Durable IDs and receipts
outlive sessions. No essential truth may exist only in a UI or hidden process
state.

## Domain spine

- **Repository** holds accepted Intent, guidance, authority, and history.
- **Intent** is accepted repository content and forms one ordered spine.
- **Change** is a durable proposed evolution of Intent.
- **Version** is one immutable realization of a Change.
- **Evaluation** interprets one Version under its governing Intent.
- **Requirement** names action or authority needed before Promotion.
- **Response** is an immutable answer from the assigned principal.
- **Promotion** advances Intent from one accepted state to another.
- **Evidence** supports Evaluation without becoming authority by itself.
- **Reconciliation** returns repository-side evolution to continuing work while
  preserving lineage and rationale.

IDs are machinery. Human interfaces should emphasize meaning, rationale, and
relationships rather than require users to manipulate opaque identifiers.

## Adaptive evaluation

Evaluation may combine repository guidance, deterministic tools, specialist
review, simulations, historical evidence, agents, and people. Triage selects
proportionate capabilities in response to significance and uncertainty; it is
not a generated static pipeline.

Evidence gathered against stale accepted state may need refreshing. Mutation
safety and semantic acceptance are separate: a compare-and-swap can protect a
ref without proving that the resulting content is acceptable.

Promotion rechecks authority at its durable boundary. Worker discovery,
leases, queues, or mutable status are never the final authority.

## Reconciliation and provenance

Repository amendment or rebase must create new immutable facts rather than
erase submitted work. GRD preserves who produced each Version, what changed,
why it changed, and how dependent work relates to it.

VCS operations construct and project content. GRD records acceptance,
evaluation, requirements, responses, and rationale. Neither history should be
flattened into the other.

## Divergence

Temporary divergence is a Change proposing to advance one repository's Intent.
Persistent divergence owns an accepted Intent lineage of its own.

One repository has one accepted Intent spine and may have many concurrent
Changes around it. A persistent divergence tracks the source Intent it has
considered and may incorporate, amend, hold, or intentionally exclude later
source movement. “Up to date” means considered, not necessarily tree-equal.

VCS forks or shared object storage may project divergence; they do not define
its identity.

## Boundaries

The core owns repository Intent, Changes, Versions, Evaluations, Requirements,
Responses, Promotion, dependencies, and reconciliation semantics.

Adapters own VCS representation, persistence, transport, authentication,
evaluator execution, model choice, CI execution, notifications, and UI.

Calling products own their users, privacy, deployment, runtime state,
commercial policy, and application lifecycle. Such concepts may cross GRD only
as opaque identity or metadata when required.

## Open questions

- What is the native proposal interaction?
- How is Change continuity represented across different VCS clients?
- Which capabilities belong in rich VCS engines versus GRD's ledger?
- How does triage choose evidence and know when to stop?
- How is final combined content reevaluated under concurrency?
- How are amendments and conflicts reconciled into continuing work?
- Which durable history is permanent, and what content may be collected?
- How are persistent divergences created, refreshed, and intentionally held?

These are design work, not permission to dilute the mission.

## Drift signals

Reject or challenge designs that:

- make Git refs, branches, pull requests, or pipelines the native model;
- replace contextual Evaluation with fixed acceptance workflows;
- let candidates alter their own governing authority;
- store mutable status instead of deriving it from durable facts;
- hide essential state in a process, provider, UI, or operator convention;
- couple repository semantics to one VCS, store, transport, model, or caller;
- erase provenance during amendment, retry, or reconciliation; or
- preserve compatibility by lying about what the system guarantees.

The recurring question is:

> Does this help a repository evaluate proposed change and turn it into
> accepted Intent while preserving authority, history, and room to evolve?
