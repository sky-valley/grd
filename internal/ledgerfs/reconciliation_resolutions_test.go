package ledgerfs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/ledgerfs"
)

func TestLedgerRestoresReconciliationResolutionAndExactRetry(t *testing.T) {
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
	initial := repository.CurrentIntent()
	original, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     initial.ID,
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
		ExpectedIntent: initial.ID,
	})
	if err != nil {
		t.Fatalf("promote B prime: %v", err)
	}
	descendant, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-c",
		BaseIntent:     initial.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "contributor",
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
	})
	if err != nil {
		t.Fatalf("record conflict: %v", err)
	}
	request := intent.ResolveReconciliationConflictRequest{
		IdempotencyKey:  "resolve-b-c",
		ConflictID:      conflict.ID,
		ExpectedVersion: descendant.Version.ID,
		ExpectedIntent:  promoted.Intent.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "c2c2c2c2"},
		Producer:        "repository-agent",
		ResolvedBy:      "evaluation-agent",
		Rationale:       "replayed C onto accepted B prime",
	}
	resolved, err := repository.ResolveReconciliationConflict(ctx, request)
	if err != nil {
		t.Fatalf("resolve conflict: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}
	journal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read resolution journal: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"mismatched target version": func(record map[string]any) {
			record["reconciliation_resolution"].(map[string]any)["ToVersion"] = "version_wrong"
		},
		"stale version base": func(record map[string]any) {
			record["version"].(map[string]any)["BaseIntent"] = string(initial.ID)
		},
		"altered dependencies": func(record map[string]any) {
			record["version"].(map[string]any)["Dependencies"] = []any{"version_wrong"}
		},
		"missing resolving actor": func(record map[string]any) {
			record["reconciliation_resolution"].(map[string]any)["ResolvedBy"] = ""
		},
	} {
		t.Run("reject malformed replay "+name, func(t *testing.T) {
			lines := bytes.Split(bytes.TrimSuffix(journal, []byte("\n")), []byte("\n"))
			var record map[string]any
			if err := json.Unmarshal(lines[len(lines)-1], &record); err != nil {
				t.Fatalf("decode final resolution record: %v", err)
			}
			if record["kind"] != "reconciliation_resolution_recorded" {
				t.Fatalf("final record kind = %q, want reconciliation resolution", record["kind"])
			}
			mutate(record)
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatalf("encode malformed resolution record: %v", err)
			}
			lines[len(lines)-1] = encoded
			corruptPath := filepath.Join(t.TempDir(), "ledger.jsonl")
			if err := os.WriteFile(corruptPath, append(bytes.Join(lines, []byte("\n")), '\n'), 0o600); err != nil {
				t.Fatalf("write malformed resolution journal: %v", err)
			}
			if reopened, err := ledgerfs.Open(corruptPath); err == nil {
				_ = reopened.Close()
				t.Fatal("opened journal with malformed reconciliation resolution")
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
		t.Fatalf("restart repository: %v", err)
	}
	loaded, found, err := restarted.ReconciliationConflict(ctx, conflict.ID)
	if err != nil || !found {
		t.Fatalf("load conflict after restart = %#v, %t, %v", loaded, found, err)
	}
	if loaded.Resolution == nil || !reflect.DeepEqual(*loaded.Resolution, resolved.Resolution) {
		t.Fatalf("restored resolution = %#v, want %#v", loaded.Resolution, resolved.Resolution)
	}
	page, err := restarted.ReconciliationConflicts(ctx, intent.ReconciliationConflictQuery{Limit: 1})
	if err != nil {
		t.Fatalf("list resolved conflicts after restart: %v", err)
	}
	if len(page.Conflicts) != 1 || page.Conflicts[0].Resolution == nil ||
		!reflect.DeepEqual(*page.Conflicts[0].Resolution, resolved.Resolution) {
		t.Fatalf("restored conflict page = %#v, want resolved conflict", page)
	}
	latest, err := restarted.InspectChange(ctx, descendant.Change.ID)
	if err != nil {
		t.Fatalf("inspect resolved change after restart: %v", err)
	}
	if !reflect.DeepEqual(latest.LatestVersion, resolved.Version) {
		t.Fatalf("restored latest version = %#v, want %#v", latest.LatestVersion, resolved.Version)
	}
	retried, err := restarted.ResolveReconciliationConflict(ctx, request)
	if err != nil {
		t.Fatalf("retry resolution after restart: %v", err)
	}
	if !reflect.DeepEqual(retried, resolved) {
		t.Fatalf("retried resolution = %#v, want %#v", retried, resolved)
	}
	conflicting := request
	conflicting.ResolvedBy = "somebody-else"
	if _, err := restarted.ResolveReconciliationConflict(ctx, conflicting); !errors.Is(err, intent.ErrIdempotencyConflict) {
		t.Fatalf("conflicting retry error = %v, want ErrIdempotencyConflict", err)
	}
}
