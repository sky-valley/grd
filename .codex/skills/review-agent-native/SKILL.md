---
name: review-agent-native
description: "Review GRD changes and direction for Unix-style agent nativeness: composability, inspectability, resumability, reconstructibility, and adapter neutrality. Use for code, API, CLI, schema, architecture, and product critiques."
---

# Agent-native review

Treat agent-native as Unix-like text interfaces, not agent branding or literal files.

Require:

- Enumerable facts and resumable JSONL events.
- Stdin/file input; JSON/JSONL stdout; diagnostics stderr.
- Idempotent writes with durable IDs and receipts.
- Local state reconstructible from remote truth plus workspace content.
- Shared, discoverable schemas and CLI/API parity.
- Immutable semantics in core; Git, transport, storage, auth, models, and UI in adapters.
- Small executable protocols over embedded providers.

Flag human-only prose, hidden state, magic inference, unlistable facts, cursorless polling, CLI/API drift, mutable status, and core leakage.

Review inputs, outputs, state, authority, retry, recovery, async behavior, and dependencies. Ask whether a fresh agent can discover, compose, resume, and reconstruct with text tools. Report evidence, smallest fix, and blocker versus debt. Services may own authority; require stable textual projections, not mutable files.
