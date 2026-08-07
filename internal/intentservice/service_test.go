package intentservice_test

import (
	"context"
	"testing"

	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/intentservice"
)

func TestServiceAdmissionCreatesDurablePendingVersionWithoutPromotion(t *testing.T) {
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	projection := &recordingProjection{current: initialContent}
	repository, err := intent.NewEphemeralRepository(initialContent, acceptingAdmission{}, projection)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	service := intentservice.New(staticResolver{repository: repository})
	initial := repository.CurrentIntent()

	admission, err := service.Propose(context.Background(), "repo_123", intentservice.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "control-api",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if admission.Change.ID == "" || admission.Version.Producer != "control-api" {
		t.Fatalf("admitted proposal = %#v", admission)
	}
	if got := repository.CurrentIntent(); got != initial {
		t.Fatalf("current intent = %#v, want unchanged %#v", got, initial)
	}
	pending, err := repository.PendingEvaluations(context.Background(), intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list pending evaluations: %v", err)
	}
	if len(pending.Versions) != 1 || pending.Versions[0].ID != admission.Version.ID {
		t.Fatalf("pending evaluations = %#v, want admitted version", pending.Versions)
	}
}

func TestServiceListsDurablePendingEvaluations(t *testing.T) {
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewEphemeralRepository(initialContent, acceptingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	service := intentservice.New(staticResolver{repository: repository})
	admission, err := service.Propose(context.Background(), "repo_123", intentservice.Proposal{
		IdempotencyKey: "request-pending",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	pending, err := service.PendingEvaluations(context.Background(), "repo_123", intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list pending evaluations: %v", err)
	}
	if len(pending.Versions) != 1 || pending.Versions[0].ID != admission.Version.ID {
		t.Fatalf("pending evaluations = %#v, want admitted version", pending.Versions)
	}
}

func TestServiceAdmissionLeavesTrunkProjectionUnchanged(t *testing.T) {
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	projection := &recordingProjection{current: initialContent}
	repository, err := intent.NewEphemeralRepository(initialContent, acceptingAdmission{}, projection)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	service := intentservice.New(staticResolver{repository: repository})
	initial := repository.CurrentIntent()

	admission, err := service.Propose(context.Background(), "repo_123", intentservice.Proposal{
		IdempotencyKey: "request-held",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if admission.Version.ID == "" {
		t.Fatal("held proposal was not admitted")
	}
	if got := repository.CurrentIntent(); got != initial {
		t.Fatalf("current intent = %#v, want unchanged %#v", got, initial)
	}
	if projection.current != initialContent {
		t.Fatalf("trunk projection = %#v, want %#v", projection.current, initialContent)
	}
}

func TestServiceAmendmentRemainsPendingUntilExplicitPromotion(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewEphemeralRepository(initialContent, acceptingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	original, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose original: %v", err)
	}
	service := intentservice.New(staticResolver{repository: repository})
	amended, err := service.Amend(ctx, "repo_123", intentservice.AmendmentRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        original.Change.ID,
		ExpectedVersion: original.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository",
		Rationale:       "timeout path could duplicate the operation",
	})
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	if amended.Version.ChangeID != original.Change.ID || amended.Amendment.FromVersion != original.Version.ID {
		t.Fatalf("amendment = %#v, want next version of original change", amended)
	}
	if got := repository.CurrentIntent().Content; got != initialContent {
		t.Fatalf("current content = %#v, want amendment to remain pending", got)
	}
	pending, err := service.PendingEvaluations(ctx, "repo_123", intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list pending evaluations: %v", err)
	}
	if len(pending.Versions) != 1 || pending.Versions[0].ID != amended.Version.ID {
		t.Fatalf("pending evaluations = %#v, want only amended version", pending.Versions)
	}
	promoted, err := service.Promote(ctx, "repo_123", intent.PromoteRequest{
		VersionID:      amended.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote amendment: %v", err)
	}
	if promoted.Promotion.VersionID != amended.Version.ID || repository.CurrentIntent().Content != amended.Version.Content {
		t.Fatalf("promoted amendment = %#v, want amended version current", promoted)
	}
}

func TestServiceAcceptsParentAmendmentWithoutPromotingDependentOnSupersededVersion(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewEphemeralRepository(initialContent, acceptingAdmission{}, &recordingProjection{current: initialContent})
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
	service := intentservice.New(staticResolver{repository: repository})

	amended, err := service.Amend(ctx, "repo_123", intentservice.AmendmentRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        parent.Change.ID,
		ExpectedVersion: parent.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository-agent",
		Rationale:       "repair B",
	})
	if err != nil {
		t.Fatalf("amend parent through service: %v", err)
	}
	promoted, err := service.Promote(ctx, "repo_123", intent.PromoteRequest{
		VersionID:      amended.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote parent amendment: %v", err)
	}
	if repository.CurrentIntent().Content != amended.Version.Content {
		t.Fatalf("accepted amendment = %#v, want B prime as current intent", promoted)
	}
	inspection, err := repository.InspectChange(ctx, dependent.Change.ID)
	if err != nil {
		t.Fatalf("inspect dependent: %v", err)
	}
	if inspection.LatestVersion.ID != dependent.Version.ID || inspection.LatestPromotion != nil {
		t.Fatalf("dependent after parent amendment = %#v, want unchanged held C", inspection)
	}
	candidates, err := repository.DependentReconciliations(ctx)
	if err != nil {
		t.Fatalf("read dependent reconciliations: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Dependent.Version.ID != dependent.Version.ID {
		t.Fatalf("dependent reconciliations = %#v, want held C", candidates)
	}
}

func TestServiceSendsReconciledDependentThroughOrdinaryEvaluation(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewEphemeralRepository(initialContent, acceptingAdmission{}, &recordingProjection{current: initialContent})
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
	request := intentservice.DependentReconciliationRequest{
		IdempotencyKey:     "reconcile-c",
		ExpectedVersion:    dependent.Version.ID,
		ReplacedDependency: parent.Version.ID,
		AcceptedVersion:    amended.Version.ID,
		ExpectedIntent:     promoted.Intent.ID,
		Content:            intent.ContentRef{Engine: "git", Revision: "c2c2c2c2"},
		Producer:           "git-engine",
		Rationale:          "replay C onto accepted B prime",
	}

	service := intentservice.New(staticResolver{repository: repository})
	held, err := service.ReconcileDependent(ctx, "repo_123", request)
	if err != nil {
		t.Fatalf("reconcile held dependent: %v", err)
	}
	if held.Version.ChangeID != dependent.Change.ID {
		t.Fatalf("held reconciliation = %#v, want durable C prime awaiting evaluation", held)
	}
	if got := repository.CurrentIntent(); got != promoted.Intent {
		t.Fatalf("intent after deferred evaluation = %#v, want %#v", got, promoted.Intent)
	}

	retried, err := service.ReconcileDependent(ctx, "repo_123", request)
	if err != nil {
		t.Fatalf("reconsider reconciled dependent: %v", err)
	}
	if retried.Version.ID != held.Version.ID {
		t.Fatalf("retried version = %q, want durable version %q", retried.Version.ID, held.Version.ID)
	}
	accepted, err := service.Promote(ctx, "repo_123", intent.PromoteRequest{
		VersionID:      held.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote reconciled dependent: %v", err)
	}
	if accepted.Promotion.VersionID != held.Version.ID {
		t.Fatalf("accepted promotion = %#v, want reconciled dependent", accepted)
	}
}

func TestServiceSendsRebasedHeldVersionThroughOrdinaryEvaluation(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewEphemeralRepository(initialContent, acceptingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	service := intentservice.New(staticResolver{repository: repository})
	proposed, err := service.Propose(ctx, "repo_123", intentservice.Proposal{
		IdempotencyKey: "proposal-held",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose held change: %v", err)
	}
	unrelated, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-current",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:       "maintainer",
	})
	if err != nil {
		t.Fatalf("propose current change: %v", err)
	}
	current, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      unrelated.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote current change: %v", err)
	}

	request := intentservice.HeldVersionRebaseRequest{
		IdempotencyKey:  "rebase-held",
		ExpectedVersion: proposed.Version.ID,
		ExpectedIntent:  current.Intent.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "c2c2c2c2"},
		Producer:        "repository-engine",
		Rationale:       "replay held change onto current intent",
	}
	held, err := service.RebaseHeldVersion(ctx, "repo_123", request)
	if err != nil {
		t.Fatalf("rebase held version: %v", err)
	}
	if held.Version.ChangeID != proposed.Change.ID {
		t.Fatalf("held rebased version = %#v, want same Change awaiting evaluation", held)
	}

	retried, err := service.RebaseHeldVersion(ctx, "repo_123", request)
	if err != nil {
		t.Fatalf("reconsider rebased held version: %v", err)
	}
	if retried.Version.ID != held.Version.ID {
		t.Fatalf("retried held version rebase = %#v, want same durable version", retried)
	}
	accepted, err := service.Promote(ctx, "repo_123", intent.PromoteRequest{
		VersionID:      held.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote rebased held version: %v", err)
	}
	if accepted.Promotion.VersionID != held.Version.ID {
		t.Fatalf("accepted held version rebase = %#v, want explicitly promoted version", accepted)
	}
}

func TestServiceRetryReturnsTheSamePendingAdmission(t *testing.T) {
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewEphemeralRepository(initialContent, acceptingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	service := intentservice.New(staticResolver{repository: repository})
	initial := repository.CurrentIntent()

	proposal := intentservice.Proposal{
		IdempotencyKey: "request-decision-failure",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	}
	admission, err := service.Propose(context.Background(), "repo_123", proposal)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if admission.Version.ID == "" {
		t.Fatal("admission did not create a durable version")
	}
	if got := repository.CurrentIntent(); got != initial {
		t.Fatalf("current intent = %#v, want unchanged %#v", got, initial)
	}
	retried, err := service.Propose(context.Background(), "repo_123", proposal)
	if err != nil {
		t.Fatalf("retry proposal: %v", err)
	}
	if retried.Version.ID != admission.Version.ID {
		t.Fatalf("retried admission = %#v, want original pending version", retried)
	}
}

type staticResolver struct {
	repository *intent.Repository
}

func (resolver staticResolver) Resolve(context.Context, string) (intentservice.Repository, error) {
	return resolver.repository, nil
}

func (resolver staticResolver) Bootstrap(_ context.Context, _ string, content intent.ContentRef) (intent.Revision, error) {
	current := resolver.repository.CurrentIntent()
	if current.Content != content {
		return intent.Revision{}, intentservice.ErrRepositoryAlreadyInitialized
	}
	return current, nil
}

type acceptingAdmission struct{}

func (acceptingAdmission) Admit(context.Context, intent.VersionID, intent.ContentRef) error {
	return nil
}

type recordingProjection struct {
	current intent.ContentRef
}

func (projection *recordingProjection) Current(context.Context) (intent.ContentRef, error) {
	return projection.current, nil
}

func (projection *recordingProjection) Advance(_ context.Context, _ intent.ContentRef, next intent.ContentRef) error {
	projection.current = next
	return nil
}
