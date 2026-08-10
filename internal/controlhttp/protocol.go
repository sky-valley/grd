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
const errorSchema = "grd.error/v1"

type Service interface {
	CurrentIntent(ctx context.Context, repositoryID string) (intent.Revision, error)
	Propose(ctx context.Context, repositoryID string, proposal intentservice.Proposal) (intent.Proposed, error)
	Version(ctx context.Context, repositoryID string, versionID intent.VersionID) (intent.Version, bool, error)
	Evaluation(ctx context.Context, repositoryID string, versionID intent.VersionID) (intent.Evaluation, bool, error)
	Requirements(ctx context.Context, repositoryID string, versionID intent.VersionID) ([]intent.Requirement, error)
	Promotion(ctx context.Context, repositoryID string, versionID intent.VersionID) (intent.Promoted, bool, error)
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
	Policy         string                   `json:"policy"`
	Assignee       string                   `json:"assignee"`
	Reason         string                   `json:"reason"`
	Evidence       []string                 `json:"evidence"`
	LatestResponse *RequirementResponseFact `json:"latestResponse,omitempty"`
}

type RequirementResponseFact struct {
	ID        string `json:"id"`
	Assignee  string `json:"assignee"`
	Decision  string `json:"decision"`
	Rationale string `json:"rationale"`
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
