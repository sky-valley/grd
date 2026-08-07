// Package ledgerfs implements the GRD decision ledger as an append-only JSONL
// journal on a durable local filesystem.
//
// A journal supports one process and one writer at a time, enforced by a
// non-blocking operating-system file lock. Each accepted record is synced
// before it affects the in-memory projection. Opening a journal repairs an
// incomplete final record and then validates and replays the complete history.
//
// The adapter is intended for a single GRD repository on one host. It is not
// shared or distributed storage. File locking is currently supported on
// operating systems with flock(2); Open returns an error without creating the
// journal on unsupported systems.
package ledgerfs
