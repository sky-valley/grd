// Package evaluatorprotocol defines GRD's provider-neutral evaluator wire
// contract. One request and one result are exchanged as JSON values.
package evaluatorprotocol

const RequestSchema = "grd.evaluator-request/v1"
const ResultSchema = "grd.evaluator-result/v1"

// Policy is the single accepted policy interpreted by one evaluator process.
type Policy struct {
	Name        string `json:"name"`
	Instruction string `json:"instruction"`
	Assignee    string `json:"assignee"`
}

// Request contains the complete immutable input for one policy evaluation.
type Request struct {
	Schema           string `json:"schema"`
	Repository       string `json:"repository"`
	Version          string `json:"version"`
	GoverningIntent  string `json:"governingIntent"`
	Purpose          string `json:"purpose"`
	Priorities       string `json:"priorities"`
	ChangeEvidence   string `json:"changeEvidence"`
	EvaluationPolicy Policy `json:"evaluationPolicy"`
}

// Provenance identifies the interpreter and its contract revision.
type Provenance struct {
	Evaluator        string `json:"evaluator"`
	ContractRevision string `json:"contractRevision"`
}

// Result is one evaluator's evidence-backed interpretation of a Policy.
type Result struct {
	Schema         string     `json:"schema"`
	RequiresAction bool       `json:"requiresAction"`
	Reason         string     `json:"reason"`
	Evidence       []string   `json:"evidence"`
	Provenance     Provenance `json:"provenance"`
}
