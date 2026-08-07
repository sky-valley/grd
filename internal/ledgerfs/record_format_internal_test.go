package ledgerfs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sky-valley/grd/internal/intent"
)

func TestJournalV1RecordKindsAreFrozen(t *testing.T) {
	records := journalV1RecordSamples()
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatalf("encode %q: %v", record.Kind, err)
		}
	}
	fixture, err := os.ReadFile(filepath.Join("testdata", "journal_v1_records.jsonl"))
	if err != nil {
		t.Fatalf("read v1 record fixture: %v", err)
	}
	if !bytes.Equal(encoded.Bytes(), fixture) {
		t.Fatalf("journal v1 record formats changed\ngot:\n%s\nwant:\n%s", encoded.Bytes(), fixture)
	}

	scanner := bufio.NewScanner(bytes.NewReader(fixture))
	for _, want := range records {
		if !scanner.Scan() {
			t.Fatalf("v1 record fixture ended before %q", want.Kind)
		}
		var got journalRecord
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&got); err != nil {
			t.Fatalf("decode frozen %q record: %v", want.Kind, err)
		}
		var trailing json.RawMessage
		if err := decoder.Decode(&trailing); err != io.EOF {
			t.Fatalf("decode frozen %q trailing data: %v", want.Kind, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("decoded frozen %q = %#v, want %#v", want.Kind, got, want)
		}
	}
	if scanner.Scan() {
		t.Fatalf("v1 record fixture has unexpected extra record: %s", scanner.Bytes())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan v1 record fixture: %v", err)
	}
}

func journalV1RecordSamples() []journalRecord {
	initial := intent.Revision{ID: "intent_initial", Content: intent.ContentRef{Engine: "fixture", Revision: "content_a"}}
	next := intent.Revision{ID: "intent_next", PreviousID: initial.ID, Content: intent.ContentRef{Engine: "fixture", Revision: "content_b"}}
	change := intent.Change{ID: "change_one"}
	version := intent.Version{
		ID:           "version_one",
		ChangeID:     change.ID,
		BaseIntent:   initial.ID,
		Content:      next.Content,
		Producer:     "principal:author",
		Dependencies: []intent.VersionID{"version_dependency"},
	}
	secondVersion := intent.Version{
		ID:           "version_two",
		ChangeID:     change.ID,
		BaseIntent:   next.ID,
		Content:      intent.ContentRef{Engine: "fixture", Revision: "content_c"},
		Producer:     "principal:repository",
		Dependencies: []intent.VersionID{"version_accepted"},
	}
	promotion := intent.Promotion{ID: "promotion_one", FromIntent: initial.ID, ToIntent: next.ID, VersionID: version.ID}
	amendment := intent.Amendment{FromVersion: version.ID, ToVersion: secondVersion.ID, Rationale: "adapt the proposal"}
	reconciliation := intent.DependentReconciliation{
		FromVersion:        version.ID,
		ToVersion:          secondVersion.ID,
		ReplacedDependency: "version_dependency",
		AcceptedVersion:    "version_accepted",
		BaseIntent:         next.ID,
		Rationale:          "follow accepted dependency",
	}
	rebase := intent.HeldVersionRebase{
		FromVersion: version.ID,
		ToVersion:   secondVersion.ID,
		FromIntent:  initial.ID,
		ToIntent:    next.ID,
		Rationale:   "follow current Intent",
	}
	conflict := intent.ReconciliationConflict{
		ID:            "conflict_one",
		Change:        change,
		Version:       version,
		FromVersion:   "version_dependency",
		ToVersion:     "version_accepted",
		BaseIntent:    next.ID,
		ReportedBy:    "principal:author",
		AffectedPaths: []string{"internal/example.go"},
	}
	resolution := intent.ReconciliationResolution{
		ID:          "resolution_one",
		ConflictID:  conflict.ID,
		FromVersion: version.ID,
		ToVersion:   secondVersion.ID,
		BaseIntent:  next.ID,
		ResolvedBy:  "principal:repository",
		Rationale:   "reconcile accepted content",
	}
	evaluation := intent.Evaluation{
		VersionID:       version.ID,
		GoverningIntent: initial.ID,
		PolicyEvaluations: []intent.PolicyEvaluation{{
			Policy:         "architecture",
			Instruction:    "Check the architecture boundary.",
			Assignee:       "principal:architect",
			Provenance:     intent.EvaluatorProvenance{Evaluator: "fixture-evaluator", ContractRevision: "fixture/v1"},
			RequiresAction: true,
			Reason:         "the change crosses a boundary",
			Evidence:       []string{"internal/example.go"},
		}},
	}
	response := intent.RequirementResponse{
		ID:        "response_one",
		VersionID: version.ID,
		Policy:    "architecture",
		Assignee:  "principal:architect",
		Decision:  intent.RequirementSatisfied,
		Rationale: "the boundary is intentional",
	}
	return []journalRecord{
		{Format: journalFormat, Kind: repositoryInitialized, Initial: &initial},
		{Format: journalFormat, Kind: proposalRecorded, IdempotencyKey: "proposal-key", Change: &change, Version: &version},
		{Format: journalFormat, Kind: amendmentRecorded, IdempotencyKey: "amendment-key", Amendment: &amendment, Version: &secondVersion},
		{Format: journalFormat, Kind: dependentReconciliationRecorded, IdempotencyKey: "reconciliation-key", DependentReconciliation: &reconciliation, Version: &secondVersion},
		{Format: journalFormat, Kind: heldVersionRebaseRecorded, IdempotencyKey: "rebase-key", HeldVersionRebase: &rebase, Version: &secondVersion},
		{Format: journalFormat, Kind: promotionPrepared, Promotion: &promotion, NextIntent: &next},
		{Format: journalFormat, Kind: promotionCompleted, PromotionID: promotion.ID},
		{Format: journalFormat, Kind: reconciliationConflictRecorded, IdempotencyKey: "conflict-key", ReconciliationConflict: &conflict},
		{Format: journalFormat, Kind: reconciliationResolutionRecorded, IdempotencyKey: "resolution-key", ReconciliationResolution: &resolution, Version: &secondVersion},
		{Format: journalFormat, Kind: evaluationRecorded, Evaluation: &evaluation},
		{Format: journalFormat, Kind: requirementResponseRecorded, IdempotencyKey: "response-key", RequirementResponse: &response},
	}
}
