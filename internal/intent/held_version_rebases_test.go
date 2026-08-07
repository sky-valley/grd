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

func TestRepositoryRebasesStaleHeldReconciliationAgainstCurrentIntent(t *testing.T) {
	ctx := context.Background()
	repository, _, reconcileRequest := newDependentReconciliationFixture(t)
	reconciled, err := repository.ReconcileDependent(ctx, reconcileRequest)
	if err != nil {
		t.Fatalf("reconcile dependent: %v", err)
	}

	unrelated, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-d-after-reconciliation",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:       "maintainer",
	})
	if err != nil {
		t.Fatalf("propose unrelated change: %v", err)
	}
	current, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      unrelated.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote unrelated change: %v", err)
	}

	candidates, err := repository.HeldVersionRebases(ctx)
	if err != nil {
		t.Fatalf("read held version rebase candidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Held.Version.ID != reconciled.Version.ID {
		t.Fatalf("held version rebase candidates = %#v, want stale C prime", candidates)
	}

	request := intent.RebaseHeldVersionRequest{
		IdempotencyKey:  "rebase-held-c-prime",
		ExpectedVersion: reconciled.Version.ID,
		ExpectedIntent:  current.Intent.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "c3c3c3c3"},
		Producer:        "repository-engine",
		Rationale:       "replay held C prime onto current intent",
	}
	rebased, err := repository.RebaseHeldVersion(ctx, request)
	if err != nil {
		t.Fatalf("rebase held version: %v", err)
	}
	if rebased.Change.ID != reconciled.Change.ID ||
		rebased.Version.ID == reconciled.Version.ID ||
		rebased.Version.ChangeID != reconciled.Change.ID ||
		rebased.Version.BaseIntent != current.Intent.ID ||
		rebased.Version.Content != request.Content ||
		rebased.Version.Producer != request.Producer ||
		!reflect.DeepEqual(rebased.Version.Dependencies, reconciled.Version.Dependencies) {
		t.Fatalf("rebased held version = %#v, want C double-prime against current intent", rebased)
	}
	if rebased.Rebase.FromVersion != reconciled.Version.ID ||
		rebased.Rebase.ToVersion != rebased.Version.ID ||
		rebased.Rebase.FromIntent != reconciled.Version.BaseIntent ||
		rebased.Rebase.ToIntent != current.Intent.ID ||
		rebased.Rebase.Rationale != request.Rationale {
		t.Fatalf("held version rebase = %#v, want immutable C prime to C double-prime fact", rebased.Rebase)
	}
	pending, err := repository.PendingEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list pending evaluations: %v", err)
	}
	if len(pending.Versions) != 1 || pending.Versions[0].ID != rebased.Version.ID {
		t.Fatalf("pending evaluations = %#v, want rebased version %q only", pending.Versions, rebased.Version.ID)
	}
	retried, err := repository.RebaseHeldVersion(ctx, request)
	if err != nil {
		t.Fatalf("retry held version rebase: %v", err)
	}
	if !reflect.DeepEqual(retried, rebased) {
		t.Fatalf("retried held version rebase = %#v, want %#v", retried, rebased)
	}
	if candidates, err := repository.HeldVersionRebases(ctx); err != nil {
		t.Fatalf("read remaining held version rebase candidates: %v", err)
	} else if len(candidates) != 0 {
		t.Fatalf("remaining held version rebase candidates = %#v, want none", candidates)
	}
}

func TestRepositoryExposesDeferredConflictResolutionForRebaseAfterIntentAdvances(t *testing.T) {
	ctx := context.Background()
	repository, dependent, reconcileRequest := newDependentReconciliationFixture(t)
	conflict, err := repository.RecordReconciliationConflict(ctx, intent.ReconciliationConflictRequest{
		IdempotencyKey:    "conflict-c",
		FromVersion:       reconcileRequest.ReplacedDependency,
		ToVersion:         reconcileRequest.AcceptedVersion,
		DescendantVersion: dependent.Version.ID,
		ExpectedIntent:    reconcileRequest.ExpectedIntent,
		ReportedBy:        "repository-engine",
	})
	if err != nil {
		t.Fatalf("record reconciliation conflict: %v", err)
	}
	resolved, err := repository.ResolveReconciliationConflict(ctx, intent.ResolveReconciliationConflictRequest{
		IdempotencyKey:  "resolve-c",
		ConflictID:      conflict.ID,
		ExpectedVersion: dependent.Version.ID,
		ExpectedIntent:  reconcileRequest.ExpectedIntent,
		Content:         intent.ContentRef{Engine: "git", Revision: "c2c2c2c2"},
		Producer:        "repository-engine",
		ResolvedBy:      "evaluation-agent",
		Rationale:       "resolve C against accepted B prime",
	})
	if err != nil {
		t.Fatalf("resolve reconciliation conflict: %v", err)
	}
	if _, err := repository.RecordReconciliationConflict(ctx, intent.ReconciliationConflictRequest{
		IdempotencyKey:    "obsolete-conflict-on-c",
		FromVersion:       reconcileRequest.ReplacedDependency,
		ToVersion:         reconcileRequest.AcceptedVersion,
		DescendantVersion: dependent.Version.ID,
		ExpectedIntent:    reconcileRequest.ExpectedIntent,
		ReportedBy:        "repository-engine",
	}); !errors.Is(err, intent.ErrVersionAdvanced) {
		t.Fatalf("obsolete C conflict error = %v, want ErrVersionAdvanced", err)
	}

	unrelated, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-d-after-resolution",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:       "maintainer",
	})
	if err != nil {
		t.Fatalf("propose unrelated change: %v", err)
	}
	current, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      unrelated.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote unrelated change: %v", err)
	}

	inspection, found, err := repository.ReconciliationConflict(ctx, conflict.ID)
	if err != nil {
		t.Fatalf("inspect reconciliation conflict: %v", err)
	}
	if !found || !inspection.Superseded || inspection.Resolution == nil {
		t.Fatalf("advanced conflict inspection = %#v, %t; want resolved attempt derived as superseded", inspection, found)
	}
	candidates, err := repository.HeldVersionRebases(ctx)
	if err != nil {
		t.Fatalf("read held version rebase candidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Held.Version.ID != resolved.Version.ID {
		t.Fatalf("held version rebase candidates = %#v, want stale resolved C prime", candidates)
	}
	rebased, err := repository.RebaseHeldVersion(ctx, intent.RebaseHeldVersionRequest{
		IdempotencyKey:  "rebase-resolved-c",
		ExpectedVersion: resolved.Version.ID,
		ExpectedIntent:  current.Intent.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "c3c3c3c3"},
		Producer:        "repository-engine",
		Rationale:       "replay resolved C prime onto current intent",
	})
	if err != nil {
		t.Fatalf("rebase resolved held version: %v", err)
	}
	final, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-rebased-resolved-c",
		ChangeID:        rebased.Change.ID,
		ExpectedVersion: rebased.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "c4c4c4c4"},
		Producer:        "repository-agent",
		Rationale:       "finish the rebased resolution",
	})
	if err != nil {
		t.Fatalf("amend rebased resolved version: %v", err)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      final.Version.ID,
		ExpectedIntent: current.Intent.ID,
	}); err != nil {
		t.Fatalf("promote amended effective resolution: %v", err)
	}
	accepted, found, err := repository.ReconciliationConflict(ctx, conflict.ID)
	if err != nil {
		t.Fatalf("inspect accepted effective resolution: %v", err)
	}
	if !found || accepted.Superseded ||
		accepted.EffectiveVersion == nil ||
		accepted.EffectiveVersion.ID != final.Version.ID ||
		len(accepted.EffectiveTransitions) != 2 ||
		accepted.EffectiveTransitions[0].Kind != intent.HeldVersionRebaseTransition ||
		accepted.EffectiveTransitions[0].FromVersion != resolved.Version.ID ||
		accepted.EffectiveTransitions[0].ToVersion != rebased.Version.ID ||
		accepted.EffectiveTransitions[1].Kind != intent.AmendmentTransition ||
		accepted.EffectiveTransitions[1].FromVersion != rebased.Version.ID ||
		accepted.EffectiveTransitions[1].ToVersion != final.Version.ID {
		t.Fatalf("accepted effective resolution = %#v, %t; want resolution, rebase, and amendment lineage", accepted, found)
	}
}

func TestRepositoryHeldVersionRebaseIsSafeToRetryAndRace(t *testing.T) {
	ctx := context.Background()
	repository, _, reconcileRequest := newDependentReconciliationFixture(t)
	reconciled, err := repository.ReconcileDependent(ctx, reconcileRequest)
	if err != nil {
		t.Fatalf("reconcile dependent: %v", err)
	}
	unrelated, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-d-for-rebase-race",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:       "maintainer",
	})
	if err != nil {
		t.Fatalf("propose unrelated change: %v", err)
	}
	current, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      unrelated.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote unrelated change: %v", err)
	}
	requests := []intent.RebaseHeldVersionRequest{
		{
			IdempotencyKey:  "rebase-contender-1",
			ExpectedVersion: reconciled.Version.ID,
			ExpectedIntent:  current.Intent.ID,
			Content:         intent.ContentRef{Engine: "git", Revision: "c3c3c3c3"},
			Producer:        "repository-engine",
			Rationale:       "replay against current intent",
		},
		{
			IdempotencyKey:  "rebase-contender-2",
			ExpectedVersion: reconciled.Version.ID,
			ExpectedIntent:  current.Intent.ID,
			Content:         intent.ContentRef{Engine: "git", Revision: "c4c4c4c4"},
			Producer:        "repository-engine",
			Rationale:       "alternative replay against current intent",
		},
	}
	var results [2]intent.RebasedHeldVersion
	var resultErrors [2]error
	var wait sync.WaitGroup
	for index := range requests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[index], resultErrors[index] = repository.RebaseHeldVersion(ctx, requests[index])
		}()
	}
	wait.Wait()
	winner := -1
	for index, resultErr := range resultErrors {
		switch {
		case resultErr == nil:
			winner = index
		case errors.Is(resultErr, intent.ErrVersionAdvanced):
		default:
			t.Fatalf("rebase contender %d error = %v, want nil or ErrVersionAdvanced", index, resultErr)
		}
	}
	if winner < 0 || (resultErrors[0] == nil) == (resultErrors[1] == nil) {
		t.Fatalf("rebase contender errors = %v, want one success and one ErrVersionAdvanced", resultErrors)
	}
	retried, err := repository.RebaseHeldVersion(ctx, requests[winner])
	if err != nil {
		t.Fatalf("retry winning held version rebase: %v", err)
	}
	if !reflect.DeepEqual(retried, results[winner]) {
		t.Fatalf("retried held version rebase = %#v, want %#v", retried, results[winner])
	}
	changed := requests[winner]
	changed.Content.Revision = "eeeeeeee"
	if _, err := repository.RebaseHeldVersion(ctx, changed); !errors.Is(err, intent.ErrIdempotencyConflict) {
		t.Fatalf("changed same-key retry error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestRepositoryHeldVersionRebaseDiscoveryScansBeyondFirstHistoryPage(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewEphemeralRepository(initialContent, &recordingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	for index := range 101 {
		parent, err := repository.Propose(ctx, intent.Proposal{
			IdempotencyKey: fmt.Sprintf("proposal-parent-%03d", index),
			BaseIntent:     repository.CurrentIntent().ID,
			Content:        intent.ContentRef{Engine: "git", Revision: fmt.Sprintf("b%07d", index)},
			Producer:       "contributor",
		})
		if err != nil {
			t.Fatalf("propose parent %d: %v", index, err)
		}
		dependent, err := repository.Propose(ctx, intent.Proposal{
			IdempotencyKey: fmt.Sprintf("proposal-dependent-%03d", index),
			BaseIntent:     repository.CurrentIntent().ID,
			Content:        intent.ContentRef{Engine: "git", Revision: fmt.Sprintf("c%07d", index)},
			Producer:       "contributor",
			Dependencies:   []intent.VersionID{parent.Version.ID},
		})
		if err != nil {
			t.Fatalf("propose dependent %d: %v", index, err)
		}
		amended, err := repository.Amend(ctx, intent.AmendRequest{
			IdempotencyKey:  fmt.Sprintf("amend-parent-%03d", index),
			ChangeID:        parent.Change.ID,
			ExpectedVersion: parent.Version.ID,
			Content:         intent.ContentRef{Engine: "git", Revision: fmt.Sprintf("p%07d", index)},
			Producer:        "repository-agent",
			Rationale:       "repair parent",
		})
		if err != nil {
			t.Fatalf("amend parent %d: %v", index, err)
		}
		promoted, err := repository.Promote(ctx, intent.PromoteRequest{
			VersionID:      amended.Version.ID,
			ExpectedIntent: repository.CurrentIntent().ID,
		})
		if err != nil {
			t.Fatalf("promote parent %d: %v", index, err)
		}
		if _, err := repository.ReconcileDependent(ctx, intent.ReconcileDependentRequest{
			IdempotencyKey:     fmt.Sprintf("reconcile-dependent-%03d", index),
			ExpectedVersion:    dependent.Version.ID,
			ReplacedDependency: parent.Version.ID,
			AcceptedVersion:    amended.Version.ID,
			ExpectedIntent:     promoted.Intent.ID,
			Content:            intent.ContentRef{Engine: "git", Revision: fmt.Sprintf("r%07d", index)},
			Producer:           "repository-engine",
			Rationale:          "replay dependent",
		}); err != nil {
			t.Fatalf("reconcile dependent %d: %v", index, err)
		}
	}
	advance, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-after-reconciliation-page",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "eeeeeeee"},
		Producer:       "maintainer",
	})
	if err != nil {
		t.Fatalf("propose final advance: %v", err)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      advance.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	}); err != nil {
		t.Fatalf("promote final advance: %v", err)
	}

	candidates, err := repository.HeldVersionRebases(ctx)
	if err != nil {
		t.Fatalf("discover held version rebases: %v", err)
	}
	if len(candidates) != 101 {
		t.Fatalf("held version rebase candidates = %d, want 101 across two history pages", len(candidates))
	}
}
