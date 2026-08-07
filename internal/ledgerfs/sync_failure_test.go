package ledgerfs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sky-valley/grd/internal/intent"
)

func TestLedgerRollsBackCompleteRecordWhenSyncFails(t *testing.T) {
	ctx := context.Background()
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	ledger, err := Open(journalPath)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	initial := intent.Revision{
		ID:      "intent_initial",
		Content: intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"},
	}
	if err := ledger.Initialize(ctx, initial); err != nil {
		t.Fatalf("initialize ledger: %v", err)
	}
	change := intent.Change{ID: "change_1"}
	version := intent.Version{
		ID:         "version_1",
		ChangeID:   change.ID,
		BaseIntent: initial.ID,
		Content:    intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:   "contributor",
	}
	if err := ledger.RecordProposal(ctx, "request-1", change, version); err != nil {
		t.Fatalf("record proposal: %v", err)
	}

	syncErr := errors.New("sync unavailable")
	realSync := ledger.file.Sync
	ledger.syncJournal = func() error {
		ledger.syncJournal = realSync
		return syncErr
	}
	first := intent.PreparedPromotion{
		Promotion: intent.Promotion{
			ID:         "promotion_1",
			FromIntent: initial.ID,
			ToIntent:   "intent_1",
			VersionID:  version.ID,
		},
		Intent: intent.Revision{
			ID:         "intent_1",
			PreviousID: initial.ID,
			Content:    version.Content,
		},
	}
	if err := ledger.PreparePromotion(ctx, first); !errors.Is(err, syncErr) {
		t.Fatalf("first prepare error = %v, want sync error", err)
	}
	second := intent.PreparedPromotion{
		Promotion: intent.Promotion{
			ID:         "promotion_2",
			FromIntent: initial.ID,
			ToIntent:   "intent_2",
			VersionID:  version.ID,
		},
		Intent: intent.Revision{
			ID:         "intent_2",
			PreviousID: initial.ID,
			Content:    version.Content,
		},
	}
	if err := ledger.PreparePromotion(ctx, second); err != nil {
		t.Fatalf("retry prepare: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}

	reopened, err := Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	pending, found, err := reopened.PendingPromotion(ctx)
	if err != nil || !found || pending != second {
		t.Fatalf("pending promotion after restart = %#v, %t, error %v; want %#v, true, nil", pending, found, err, second)
	}
}
