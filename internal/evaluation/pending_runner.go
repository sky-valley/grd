package evaluation

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sky-valley/grd/internal/intent"
)

type WorkItem struct {
	RepoID    string
	VersionID intent.VersionID
}

type PendingPage struct {
	Items      []WorkItem
	NextCursor string
}

type PendingSource interface {
	ListPending(ctx context.Context, after string, limit int) (PendingPage, error)
}

type PendingProcessor interface {
	// Process may be called more than once for the same WorkItem after retries,
	// restarts, or competing runners. Durable effects must be idempotent.
	Process(ctx context.Context, item WorkItem) error
}

type RunnerOptions struct {
	Workers         int
	BatchSize       int
	PollInterval    time.Duration
	LeaseTTL        time.Duration
	RetryBackoff    time.Duration
	MaxRetryBackoff time.Duration
	Report          func(error)
}

type PendingRunner struct {
	source       PendingSource
	leases       LeaseStore
	processor    PendingProcessor
	workers      int
	batchSize    int
	pollInterval time.Duration
	leaseTTL     time.Duration
	retryBackoff time.Duration
	retryMax     time.Duration
	report       func(error)
	owner        string
	retryMu      sync.Mutex
	retries      map[WorkKey]retryState
}

type retryState struct {
	failures    int
	nextAttempt time.Time
	forgetAfter time.Time
}

type claimedWork struct {
	item  WorkItem
	lease Lease
}

type workRequest struct {
	item WorkItem
	done chan struct{}
}

var runnerSequence atomic.Uint64

func NewPendingRunner(source PendingSource, leases LeaseStore, processor PendingProcessor, options RunnerOptions) *PendingRunner {
	workers := max(options.Workers, 1)
	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = workers
	}
	pollInterval := options.PollInterval
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	leaseTTL := options.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = 5 * time.Minute
	}
	retryBackoff := options.RetryBackoff
	if retryBackoff <= 0 {
		retryBackoff = 5 * time.Second
	}
	retryMax := options.MaxRetryBackoff
	if retryMax <= 0 {
		retryMax = 5 * time.Minute
	}
	if retryMax < retryBackoff {
		retryMax = retryBackoff
	}
	report := options.Report
	if report == nil {
		report = func(error) {}
	}
	return &PendingRunner{
		source:       source,
		leases:       leases,
		processor:    processor,
		workers:      workers,
		batchSize:    batchSize,
		pollInterval: pollInterval,
		leaseTTL:     leaseTTL,
		retryBackoff: retryBackoff,
		retryMax:     retryMax,
		report:       report,
		owner:        fmt.Sprintf("pending-runner-%d", runnerSequence.Add(1)),
		retries:      make(map[WorkKey]retryState),
	}
}

func (runner *PendingRunner) Run(ctx context.Context) {
	// An unbuffered handoff ensures a lease is acquired only by a worker that
	// can begin processing immediately. Pending work remains the durable queue.
	work := make(chan workRequest)
	var workers sync.WaitGroup
	for range runner.workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for request := range work {
				runner.process(ctx, request.item)
				close(request.done)
			}
		}()
	}

	cursor := ""
	for ctx.Err() == nil {
		page, err := runner.source.ListPending(ctx, cursor, runner.batchSize)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				runner.report(fmt.Errorf("list pending evaluation: %w", err))
			}
			if !waitFor(ctx, runner.pollInterval) {
				break
			}
			continue
		}
		runner.pruneRetries(time.Now())

		completed := make([]<-chan struct{}, 0, len(page.Items))
		for _, item := range page.Items {
			if !runner.retryReady(item, time.Now()) {
				continue
			}
			done := make(chan struct{})
			select {
			case work <- workRequest{item: item, done: done}:
				completed = append(completed, done)
			case <-ctx.Done():
			}
		}
		for _, done := range completed {
			<-done
		}

		cursor = page.NextCursor
		if cursor == "" && !waitFor(ctx, runner.pollInterval) {
			break
		}
	}
	close(work)
	workers.Wait()
}

func (runner *PendingRunner) process(ctx context.Context, item WorkItem) {
	lease, acquired, err := runner.leases.Acquire(ctx, WorkKey{RepoID: item.RepoID, VersionID: item.VersionID}, runner.owner, runner.leaseTTL)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			runner.report(fmt.Errorf("claim pending evaluation: %w", err))
		}
		return
	}
	if !acquired {
		return
	}
	claimed := claimedWork{item: item, lease: lease}
	processCtx, cancelProcess := context.WithCancel(ctx)
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		interval := runner.leaseTTL / 3
		if interval <= 0 {
			interval = time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-processCtx.Done():
				return
			case <-ticker.C:
				if err := runner.leases.Renew(processCtx, claimed.lease, runner.leaseTTL); err != nil {
					if !errors.Is(err, context.Canceled) {
						runner.report(fmt.Errorf("renew evaluation lease: %w", err))
					}
					cancelProcess()
					return
				}
			}
		}
	}()

	err = runner.processor.Process(processCtx, claimed.item)
	cancelProcess()
	<-renewDone
	if err != nil && !errors.Is(err, context.Canceled) {
		runner.noteFailure(claimed.item, time.Now())
		runner.report(fmt.Errorf("process pending %s/%s: %w", claimed.item.RepoID, claimed.item.VersionID, err))
	} else if err == nil {
		runner.clearRetry(claimed.item)
	}
	runner.release(claimed.lease)
}

func (runner *PendingRunner) retryReady(item WorkItem, now time.Time) bool {
	runner.retryMu.Lock()
	defer runner.retryMu.Unlock()
	state, found := runner.retries[workKey(item)]
	return !found || !now.Before(state.nextAttempt)
}

func (runner *PendingRunner) noteFailure(item WorkItem, now time.Time) {
	runner.retryMu.Lock()
	defer runner.retryMu.Unlock()
	key := workKey(item)
	state := runner.retries[key]
	state.failures++
	delay := runner.retryBackoff
	for attempt := 1; attempt < state.failures && delay < runner.retryMax; attempt++ {
		if delay > runner.retryMax/2 {
			delay = runner.retryMax
			break
		}
		delay *= 2
	}
	if delay > runner.retryMax {
		delay = runner.retryMax
	}
	state.nextAttempt = now.Add(delay)
	state.forgetAfter = state.nextAttempt.Add(runner.retryMax)
	runner.retries[key] = state
}

func (runner *PendingRunner) clearRetry(item WorkItem) {
	runner.retryMu.Lock()
	defer runner.retryMu.Unlock()
	delete(runner.retries, workKey(item))
}

func (runner *PendingRunner) pruneRetries(now time.Time) {
	runner.retryMu.Lock()
	defer runner.retryMu.Unlock()
	for key, state := range runner.retries {
		if !now.Before(state.forgetAfter) {
			delete(runner.retries, key)
		}
	}
}

func workKey(item WorkItem) WorkKey {
	return WorkKey{RepoID: item.RepoID, VersionID: item.VersionID}
}

func (runner *PendingRunner) release(lease Lease) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runner.leases.Release(ctx, lease); err != nil && !errors.Is(err, ErrLeaseLost) {
		runner.report(fmt.Errorf("release evaluation lease: %w", err))
	}
}

func waitFor(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
