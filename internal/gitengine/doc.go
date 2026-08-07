// Package gitengine adapts a Git repository to GRD's engine-neutral content
// admission and trunk projection contracts.
//
// Admitted Version content is pinned by immutable private refs so Git object
// collection cannot discard it. Trunk advances use Git's compare-and-swap ref
// update, allowing the GRD repository to detect and reconcile concurrent or
// interrupted projection changes. Open persists the private-ref rule in local
// Git config and fails closed when effective included or program-specific
// configuration could expose any part of the private namespace.
package gitengine
