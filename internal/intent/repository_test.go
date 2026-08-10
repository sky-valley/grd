package intent_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/sky-valley/grd/internal/intent"
)

func TestRepositoryAdmitsDependentVersionAndRefusesPrematurePromotion(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	projection := &recordingProjection{current: initialContent}
	repository, err := intent.NewEphemeralRepository(initialContent, &recordingAdmission{}, projection)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	baseIntent := repository.CurrentIntent()

	parent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-parent",
		BaseIntent:     baseIntent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	dependent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-dependent",
		BaseIntent:     baseIntent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "contributor",
		Dependencies:   []intent.VersionID{parent.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	if !slices.Equal(dependent.Version.Dependencies, []intent.VersionID{parent.Version.ID}) {
		t.Fatalf("dependent versions = %q, want parent version %q", dependent.Version.Dependencies, parent.Version.ID)
	}
	dependent.Version.Dependencies[0] = "version_corrupted_by_caller"
	inspection, err := repository.InspectChange(ctx, dependent.Change.ID)
	if err != nil {
		t.Fatalf("inspect dependent: %v", err)
	}
	if !slices.Equal(inspection.LatestVersion.Dependencies, []intent.VersionID{parent.Version.ID}) {
		t.Fatalf("stored dependencies changed through returned value: %q", inspection.LatestVersion.Dependencies)
	}

	_, err = repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      dependent.Version.ID,
		ExpectedIntent: baseIntent.ID,
	})
	if !errors.Is(err, intent.ErrDependenciesPending) {
		t.Fatalf("promote dependent error = %v, want ErrDependenciesPending", err)
	}
	if got := repository.CurrentIntent(); got != baseIntent {
		t.Fatalf("current intent = %#v, want unchanged %#v", got, baseIntent)
	}
	if len(projection.advances) != 0 {
		t.Fatalf("projection advances = %d, want 0", len(projection.advances))
	}
}

func TestRepositoryReadsAnExactVersionByID(t *testing.T) {
	ctx := context.Background()
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewEphemeralRepository(initial, &recordingAdmission{}, &recordingProjection{current: initial})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-version-read",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "local:ion",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	got, found, err := repository.Version(ctx, proposed.Version.ID)
	if err != nil || !found {
		t.Fatalf("read Version: found %t, error %v", found, err)
	}
	if !reflect.DeepEqual(got, proposed.Version) {
		t.Fatalf("Version = %#v, want %#v", got, proposed.Version)
	}
	if _, found, err := repository.Version(ctx, "version_missing"); err != nil || found {
		t.Fatalf("missing Version: found %t, error %v", found, err)
	}
}

func TestRepositoryPromotesDependentAgainstIntentProducedByItsDependency(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	projection := &recordingProjection{current: initialContent}
	repository, err := intent.NewEphemeralRepository(initialContent, &recordingAdmission{}, projection)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	initial := repository.CurrentIntent()
	parent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-parent",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	dependent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-dependent",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "contributor",
		Dependencies:   []intent.VersionID{parent.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	parentPromotion, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      parent.Version.ID,
		ExpectedIntent: initial.ID,
	})
	if err != nil {
		t.Fatalf("promote parent: %v", err)
	}

	ready, err := repository.ReadyDependents(ctx)
	if err != nil {
		t.Fatalf("read ready dependents: %v", err)
	}
	if len(ready) != 1 || ready[0].Version.ID != dependent.Version.ID {
		t.Fatalf("ready dependents = %#v, want dependent version %q", ready, dependent.Version.ID)
	}
	dependentPromotion, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      dependent.Version.ID,
		ExpectedIntent: parentPromotion.Intent.ID,
	})
	if err != nil {
		t.Fatalf("promote dependent: %v", err)
	}
	if dependentPromotion.Promotion.FromIntent != parentPromotion.Intent.ID {
		t.Fatalf("dependent promoted from %q, want parent intent %q", dependentPromotion.Promotion.FromIntent, parentPromotion.Intent.ID)
	}
	if got := repository.CurrentIntent().Content; got != dependent.Version.Content {
		t.Fatalf("current content = %#v, want dependent content %#v", got, dependent.Version.Content)
	}
}

func TestRepositoryDoesNotTreatDependentAsReadyAfterUnrelatedIntentAdvance(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewEphemeralRepository(initialContent, &recordingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	initial := repository.CurrentIntent()
	parent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-parent",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	_, err = repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-dependent",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "contributor",
		Dependencies:   []intent.VersionID{parent.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	unrelated, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-unrelated",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:       "maintainer",
	})
	if err != nil {
		t.Fatalf("propose unrelated: %v", err)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{VersionID: unrelated.Version.ID, ExpectedIntent: initial.ID}); err != nil {
		t.Fatalf("promote unrelated: %v", err)
	}

	ready, err := repository.ReadyDependents(ctx)
	if err != nil {
		t.Fatalf("read ready dependents: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("ready dependents = %#v, want none after unrelated intent advance", ready)
	}
}

func TestRepositoryProposeThenPromoteAgainstCurrentIntent(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	proposedContent := intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"}
	admission := &recordingAdmission{}
	projection := &recordingProjection{current: initialContent}
	repository, err := intent.NewEphemeralRepository(initialContent, admission, projection)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	initialIntent := repository.CurrentIntent()

	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     initialIntent.ID,
		Content:        proposedContent,
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	if proposed.Change.ID == "" {
		t.Fatal("change id is empty")
	}
	if proposed.Version.ID == "" {
		t.Fatal("version id is empty")
	}
	if proposed.Version.ChangeID != proposed.Change.ID {
		t.Fatalf("version change id = %q, want %q", proposed.Version.ChangeID, proposed.Change.ID)
	}
	if proposed.Version.BaseIntent != initialIntent.ID {
		t.Fatalf("version base intent = %q, want %q", proposed.Version.BaseIntent, initialIntent.ID)
	}
	if proposed.Version.Content != proposedContent {
		t.Fatalf("version content = %#v, want %#v", proposed.Version.Content, proposedContent)
	}
	if proposed.Version.Producer != "contributor" {
		t.Fatalf("version producer = %q, want contributor", proposed.Version.Producer)
	}
	if got := repository.CurrentIntent(); got != initialIntent {
		t.Fatalf("current intent after propose = %#v, want %#v", got, initialIntent)
	}
	if len(projection.advances) != 0 {
		t.Fatalf("projection advances after propose = %d, want 0", len(projection.advances))
	}
	if len(admission.admissions) != 1 {
		t.Fatalf("content admissions = %d, want 1", len(admission.admissions))
	}
	if admission.admissions[0].versionID != proposed.Version.ID || admission.admissions[0].content != proposedContent {
		t.Fatalf("content admission = %#v, want version %q content %#v", admission.admissions[0], proposed.Version.ID, proposedContent)
	}

	promoted, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: initialIntent.ID,
	})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}

	currentIntent := repository.CurrentIntent()
	if currentIntent.ID == "" || currentIntent.ID == initialIntent.ID {
		t.Fatalf("current intent id = %q, want a new non-empty id", currentIntent.ID)
	}
	if currentIntent.PreviousID != initialIntent.ID {
		t.Fatalf("current intent previous id = %q, want %q", currentIntent.PreviousID, initialIntent.ID)
	}
	if currentIntent.Content != proposedContent {
		t.Fatalf("current intent content = %#v, want %#v", currentIntent.Content, proposedContent)
	}
	if promoted.Intent != currentIntent {
		t.Fatalf("promoted intent = %#v, want current intent %#v", promoted.Intent, currentIntent)
	}

	if promoted.Promotion.ID == "" {
		t.Fatal("promotion id is empty")
	}
	if promoted.Promotion.FromIntent != initialIntent.ID {
		t.Fatalf("promotion from intent = %q, want %q", promoted.Promotion.FromIntent, initialIntent.ID)
	}
	if promoted.Promotion.ToIntent != currentIntent.ID {
		t.Fatalf("promotion to intent = %q, want %q", promoted.Promotion.ToIntent, currentIntent.ID)
	}
	if promoted.Promotion.VersionID != proposed.Version.ID {
		t.Fatalf("promotion version id = %q, want %q", promoted.Promotion.VersionID, proposed.Version.ID)
	}

	if len(projection.advances) != 1 {
		t.Fatalf("projection advances = %d, want 1", len(projection.advances))
	}
	if projection.advances[0].expected != initialContent {
		t.Fatalf("projection expected content = %#v, want %#v", projection.advances[0].expected, initialContent)
	}
	if projection.advances[0].next != proposedContent {
		t.Fatalf("projection next content = %#v, want %#v", projection.advances[0].next, proposedContent)
	}
}

func TestRepositoryTracksAnAdmittedVersionAsPendingEvaluationUntilPromotion(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewEphemeralRepository(initialContent, &recordingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	initial := repository.CurrentIntent()
	proposal := intent.Proposal{
		IdempotencyKey: "request-pending",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	}
	proposed, err := repository.Propose(ctx, proposal)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := repository.Propose(ctx, proposal); err != nil {
		t.Fatalf("retry proposal: %v", err)
	}

	pending, err := repository.PendingEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list pending evaluations: %v", err)
	}
	if len(pending.Versions) != 1 || !reflect.DeepEqual(pending.Versions[0], proposed.Version) {
		t.Fatalf("pending evaluations = %#v, want proposed version %#v", pending.Versions, proposed)
	}

	if _, err := repository.Promote(ctx, intent.PromoteRequest{VersionID: proposed.Version.ID, ExpectedIntent: initial.ID}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	pending, err = repository.PendingEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list pending evaluations after promotion: %v", err)
	}
	if len(pending.Versions) != 0 {
		t.Fatalf("pending evaluations after promotion = %#v, want none", pending.Versions)
	}
}

func TestRepositoryPaginatesPendingEvaluationsInVersionCreationOrder(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewEphemeralRepository(initialContent, &recordingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	base := repository.CurrentIntent()
	var proposed []intent.Proposed
	for index, revision := range []string{"bbbbbbbb", "cccccccc", "dddddddd"} {
		candidate, err := repository.Propose(ctx, intent.Proposal{
			IdempotencyKey: "request-" + revision,
			BaseIntent:     base.ID,
			Content:        intent.ContentRef{Engine: "git", Revision: revision},
			Producer:       "actor-" + string(rune('a'+index)),
		})
		if err != nil {
			t.Fatalf("propose %s: %v", revision, err)
		}
		proposed = append(proposed, candidate)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{VersionID: proposed[0].Version.ID, ExpectedIntent: base.ID}); err != nil {
		t.Fatalf("promote first version: %v", err)
	}

	first, err := repository.PendingEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 1})
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(first.Versions) != 1 || first.Versions[0].ID != proposed[1].Version.ID || first.NextCursor != proposed[1].Version.ID {
		t.Fatalf("first page = %#v, want second proposal and cursor", first)
	}
	second, err := repository.PendingEvaluations(ctx, intent.PendingEvaluationQuery{After: first.NextCursor, Limit: 1})
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(second.Versions) != 1 || second.Versions[0].ID != proposed[2].Version.ID || second.NextCursor != "" {
		t.Fatalf("second page = %#v, want final proposal without cursor", second)
	}
}

func TestRepositoryReadsAProposedChangeAndItsVersions(t *testing.T) {
	ctx := context.Background()
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewEphemeralRepository(initial, &recordingAdmission{}, &recordingProjection{current: initial})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}

	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-query",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "control-api",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	change, found, err := repository.Change(ctx, proposed.Change.ID)
	if err != nil {
		t.Fatalf("read change: %v", err)
	}
	if !found || change != proposed.Change {
		t.Fatalf("change = %#v, %t; want %#v, true", change, found, proposed.Change)
	}

	page, err := repository.Versions(ctx, intent.VersionQuery{ChangeID: proposed.Change.ID, Limit: 1})
	if err != nil {
		t.Fatalf("read versions: %v", err)
	}
	if len(page.Versions) != 1 || !reflect.DeepEqual(page.Versions[0], proposed.Version) {
		t.Fatalf("versions = %#v, want [%#v]", page.Versions, proposed.Version)
	}
	if page.NextCursor != "" {
		t.Fatalf("next cursor = %q, want empty", page.NextCursor)
	}
	inspection, err := repository.InspectChange(ctx, proposed.Change.ID)
	if err != nil {
		t.Fatalf("inspect change: %v", err)
	}
	if inspection.Change != proposed.Change || !reflect.DeepEqual(inspection.LatestVersion, proposed.Version) || inspection.LatestPromotion != nil {
		t.Fatalf("change inspection = %#v, want change and latest version without promotion", inspection)
	}
	promoted, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: proposed.Version.BaseIntent,
	})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	inspection, err = repository.InspectChange(ctx, proposed.Change.ID)
	if err != nil {
		t.Fatalf("inspect promoted change: %v", err)
	}
	if inspection.LatestPromotion == nil || *inspection.LatestPromotion != promoted {
		t.Fatalf("inspection promotion = %#v, want %#v", inspection.LatestPromotion, promoted)
	}

	_, found, err = repository.Change(ctx, "change_missing")
	if err != nil {
		t.Fatalf("read missing change: %v", err)
	}
	if found {
		t.Fatal("missing change was found")
	}
}

func TestRepositoryHoldsStaleProposalInsteadOfAdvancingIntent(t *testing.T) {
	ctx := context.Background()
	admission := &recordingAdmission{}
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	projection := &recordingProjection{current: initialContent}
	repository, err := intent.NewEphemeralRepository(initialContent, admission, projection)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	staleIntent := repository.CurrentIntent()

	first, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     staleIntent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("first propose: %v", err)
	}
	_, err = repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      first.Version.ID,
		ExpectedIntent: staleIntent.ID,
	})
	if err != nil {
		t.Fatalf("first promote: %v", err)
	}
	currentIntent := repository.CurrentIntent()

	stale, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-2",
		BaseIntent:     staleIntent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("stale propose: %v", err)
	}
	_, err = repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      stale.Version.ID,
		ExpectedIntent: staleIntent.ID,
	})
	if !errors.Is(err, intent.ErrIntentAdvanced) {
		t.Fatalf("stale promote error = %v, want ErrIntentAdvanced", err)
	}
	if got := repository.CurrentIntent(); got != currentIntent {
		t.Fatalf("current intent after stale promote = %#v, want %#v", got, currentIntent)
	}
	if len(projection.advances) != 1 {
		t.Fatalf("projection advances = %d, want 1", len(projection.advances))
	}
}

func TestRepositoryKeepsProposalWhenProjectionFails(t *testing.T) {
	ctx := context.Background()
	projectionErr := errors.New("projection unavailable")
	admission := &recordingAdmission{}
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	projection := &recordingProjection{current: initialContent, err: projectionErr}
	repository, err := intent.NewEphemeralRepository(initialContent, admission, projection)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	initialIntent := repository.CurrentIntent()

	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     initialIntent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	_, err = repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: initialIntent.ID,
	})
	if !errors.Is(err, projectionErr) {
		t.Fatalf("promote error = %v, want projection error", err)
	}
	if got := repository.CurrentIntent(); got != initialIntent {
		t.Fatalf("current intent after projection failure = %#v, want %#v", got, initialIntent)
	}

	projection.err = nil
	if _, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: initialIntent.ID,
	}); err != nil {
		t.Fatalf("retry promote: %v", err)
	}
}

func TestRepositoryDoesNotRecordContentWhenAdmissionFails(t *testing.T) {
	ctx := context.Background()
	admissionErr := errors.New("content missing")
	admission := &recordingAdmission{err: admissionErr}
	repository, err := intent.NewEphemeralRepository(
		intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"},
		admission,
		&recordingProjection{},
	)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	initialIntent := repository.CurrentIntent()

	_, err = repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     initialIntent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if !errors.Is(err, admissionErr) {
		t.Fatalf("propose error = %v, want admission error", err)
	}
	if len(admission.admissions) != 1 {
		t.Fatalf("content admissions = %d, want 1", len(admission.admissions))
	}
	_, err = repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      admission.admissions[0].versionID,
		ExpectedIntent: initialIntent.ID,
	})
	if !errors.Is(err, intent.ErrVersionNotFound) {
		t.Fatalf("promote unrecorded version error = %v, want ErrVersionNotFound", err)
	}
}

type recordingAdmission struct {
	admissions []contentAdmission
	err        error
}

type contentAdmission struct {
	versionID intent.VersionID
	content   intent.ContentRef
}

func (admission *recordingAdmission) Admit(_ context.Context, versionID intent.VersionID, content intent.ContentRef) error {
	admission.admissions = append(admission.admissions, contentAdmission{versionID: versionID, content: content})
	return admission.err
}

type recordingProjection struct {
	current  intent.ContentRef
	advances []projectionAdvance
	err      error
}

type projectionAdvance struct {
	expected intent.ContentRef
	next     intent.ContentRef
}

func (projection *recordingProjection) Advance(_ context.Context, expected, next intent.ContentRef) error {
	projection.advances = append(projection.advances, projectionAdvance{expected: expected, next: next})
	if projection.err == nil {
		projection.current = next
	}
	return projection.err
}

func (projection *recordingProjection) Current(context.Context) (intent.ContentRef, error) {
	return projection.current, nil
}
