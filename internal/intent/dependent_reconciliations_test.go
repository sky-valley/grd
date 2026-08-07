package intent_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/sky-valley/grd/internal/intent"
)

func TestRepositoryReconcilesDependentAsNewVersionAgainstAcceptedAmendment(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewEphemeralRepository(initialContent, &recordingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	foundation, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-foundation",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "ffffffff"},
		Producer:       "maintainer",
	})
	if err != nil {
		t.Fatalf("propose foundation: %v", err)
	}
	foundationPromotion, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      foundation.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote foundation: %v", err)
	}
	parent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     foundationPromotion.Intent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	dependent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-c",
		BaseIntent:     foundationPromotion.Intent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "contributor",
		Dependencies:   []intent.VersionID{foundation.Version.ID, parent.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	amended, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        parent.Change.ID,
		ExpectedVersion: parent.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository-agent",
		Rationale:       "repair B",
	})
	if err != nil {
		t.Fatalf("amend parent: %v", err)
	}
	promoted, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      amended.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote amended parent: %v", err)
	}
	request := intent.ReconcileDependentRequest{
		IdempotencyKey:     "reconcile-c",
		ExpectedVersion:    dependent.Version.ID,
		ReplacedDependency: parent.Version.ID,
		AcceptedVersion:    amended.Version.ID,
		ExpectedIntent:     promoted.Intent.ID,
		Content:            intent.ContentRef{Engine: "git", Revision: "c2c2c2c2"},
		Producer:           "git-engine",
		Rationale:          "replayed C onto accepted B prime",
	}

	reconciled, err := repository.ReconcileDependent(ctx, request)
	if err != nil {
		t.Fatalf("reconcile dependent: %v", err)
	}
	if reconciled.Change.ID != dependent.Change.ID ||
		reconciled.Version.ID == dependent.Version.ID ||
		reconciled.Version.ChangeID != dependent.Change.ID ||
		reconciled.Version.BaseIntent != promoted.Intent.ID ||
		reconciled.Version.Content != request.Content ||
		reconciled.Version.Producer != request.Producer ||
		!reflect.DeepEqual(reconciled.Version.Dependencies, []intent.VersionID{foundation.Version.ID}) {
		t.Fatalf("reconciled dependent = %#v, want C prime based on accepted B prime", reconciled)
	}
	if reconciled.Reconciliation.FromVersion != dependent.Version.ID ||
		reconciled.Reconciliation.ToVersion != reconciled.Version.ID ||
		reconciled.Reconciliation.ReplacedDependency != parent.Version.ID ||
		reconciled.Reconciliation.AcceptedVersion != amended.Version.ID ||
		reconciled.Reconciliation.BaseIntent != promoted.Intent.ID ||
		reconciled.Reconciliation.Rationale != request.Rationale {
		t.Fatalf("reconciliation lineage = %#v, want immutable B/B prime and C/C prime facts", reconciled.Reconciliation)
	}
	if candidates, err := repository.DependentReconciliations(ctx); err != nil {
		t.Fatalf("read remaining reconciliations: %v", err)
	} else if len(candidates) != 0 {
		t.Fatalf("remaining reconciliations = %#v, want C prime to supersede C", candidates)
	}
	pending, err := repository.PendingEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list pending evaluations: %v", err)
	}
	if len(pending.Versions) != 1 || pending.Versions[0].ID != reconciled.Version.ID {
		t.Fatalf("pending evaluations = %#v, want reconciled version %q only", pending.Versions, reconciled.Version.ID)
	}
	retried, err := repository.ReconcileDependent(ctx, request)
	if err != nil {
		t.Fatalf("retry reconciliation: %v", err)
	}
	if !reflect.DeepEqual(retried, reconciled) {
		t.Fatalf("retried reconciliation = %#v, want %#v", retried, reconciled)
	}
	versions, err := repository.Versions(ctx, intent.VersionQuery{ChangeID: dependent.Change.ID, Limit: 10})
	if err != nil {
		t.Fatalf("read dependent versions: %v", err)
	}
	if len(versions.Versions) != 2 ||
		versions.Versions[0].ID != dependent.Version.ID ||
		versions.Versions[1].ID != reconciled.Version.ID {
		t.Fatalf("dependent versions = %#v, want immutable C then C prime", versions.Versions)
	}
}

func TestRepositoryReconcilesHistoricalCandidateAgainstCurrentIntent(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewEphemeralRepository(initialContent, &recordingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	parent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	dependent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-c",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "contributor",
		Dependencies:   []intent.VersionID{parent.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	amended, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        parent.Change.ID,
		ExpectedVersion: parent.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository-agent",
		Rationale:       "repair B",
	})
	if err != nil {
		t.Fatalf("amend parent: %v", err)
	}
	acceptedParent, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      amended.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote amended parent: %v", err)
	}
	unrelated, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-d",
		BaseIntent:     acceptedParent.Intent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:       "maintainer",
	})
	if err != nil {
		t.Fatalf("propose unrelated change: %v", err)
	}
	current, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      unrelated.Version.ID,
		ExpectedIntent: acceptedParent.Intent.ID,
	})
	if err != nil {
		t.Fatalf("promote unrelated change: %v", err)
	}
	candidates, err := repository.DependentReconciliations(ctx)
	if err != nil {
		t.Fatalf("discover historical candidate: %v", err)
	}
	if len(candidates) != 1 || candidates[0].AcceptedAtIntent != acceptedParent.Intent.ID {
		t.Fatalf("historical candidates = %#v, want C from accepted B prime intent", candidates)
	}

	reconciled, err := repository.ReconcileDependent(ctx, intent.ReconcileDependentRequest{
		IdempotencyKey:     "reconcile-c-after-d",
		ExpectedVersion:    dependent.Version.ID,
		ReplacedDependency: parent.Version.ID,
		AcceptedVersion:    amended.Version.ID,
		ExpectedIntent:     current.Intent.ID,
		Content:            intent.ContentRef{Engine: "git", Revision: "c3c3c3c3"},
		Producer:           "git-engine",
		Rationale:          "replayed C onto current intent after accepted B prime",
	})
	if err != nil {
		t.Fatalf("reconcile historical candidate: %v", err)
	}
	if reconciled.Version.BaseIntent != current.Intent.ID ||
		reconciled.Reconciliation.BaseIntent != current.Intent.ID ||
		reconciled.Reconciliation.AcceptedVersion != amended.Version.ID {
		t.Fatalf("historical reconciliation = %#v, want B prime lineage based on current D", reconciled)
	}
}

func TestRepositoryDependentReconciliationIsSafeToRetryAndRace(t *testing.T) {
	ctx := context.Background()
	repository, dependent, request := newDependentReconciliationFixture(t)

	start := make(chan struct{})
	results := make(chan intent.ReconciledDependent, 2)
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := repository.ReconcileDependent(ctx, request)
			results <- result
			errs <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errs)

	var first intent.ReconciledDependent
	for result := range results {
		if first.Version.ID == "" {
			first = result
		} else if !reflect.DeepEqual(result, first) {
			t.Fatalf("concurrent retry = %#v, want %#v", result, first)
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent identical retry: %v", err)
		}
	}
	versions, err := repository.Versions(ctx, intent.VersionQuery{ChangeID: dependent.Change.ID, Limit: 10})
	if err != nil {
		t.Fatalf("read versions after retry race: %v", err)
	}
	if len(versions.Versions) != 2 {
		t.Fatalf("versions after retry race = %#v, want C and one C prime", versions.Versions)
	}
	changed := request
	changed.Rationale = "different replay"
	if _, err := repository.ReconcileDependent(ctx, changed); !errors.Is(err, intent.ErrIdempotencyConflict) {
		t.Fatalf("changed same-key retry error = %v, want ErrIdempotencyConflict", err)
	}

	racingRepository, _, firstRequest := newDependentReconciliationFixture(t)
	secondRequest := firstRequest
	secondRequest.IdempotencyKey = "reconcile-c-different-attempt"
	start = make(chan struct{})
	errs = make(chan error, 2)
	for _, attempt := range []intent.ReconcileDependentRequest{firstRequest, secondRequest} {
		wait.Add(1)
		go func(request intent.ReconcileDependentRequest) {
			defer wait.Done()
			<-start
			_, err := racingRepository.ReconcileDependent(ctx, request)
			errs <- err
		}(attempt)
	}
	close(start)
	wait.Wait()
	close(errs)
	successes := 0
	advanced := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, intent.ErrVersionAdvanced):
			advanced++
		default:
			t.Fatalf("different-key race error = %v, want nil or ErrVersionAdvanced", err)
		}
	}
	if successes != 1 || advanced != 1 {
		t.Fatalf("different-key race results = %d successes, %d advanced; want one each", successes, advanced)
	}
}

func TestRepositoryDependentReconciliationRejectsStaleIntent(t *testing.T) {
	ctx := context.Background()
	repository, _, request := newDependentReconciliationFixture(t)
	request.ExpectedIntent = "intent_stale"
	if _, err := repository.ReconcileDependent(ctx, request); !errors.Is(err, intent.ErrIntentAdvanced) {
		t.Fatalf("stale intent error = %v, want ErrIntentAdvanced", err)
	}
}

func TestRepositoryReconsidersDependentWhenConflictBelongsToOlderIntent(t *testing.T) {
	ctx := context.Background()
	repository, dependent, request := newDependentReconciliationFixture(t)
	conflict, err := repository.RecordReconciliationConflict(ctx, intent.ReconciliationConflictRequest{
		IdempotencyKey:    "conflict-c-at-b-prime",
		FromVersion:       request.ReplacedDependency,
		ToVersion:         request.AcceptedVersion,
		DescendantVersion: dependent.Version.ID,
		ExpectedIntent:    request.ExpectedIntent,
		ReportedBy:        "git-engine",
		AffectedPaths:     []string{"schema.sql"},
	})
	if err != nil {
		t.Fatalf("record conflict at B prime: %v", err)
	}
	if candidates, err := repository.DependentReconciliations(ctx); err != nil {
		t.Fatalf("read candidates at B prime: %v", err)
	} else if len(candidates) != 0 {
		t.Fatalf("candidates at conflicted B prime = %#v, want none", candidates)
	}
	unrelated, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-d",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:       "maintainer",
	})
	if err != nil {
		t.Fatalf("propose D: %v", err)
	}
	current, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      unrelated.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote D: %v", err)
	}
	candidates, err := repository.DependentReconciliations(ctx)
	if err != nil {
		t.Fatalf("read candidates after D: %v", err)
	}
	if len(candidates) != 1 ||
		candidates[0].Dependent.Version.ID != dependent.Version.ID ||
		current.Intent.ID == request.ExpectedIntent {
		t.Fatalf("candidates after conflict became stale = %#v, want C reconsidered against D", candidates)
	}
	stale, found, err := repository.ReconciliationConflict(ctx, conflict.ID)
	if err != nil {
		t.Fatalf("read stale conflict: %v", err)
	}
	if !found || !stale.Superseded {
		t.Fatalf("stale conflict = %#v, %t; want derived superseded state", stale, found)
	}
}

func TestRepositoryConflictSuppressionScansBeyondFirstHistoryPage(t *testing.T) {
	ctx := context.Background()
	repository, dependent, request := newDependentReconciliationFixture(t)
	dummy, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-dummy-conflict",
		BaseIntent:     dependent.Version.BaseIntent,
		Content:        intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose dummy conflict descendant: %v", err)
	}
	for index := range 100 {
		if _, err := repository.RecordReconciliationConflict(ctx, intent.ReconciliationConflictRequest{
			IdempotencyKey:    fmt.Sprintf("dummy-conflict-%03d", index),
			FromVersion:       request.ReplacedDependency,
			ToVersion:         request.AcceptedVersion,
			DescendantVersion: dummy.Version.ID,
			ExpectedIntent:    request.ExpectedIntent,
			ReportedBy:        "repository-engine",
		}); err != nil {
			t.Fatalf("record dummy conflict %d: %v", index, err)
		}
	}
	if _, err := repository.RecordReconciliationConflict(ctx, intent.ReconciliationConflictRequest{
		IdempotencyKey:    "dependent-conflict-after-first-page",
		FromVersion:       request.ReplacedDependency,
		ToVersion:         request.AcceptedVersion,
		DescendantVersion: dependent.Version.ID,
		ExpectedIntent:    request.ExpectedIntent,
		ReportedBy:        "repository-engine",
	}); err != nil {
		t.Fatalf("record dependent conflict after first page: %v", err)
	}

	candidates, err := repository.DependentReconciliations(ctx)
	if err != nil {
		t.Fatalf("read dependent reconciliation candidates: %v", err)
	}
	for _, candidate := range candidates {
		if candidate.Dependent.Version.ID == dependent.Version.ID {
			t.Fatalf("candidate after second-page conflict = %#v, want durable conflict to suppress it", candidate)
		}
	}
}

func newDependentReconciliationFixture(t *testing.T) (*intent.Repository, intent.Proposed, intent.ReconcileDependentRequest) {
	t.Helper()
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewEphemeralRepository(initialContent, &recordingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	parent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	dependent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-c",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "contributor",
		Dependencies:   []intent.VersionID{parent.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	amended, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        parent.Change.ID,
		ExpectedVersion: parent.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository-agent",
		Rationale:       "repair B",
	})
	if err != nil {
		t.Fatalf("amend parent: %v", err)
	}
	promoted, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      amended.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote amended parent: %v", err)
	}
	return repository, dependent, intent.ReconcileDependentRequest{
		IdempotencyKey:     "reconcile-c",
		ExpectedVersion:    dependent.Version.ID,
		ReplacedDependency: parent.Version.ID,
		AcceptedVersion:    amended.Version.ID,
		ExpectedIntent:     promoted.Intent.ID,
		Content:            intent.ContentRef{Engine: "git", Revision: "c2c2c2c2"},
		Producer:           "git-engine",
		Rationale:          "replay C onto accepted B prime",
	}
}
