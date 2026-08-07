package evaluation

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sky-valley/grd/internal/intent"
)

var ErrLeaseLost = errors.New("evaluation lease is no longer owned")

type WorkKey struct {
	RepoID    string
	VersionID intent.VersionID
}

type Lease struct {
	Key        WorkKey
	Owner      string
	Generation uint64
	ExpiresAt  time.Time
}

type LeaseStore interface {
	Acquire(ctx context.Context, key WorkKey, owner string, ttl time.Duration) (Lease, bool, error)
	Renew(ctx context.Context, lease Lease, ttl time.Duration) error
	Release(ctx context.Context, lease Lease) error
}

type MemoryLeases struct {
	mu          sync.Mutex
	now         func() time.Time
	leases      map[WorkKey]Lease
	generations map[WorkKey]uint64
}

func NewMemoryLeases(now func() time.Time) *MemoryLeases {
	if now == nil {
		now = time.Now
	}
	return &MemoryLeases{
		now:         now,
		leases:      make(map[WorkKey]Lease),
		generations: make(map[WorkKey]uint64),
	}
}

func (leases *MemoryLeases) Acquire(ctx context.Context, key WorkKey, owner string, ttl time.Duration) (Lease, bool, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, false, err
	}
	if key.RepoID == "" || key.VersionID == "" || owner == "" || ttl <= 0 {
		return Lease{}, false, errors.New("evaluation lease requires work identity, owner, and positive ttl")
	}

	leases.mu.Lock()
	defer leases.mu.Unlock()
	now := leases.now()
	if current, found := leases.leases[key]; found && current.ExpiresAt.After(now) {
		return Lease{}, false, nil
	}
	generation := leases.generations[key] + 1
	lease := Lease{
		Key:        key,
		Owner:      owner,
		Generation: generation,
		ExpiresAt:  now.Add(ttl),
	}
	leases.generations[key] = generation
	leases.leases[key] = lease
	return lease, true, nil
}

func (leases *MemoryLeases) Renew(ctx context.Context, lease Lease, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ttl <= 0 {
		return errors.New("evaluation lease renewal requires a positive ttl")
	}

	leases.mu.Lock()
	defer leases.mu.Unlock()
	now := leases.now()
	current, found := leases.leases[lease.Key]
	if !found || current.Owner != lease.Owner || current.Generation != lease.Generation || !current.ExpiresAt.After(now) {
		return ErrLeaseLost
	}
	current.ExpiresAt = now.Add(ttl)
	leases.leases[lease.Key] = current
	return nil
}

func (leases *MemoryLeases) Release(ctx context.Context, lease Lease) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	leases.mu.Lock()
	defer leases.mu.Unlock()
	current, found := leases.leases[lease.Key]
	if !found || current.Owner != lease.Owner || current.Generation != lease.Generation {
		return ErrLeaseLost
	}
	delete(leases.leases, lease.Key)
	return nil
}
