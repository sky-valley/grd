package controlhttp

import (
	"context"
	"strings"

	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/intentservice"
)

const IntentSchema = "grd.intent/v1"
const ProposalSchema = "grd.proposal/v1"
const ProposalReceiptSchema = "grd.proposal-receipt/v1"
const VersionSchema = "grd.version/v1"
const RequirementsSchema = "grd.requirements/v1"
const RequirementResponseSchema = "grd.requirement-response/v1"
const RequirementResponseReceiptSchema = "grd.requirement-response-receipt/v1"
const HistorySchema = "grd.history/v1"
const HistoryFactSchema = "grd.history-fact/v1"
const ChangeSchema = "grd.change/v1"
const AmendmentSchema = "grd.amendment/v1"
const AmendmentReceiptSchema = "grd.amendment-receipt/v1"
const HeldVersionRebaseSchema = "grd.held-version-rebase/v1"
const HeldVersionRebaseReceiptSchema = "grd.held-version-rebase-receipt/v1"
const DependentReconciliationSchema = "grd.dependent-reconciliation/v1"
const DependentReconciliationReceiptSchema = "grd.dependent-reconciliation-receipt/v1"
const ReconciliationConflictSchema = "grd.reconciliation-conflict/v1"
const ReconciliationConflictReceiptSchema = "grd.reconciliation-conflict-receipt/v1"
const ReconciliationResolutionSchema = "grd.reconciliation-resolution/v1"
const ReconciliationResolutionReceiptSchema = "grd.reconciliation-resolution-receipt/v1"
const errorSchema = "grd.error/v1"

type Service interface {
	CurrentIntent(ctx context.Context, repositoryID string) (intent.Revision, error)
	Propose(ctx context.Context, repositoryID string, proposal intentservice.Proposal) (intent.Proposed, error)
	Version(ctx context.Context, repositoryID string, versionID intent.VersionID) (intent.Version, bool, error)
	Evaluation(ctx context.Context, repositoryID string, versionID intent.VersionID) (intent.Evaluation, bool, error)
	Requirements(ctx context.Context, repositoryID string, versionID intent.VersionID) ([]intent.Requirement, error)
	PendingRequirements(ctx context.Context, repositoryID string, query intent.PendingRequirementQuery) (intent.PendingRequirementPage, error)
	RecordRequirementResponse(ctx context.Context, repositoryID string, request intent.RequirementResponseRequest) (intent.RequirementResponse, error)
	Promotion(ctx context.Context, repositoryID string, versionID intent.VersionID) (intent.Promoted, bool, error)
	History(ctx context.Context, repositoryID string, query intent.HistoryQuery) (intent.HistoryPage, error)
	InspectChange(ctx context.Context, repositoryID string, changeID intent.ChangeID) (intent.ChangeInspection, error)
	Amend(ctx context.Context, repositoryID string, request intentservice.AmendmentRequest) (intent.Amended, error)
	RebaseHeldVersion(ctx context.Context, repositoryID string, request intentservice.HeldVersionRebaseRequest) (intent.RebasedHeldVersion, error)
	ReconcileDependent(ctx context.Context, repositoryID string, request intentservice.DependentReconciliationRequest) (intent.ReconciledDependent, error)
	RecordReconciliationConflict(ctx context.Context, repositoryID string, request intentservice.ReconciliationConflictRequest) (intent.ReconciliationConflictInspection, error)
	ResolveReconciliationConflict(ctx context.Context, repositoryID string, request intentservice.ReconciliationResolutionRequest) (intent.ResolvedReconciliationConflict, error)
}

type Config struct {
	Repository string
	Producer   string
}

type IntentFact struct {
	Schema         string  `json:"schema"`
	Repository     string  `json:"repository"`
	Intent         string  `json:"intent"`
	PreviousIntent string  `json:"previousIntent,omitempty"`
	Content        Content `json:"content"`
}

type Content struct {
	Engine   string `json:"engine"`
	Revision string `json:"revision"`
}

type ProposalRequest struct {
	Schema       string   `json:"schema"`
	BaseIntent   string   `json:"baseIntent"`
	Content      Content  `json:"content"`
	Dependencies []string `json:"dependencies,omitempty"`
}

type ProposalReceipt struct {
	Schema     string      `json:"schema"`
	Repository string      `json:"repository"`
	Change     ChangeFact  `json:"change"`
	Version    VersionFact `json:"version"`
}

type ChangeFact struct {
	ID string `json:"id"`
}

type VersionFact struct {
	ID           string   `json:"id"`
	Change       string   `json:"change"`
	BaseIntent   string   `json:"baseIntent"`
	Content      Content  `json:"content"`
	Producer     string   `json:"producer"`
	Dependencies []string `json:"dependencies,omitempty"`
}

type VersionInspection struct {
	Schema       string            `json:"schema"`
	Repository   string            `json:"repository"`
	Version      VersionFact       `json:"version"`
	Evaluation   *EvaluationFact   `json:"evaluation,omitempty"`
	Requirements []RequirementFact `json:"requirements,omitempty"`
	Promotion    *PromotionFact    `json:"promotion,omitempty"`
}

type EvaluationFact struct {
	Version         string                 `json:"version"`
	GoverningIntent string                 `json:"governingIntent"`
	Policies        []PolicyEvaluationFact `json:"policies"`
}

type PolicyEvaluationFact struct {
	Policy         string         `json:"policy"`
	Instruction    string         `json:"instruction"`
	Assignee       string         `json:"assignee"`
	RequiresAction bool           `json:"requiresAction"`
	Reason         string         `json:"reason"`
	Evidence       []string       `json:"evidence"`
	Provenance     ProvenanceFact `json:"provenance"`
}

type ProvenanceFact struct {
	Evaluator        string `json:"evaluator"`
	ContractRevision string `json:"contractRevision"`
}

type RequirementFact struct {
	Version        string                   `json:"version,omitempty"`
	Policy         string                   `json:"policy"`
	Assignee       string                   `json:"assignee"`
	Reason         string                   `json:"reason"`
	Evidence       []string                 `json:"evidence"`
	LatestResponse *RequirementResponseFact `json:"latestResponse,omitempty"`
}

type RequirementResponseFact struct {
	ID        string `json:"id"`
	Version   string `json:"version,omitempty"`
	Policy    string `json:"policy,omitempty"`
	Assignee  string `json:"assignee"`
	Decision  string `json:"decision"`
	Rationale string `json:"rationale"`
}

type RequirementsPage struct {
	Schema       string            `json:"schema"`
	Repository   string            `json:"repository"`
	Requirements []RequirementFact `json:"requirements"`
	NextCursor   string            `json:"nextCursor,omitempty"`
}

type RequirementResponseRequest struct {
	Schema    string `json:"schema"`
	Version   string `json:"version"`
	Policy    string `json:"policy"`
	Decision  string `json:"decision"`
	Rationale string `json:"rationale"`
}

type RequirementResponseReceipt struct {
	Schema     string                  `json:"schema"`
	Repository string                  `json:"repository"`
	Response   RequirementResponseFact `json:"response"`
}

type HistoryPage struct {
	Schema     string         `json:"schema"`
	Repository string         `json:"repository"`
	Facts      []HistoryEntry `json:"facts"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

type HistoryEntry struct {
	Cursor                  string                        `json:"cursor"`
	Kind                    string                        `json:"kind"`
	Intent                  *HistoryIntentFact            `json:"intent,omitempty"`
	Change                  *ChangeFact                   `json:"change,omitempty"`
	Version                 *VersionFact                  `json:"version,omitempty"`
	Evaluation              *EvaluationFact               `json:"evaluation,omitempty"`
	Response                *RequirementResponseFact      `json:"response,omitempty"`
	Promotion               *PromotionFact                `json:"promotion,omitempty"`
	Amendment               *AmendmentFact                `json:"amendment,omitempty"`
	DependentReconciliation *DependentReconciliationFact  `json:"dependentReconciliation,omitempty"`
	HeldVersionRebase       *HeldVersionRebaseFact        `json:"heldVersionRebase,omitempty"`
	Conflict                *ReconciliationConflictFact   `json:"conflict,omitempty"`
	Resolution              *ReconciliationResolutionFact `json:"resolution,omitempty"`
}

type HistoryFactEnvelope struct {
	Schema     string       `json:"schema"`
	Repository string       `json:"repository"`
	Fact       HistoryEntry `json:"fact"`
}

type HistoryIntentFact struct {
	ID         string  `json:"id"`
	PreviousID string  `json:"previousId,omitempty"`
	Content    Content `json:"content"`
}

type AmendmentFact struct {
	FromVersion string `json:"fromVersion"`
	ToVersion   string `json:"toVersion"`
	Rationale   string `json:"rationale"`
}

type DependentReconciliationFact struct {
	FromVersion        string `json:"fromVersion"`
	ToVersion          string `json:"toVersion"`
	ReplacedDependency string `json:"replacedDependency"`
	AcceptedVersion    string `json:"acceptedVersion"`
	BaseIntent         string `json:"baseIntent"`
	Rationale          string `json:"rationale"`
}

type HeldVersionRebaseFact struct {
	FromVersion string `json:"fromVersion"`
	ToVersion   string `json:"toVersion"`
	FromIntent  string `json:"fromIntent"`
	ToIntent    string `json:"toIntent"`
	Rationale   string `json:"rationale"`
}

type ReconciliationConflictFact struct {
	ID            string      `json:"id"`
	Change        ChangeFact  `json:"change"`
	Version       VersionFact `json:"version"`
	FromVersion   string      `json:"fromVersion"`
	ToVersion     string      `json:"toVersion"`
	BaseIntent    string      `json:"baseIntent"`
	ReportedBy    string      `json:"reportedBy"`
	AffectedPaths []string    `json:"affectedPaths"`
}

type ReconciliationResolutionFact struct {
	ID          string `json:"id"`
	ConflictID  string `json:"conflict"`
	FromVersion string `json:"fromVersion"`
	ToVersion   string `json:"toVersion"`
	BaseIntent  string `json:"baseIntent"`
	ResolvedBy  string `json:"resolvedBy"`
	Rationale   string `json:"rationale"`
}

type ChangeInspection struct {
	Schema          string         `json:"schema"`
	Repository      string         `json:"repository"`
	Change          ChangeFact     `json:"change"`
	LatestVersion   VersionFact    `json:"latestVersion"`
	LatestAmendment *AmendmentFact `json:"latestAmendment,omitempty"`
	LatestPromotion *PromotionFact `json:"latestPromotion,omitempty"`
}

type AmendmentRequest struct {
	Schema          string  `json:"schema"`
	Change          string  `json:"change"`
	ExpectedVersion string  `json:"expectedVersion"`
	Content         Content `json:"content"`
	Rationale       string  `json:"rationale"`
}

type AmendmentReceipt struct {
	Schema     string        `json:"schema"`
	Repository string        `json:"repository"`
	Change     ChangeFact    `json:"change"`
	Version    VersionFact   `json:"version"`
	Amendment  AmendmentFact `json:"amendment"`
}

type HeldVersionRebaseRequest struct {
	Schema          string  `json:"schema"`
	ExpectedVersion string  `json:"expectedVersion"`
	ExpectedIntent  string  `json:"expectedIntent"`
	Content         Content `json:"content"`
	Rationale       string  `json:"rationale"`
}

type HeldVersionRebaseReceipt struct {
	Schema     string                `json:"schema"`
	Repository string                `json:"repository"`
	Change     ChangeFact            `json:"change"`
	Version    VersionFact           `json:"version"`
	Rebase     HeldVersionRebaseFact `json:"rebase"`
}

type DependentReconciliationRequest struct {
	Schema             string  `json:"schema"`
	ExpectedVersion    string  `json:"expectedVersion"`
	ReplacedDependency string  `json:"replacedDependency"`
	AcceptedVersion    string  `json:"acceptedVersion"`
	ExpectedIntent     string  `json:"expectedIntent"`
	Content            Content `json:"content"`
	Rationale          string  `json:"rationale"`
}

type DependentReconciliationReceipt struct {
	Schema         string                      `json:"schema"`
	Repository     string                      `json:"repository"`
	Change         ChangeFact                  `json:"change"`
	Version        VersionFact                 `json:"version"`
	Reconciliation DependentReconciliationFact `json:"reconciliation"`
}

type ReconciliationConflictRequest struct {
	Schema            string   `json:"schema"`
	FromVersion       string   `json:"fromVersion"`
	ToVersion         string   `json:"toVersion"`
	DescendantVersion string   `json:"descendantVersion"`
	ExpectedIntent    string   `json:"expectedIntent,omitempty"`
	AffectedPaths     []string `json:"affectedPaths"`
}

type ReconciliationConflictReceipt struct {
	Schema     string                     `json:"schema"`
	Repository string                     `json:"repository"`
	Conflict   ReconciliationConflictFact `json:"conflict"`
}

type ReconciliationResolutionRequest struct {
	Schema          string  `json:"schema"`
	Conflict        string  `json:"conflict"`
	ExpectedVersion string  `json:"expectedVersion"`
	ExpectedIntent  string  `json:"expectedIntent"`
	Content         Content `json:"content"`
	Rationale       string  `json:"rationale"`
}

type ReconciliationResolutionReceipt struct {
	Schema     string                       `json:"schema"`
	Repository string                       `json:"repository"`
	Change     ChangeFact                   `json:"change"`
	Version    VersionFact                  `json:"version"`
	Resolution ReconciliationResolutionFact `json:"resolution"`
}

type PromotionFact struct {
	ID         string `json:"id"`
	FromIntent string `json:"fromIntent"`
	ToIntent   string `json:"toIntent"`
	Version    string `json:"version"`
}

type errorFact struct {
	Schema  string `json:"schema"`
	Message string `json:"message"`
}

func canonicalText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\x00\r\n")
}
