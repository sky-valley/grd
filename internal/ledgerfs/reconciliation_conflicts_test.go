package ledgerfs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/ledgerfs"
)

func TestLedgerRestoresReconciliationConflictIdentityAndIdempotency(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	ledger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	projection := &recordingProjection{current: initialContent}
	repository, err := intent.OpenRepository(ctx, initialContent, ledger, &recordingAdmission{}, projection)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	original, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     repository.CurrentIntent().ID,
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
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote B prime: %v", err)
	}
	descendant, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-c",
		BaseIntent:     original.Version.BaseIntent,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose C: %v", err)
	}
	request := intent.ReconciliationConflictRequest{
		IdempotencyKey:    "conflict-b-c",
		FromVersion:       original.Version.ID,
		ToVersion:         amended.Version.ID,
		DescendantVersion: descendant.Version.ID,
		ExpectedIntent:    repository.CurrentIntent().ID,
		ReportedBy:        "contributor",
	}
	recorded, err := repository.RecordReconciliationConflict(ctx, request)
	if err != nil {
		t.Fatalf("record conflict: %v", err)
	}
	secondDescendant, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-d",
		BaseIntent:     original.Version.BaseIntent,
		Content:        intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose D: %v", err)
	}
	secondRecorded, err := repository.RecordReconciliationConflict(ctx, intent.ReconciliationConflictRequest{
		IdempotencyKey:    "conflict-b-d",
		FromVersion:       original.Version.ID,
		ToVersion:         amended.Version.ID,
		DescendantVersion: secondDescendant.Version.ID,
		ExpectedIntent:    repository.CurrentIntent().ID,
		ReportedBy:        "contributor",
	})
	if err != nil {
		t.Fatalf("record second conflict: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}
	journal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read valid conflict journal: %v", err)
	}
	for name, paths := range map[string][]string{
		"empty":     {""},
		"duplicate": {"same", "same"},
		"unsorted":  {"z", "a"},
		"oversized": {strings.Repeat("x", 4097)},
	} {
		t.Run("reject malformed replay "+name, func(t *testing.T) {
			encodedPaths, err := json.Marshal(paths)
			if err != nil {
				t.Fatalf("encode malformed paths: %v", err)
			}
			corrupt := bytes.Replace(journal, []byte(`"AffectedPaths":[]`), append([]byte(`"AffectedPaths":`), encodedPaths...), 1)
			if bytes.Equal(corrupt, journal) {
				t.Fatal("valid journal did not contain empty affected paths")
			}
			corruptPath := filepath.Join(t.TempDir(), "ledger.jsonl")
			if err := os.WriteFile(corruptPath, corrupt, 0o600); err != nil {
				t.Fatalf("write malformed journal: %v", err)
			}
			if reopened, err := ledgerfs.Open(corruptPath); err == nil {
				_ = reopened.Close()
				t.Fatal("opened journal with malformed reconciliation diagnostics")
			}
		})
	}

	reopened, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := intent.OpenRepository(ctx, initialContent, reopened, &recordingAdmission{}, &recordingProjection{current: promoted.Intent.Content})
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	loaded, found, err := restarted.ReconciliationConflict(ctx, recorded.ID)
	if err != nil || !found {
		t.Fatalf("restored conflict = %#v, %t, %v; want recorded", loaded, found, err)
	}
	if !reflect.DeepEqual(loaded, recorded) {
		t.Fatalf("restored conflict = %#v, want %#v", loaded, recorded)
	}
	firstPage, err := restarted.ReconciliationConflicts(ctx, intent.ReconciliationConflictQuery{Limit: 1})
	if err != nil {
		t.Fatalf("list first restored conflict page: %v", err)
	}
	if len(firstPage.Conflicts) != 1 || !reflect.DeepEqual(firstPage.Conflicts[0], recorded) || firstPage.NextCursor != recorded.ID {
		t.Fatalf("first restored page = %#v, want first conflict and cursor %q", firstPage, recorded.ID)
	}
	secondPage, err := restarted.ReconciliationConflicts(ctx, intent.ReconciliationConflictQuery{
		After: firstPage.NextCursor,
		Limit: 1,
	})
	if err != nil {
		t.Fatalf("list second restored conflict page: %v", err)
	}
	if len(secondPage.Conflicts) != 1 || !reflect.DeepEqual(secondPage.Conflicts[0], secondRecorded) || secondPage.NextCursor != "" {
		t.Fatalf("second restored page = %#v, want second conflict and no cursor", secondPage)
	}
	retried, err := restarted.RecordReconciliationConflict(ctx, request)
	if err != nil {
		t.Fatalf("retry restored conflict: %v", err)
	}
	if !reflect.DeepEqual(retried, recorded) {
		t.Fatalf("restored retry = %#v, want %#v", retried, recorded)
	}
	if _, err := restarted.Propose(ctx, intent.Proposal{
		IdempotencyKey: request.IdempotencyKey,
		BaseIntent:     restarted.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:       "contributor",
	}); !errors.Is(err, intent.ErrIdempotencyConflict) {
		t.Fatalf("restored conflict key reused for proposal error = %v, want ErrIdempotencyConflict", err)
	}
}
