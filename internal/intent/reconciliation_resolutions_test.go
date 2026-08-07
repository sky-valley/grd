package intent_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/sky-valley/grd/internal/intent"
)

func TestRepositoryResolvesReconciliationConflictAsNewVersionOfExistingChange(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	admission := &recordingAdmission{}
	repository, err := intent.NewEphemeralRepository(initialContent, admission, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	initial := repository.CurrentIntent()
	foundation, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-foundation",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "f0f0f0f0"},
		Producer:       "maintainer",
	})
	if err != nil {
		t.Fatalf("propose foundation: %v", err)
	}
	foundationPromotion, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      foundation.Version.ID,
		ExpectedIntent: initial.ID,
	})
	if err != nil {
		t.Fatalf("promote foundation: %v", err)
	}
	original, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     foundationPromotion.Intent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose B: %v", err)
	}
	amended, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        original.Change.ID,
		ExpectedVersion: original.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository-agent",
		Rationale:       "repair B",
	})
	if err != nil {
		t.Fatalf("amend B: %v", err)
	}
	promoted, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      amended.Version.ID,
		ExpectedIntent: foundationPromotion.Intent.ID,
	})
	if err != nil {
		t.Fatalf("promote B prime: %v", err)
	}
	descendant, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-c",
		BaseIntent:     foundationPromotion.Intent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "contributor",
		Dependencies:   []intent.VersionID{foundation.Version.ID, original.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose C: %v", err)
	}
	conflict, err := repository.RecordReconciliationConflict(ctx, intent.ReconciliationConflictRequest{
		IdempotencyKey:    "conflict-b-c",
		FromVersion:       original.Version.ID,
		ToVersion:         amended.Version.ID,
		DescendantVersion: descendant.Version.ID,
		ExpectedIntent:    promoted.Intent.ID,
		ReportedBy:        "contributor",
		AffectedPaths:     []string{"model.go"},
	})
	if err != nil {
		t.Fatalf("record conflict: %v", err)
	}
	admissionsBeforeResolution := len(admission.admissions)
	request := intent.ResolveReconciliationConflictRequest{
		IdempotencyKey:  "resolve-b-c",
		ConflictID:      conflict.ID,
		ExpectedVersion: descendant.Version.ID,
		ExpectedIntent:  promoted.Intent.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "c2c2c2c2"},
		Producer:        "repository-agent",
		ResolvedBy:      "evaluation-agent",
		Rationale:       "replayed C onto accepted B prime and repaired the conflict",
	}
	stale := request
	stale.IdempotencyKey = "resolve-b-c-from-stale-intent"
	stale.ExpectedIntent = foundationPromotion.Intent.ID
	if _, err := repository.ResolveReconciliationConflict(ctx, stale); !errors.Is(err, intent.ErrIntentAdvanced) {
		t.Fatalf("stale resolution error = %v, want ErrIntentAdvanced", err)
	}
	if len(admission.admissions) != admissionsBeforeResolution {
		t.Fatalf("stale resolution admitted content: admissions %d -> %d", admissionsBeforeResolution, len(admission.admissions))
	}
	wrongOperation := request
	wrongOperation.IdempotencyKey = "proposal-b"
	if _, err := repository.ResolveReconciliationConflict(ctx, wrongOperation); !errors.Is(err, intent.ErrIdempotencyConflict) {
		t.Fatalf("proposal key reused for resolution error = %v, want ErrIdempotencyConflict", err)
	}

	requests := []intent.ResolveReconciliationConflictRequest{request, request}
	requests[1].IdempotencyKey = "resolve-b-c-contender"
	var results [2]intent.ResolvedReconciliationConflict
	var resolutionErrors [2]error
	var wait sync.WaitGroup
	for index := range requests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[index], resolutionErrors[index] = repository.ResolveReconciliationConflict(ctx, requests[index])
		}()
	}
	wait.Wait()
	winner := -1
	loser := -1
	for index, resolutionErr := range resolutionErrors {
		switch {
		case resolutionErr == nil:
			winner = index
		case errors.Is(resolutionErr, intent.ErrReconciliationConflictResolved):
			loser = index
		default:
			t.Fatalf("contended resolution %d error = %v, want one success and one ErrReconciliationConflictResolved", index, resolutionErr)
		}
	}
	if winner < 0 || loser < 0 {
		t.Fatalf("contended resolution errors = %v, want one success and one resolved conflict", resolutionErrors)
	}
	request = requests[winner]
	resolved := results[winner]
	if resolved.Resolution.ID == "" {
		t.Fatalf("resolution = %#v, want durable identity", resolved.Resolution)
	}
	if resolved.Change != descendant.Change || resolved.Version.ChangeID != descendant.Change.ID {
		t.Fatalf("resolved change/version = %#v / %#v, want existing change %#v", resolved.Change, resolved.Version, descendant.Change)
	}
	if resolved.Version.ID == descendant.Version.ID {
		t.Fatalf("resolved version id = %q, want new immutable version", resolved.Version.ID)
	}
	if resolved.Version.BaseIntent != promoted.Intent.ID {
		t.Fatalf("resolved base = %q, want current accepted intent %q", resolved.Version.BaseIntent, promoted.Intent.ID)
	}
	if !reflect.DeepEqual(resolved.Version.Dependencies, []intent.VersionID{foundation.Version.ID}) {
		t.Fatalf("resolved dependencies = %q, want accepted foundation preserved and obsolete B removed", resolved.Version.Dependencies)
	}
	if resolved.Version.Content != request.Content || resolved.Version.Producer != request.Producer {
		t.Fatalf("resolved content/producer = %#v / %q, want %#v / %q", resolved.Version.Content, resolved.Version.Producer, request.Content, request.Producer)
	}
	if resolved.Resolution.ConflictID != conflict.ID ||
		resolved.Resolution.FromVersion != descendant.Version.ID ||
		resolved.Resolution.ToVersion != resolved.Version.ID ||
		resolved.Resolution.BaseIntent != promoted.Intent.ID ||
		resolved.Resolution.ResolvedBy != request.ResolvedBy ||
		resolved.Resolution.Rationale != request.Rationale {
		t.Fatalf("resolution = %#v, want exact C -> C prime evaluation fact", resolved.Resolution)
	}
	if len(admission.admissions) != admissionsBeforeResolution+1 || admission.admissions[len(admission.admissions)-1].versionID != resolved.Version.ID {
		t.Fatalf("resolution admissions = %#v, want C prime admitted once", admission.admissions)
	}
	pending, err := repository.PendingEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list pending evaluations: %v", err)
	}
	if len(pending.Versions) != 1 || pending.Versions[0].ID != resolved.Version.ID {
		t.Fatalf("pending evaluations = %#v, want resolved version %q only", pending.Versions, resolved.Version.ID)
	}

	loadedConflict, found, err := repository.ReconciliationConflict(ctx, conflict.ID)
	if err != nil || !found {
		t.Fatalf("load resolved conflict = %#v, %t, %v", loadedConflict, found, err)
	}
	if loadedConflict.Resolution == nil || !reflect.DeepEqual(*loadedConflict.Resolution, resolved.Resolution) {
		t.Fatalf("loaded conflict resolution = %#v, want %#v", loadedConflict.Resolution, resolved.Resolution)
	}
	retried, err := repository.ResolveReconciliationConflict(ctx, request)
	if err != nil {
		t.Fatalf("retry resolution: %v", err)
	}
	if !reflect.DeepEqual(retried, resolved) {
		t.Fatalf("retried resolution = %#v, want %#v", retried, resolved)
	}
	secondResolution := request
	secondResolution.IdempotencyKey = "resolve-b-c-again"
	if _, err := repository.ResolveReconciliationConflict(ctx, secondResolution); !errors.Is(err, intent.ErrReconciliationConflictResolved) {
		t.Fatalf("second resolution error = %v, want ErrReconciliationConflictResolved", err)
	}
	conflictingRetry := request
	conflictingRetry.Content.Revision = "dddddddd"
	if _, err := repository.ResolveReconciliationConflict(ctx, conflictingRetry); !errors.Is(err, intent.ErrIdempotencyConflict) {
		t.Fatalf("conflicting retry error = %v, want ErrIdempotencyConflict", err)
	}

	final, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      resolved.Version.ID,
		ExpectedIntent: promoted.Intent.ID,
	})
	if err != nil {
		t.Fatalf("promote C prime through normal promotion: %v", err)
	}
	if final.Intent.Content != request.Content {
		t.Fatalf("final intent content = %#v, want C prime %#v", final.Intent.Content, request.Content)
	}
}
