package ledgerfs_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/ledgerfs"
)

func TestHistoryCursorAndFactsSurviveLedgerRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	firstLedger, err := ledgerfs.Open(path)
	if err != nil {
		t.Fatalf("open first ledger: %v", err)
	}
	first, err := intent.OpenRepository(ctx, initial, firstLedger, &recordingAdmission{}, &recordingProjection{current: initial})
	if err != nil {
		t.Fatalf("open first repository: %v", err)
	}
	proposed, err := first.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-one",
		BaseIntent:     first.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "local:ion",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := first.RecordEvaluation(ctx, intent.Evaluation{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		PolicyEvaluations: []intent.PolicyEvaluation{{
			Policy:      "architecture",
			Instruction: "Does the boundary remain sound?",
			Assignee:    "local:ion",
			Reason:      "The boundary remains sound.",
			Evidence:    []string{"history_test.go"},
		}},
	}); err != nil {
		t.Fatalf("record Evaluation: %v", err)
	}
	if _, err := first.Promote(ctx, intent.PromoteRequest{VersionID: proposed.Version.ID, ExpectedIntent: proposed.Version.BaseIntent}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	before, err := first.History(ctx, intent.HistoryQuery{Limit: 100})
	if err != nil {
		t.Fatalf("history before restart: %v", err)
	}
	if got, semanticCount := before.Facts[len(before.Facts)-1].Cursor, intent.HistoryCursor(len(before.Facts)); got <= semanticCount {
		t.Fatalf("Promotion cursor = %d, want append-only journal position beyond %d semantic facts", got, semanticCount)
	}
	if err := firstLedger.Close(); err != nil {
		t.Fatalf("close first ledger: %v", err)
	}

	secondLedger, err := ledgerfs.Open(path)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = secondLedger.Close() })
	second, err := intent.OpenRepository(ctx, initial, secondLedger, &recordingAdmission{}, &recordingProjection{current: initial})
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	after, err := second.History(ctx, intent.HistoryQuery{Limit: 100})
	if err != nil {
		t.Fatalf("history after restart: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("history after restart = %#v, want %#v", after, before)
	}
}
