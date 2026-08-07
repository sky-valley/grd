package ledgerfs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sky-valley/grd/internal/intent"
)

const journalFormat = 1
const maxRecordBytes = 1024 * 1024

// Ledger is a durable, exclusively locked projection of one repository's
// append-only decision journal.
type Ledger struct {
	mu          sync.Mutex
	file        *os.File
	syncJournal func() error
	state       journalState
	closed      bool
	failed      error
}

var _ intent.Ledger = (*Ledger)(nil)

// Open locks, recovers, validates, and replays the journal at path.
func Open(path string) (*Ledger, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("journal path is required")
	}
	if err := checkJournalLockSupport(); err != nil {
		return nil, fmt.Errorf("lock journal: %w", err)
	}
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}
	if err := lockJournal(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock journal: %w", err)
	}
	if created {
		if err := syncDirectory(filepath.Dir(path)); err != nil {
			_ = unlockJournal(file)
			_ = file.Close()
			return nil, fmt.Errorf("sync journal directory: %w", err)
		}
	}
	ledger := &Ledger{file: file, syncJournal: file.Sync, state: newJournalState()}
	if err := ledger.replay(); err != nil {
		_ = unlockJournal(file)
		_ = file.Close()
		return nil, err
	}
	return ledger, nil
}

// Close releases the journal lock and closes its file.
func (ledger *Ledger) Close() error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return nil
	}
	ledger.closed = true
	unlockErr := unlockJournal(ledger.file)
	closeErr := ledger.file.Close()
	if unlockErr != nil {
		return fmt.Errorf("unlock journal: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close journal: %w", closeErr)
	}
	return nil
}

func (ledger *Ledger) CurrentIntent(ctx context.Context) (intent.Revision, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Revision{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Revision{}, false, errors.New("journal is closed")
	}
	return ledger.state.current, ledger.state.current.ID != "", nil
}

func (ledger *Ledger) Revision(ctx context.Context, id intent.RevisionID) (intent.Revision, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Revision{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Revision{}, false, errors.New("journal is closed")
	}
	revision, found := ledger.state.revisions[id]
	return revision, found, nil
}

func (ledger *Ledger) Change(ctx context.Context, id intent.ChangeID) (intent.Change, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Change{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Change{}, false, errors.New("journal is closed")
	}
	change, found := ledger.state.changes[id]
	return change, found, nil
}

func (ledger *Ledger) Version(ctx context.Context, id intent.VersionID) (intent.Version, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Version{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Version{}, false, errors.New("journal is closed")
	}
	version, found := ledger.state.versions[id]
	return cloneVersion(version), found, nil
}

func (ledger *Ledger) Dependents(ctx context.Context, id intent.VersionID) ([]intent.Version, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return nil, errors.New("journal is closed")
	}
	ids := ledger.state.dependents[id]
	versions := make([]intent.Version, 0, len(ids))
	for _, dependentID := range ids {
		versions = append(versions, cloneVersion(ledger.state.versions[dependentID]))
	}
	return versions, nil
}

func (ledger *Ledger) LatestVersion(ctx context.Context, changeID intent.ChangeID) (intent.Version, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Version{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Version{}, false, errors.New("journal is closed")
	}
	ids := ledger.state.versionIDs[changeID]
	if len(ids) == 0 {
		return intent.Version{}, false, nil
	}
	return cloneVersion(ledger.state.versions[ids[len(ids)-1]]), true, nil
}

func (ledger *Ledger) Versions(ctx context.Context, changeID intent.ChangeID, after intent.VersionID, limit int) ([]intent.Version, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return nil, false, errors.New("journal is closed")
	}
	ids := ledger.state.versionIDs[changeID]
	start := 0
	if after != "" {
		start = -1
		for index, id := range ids {
			if id == after {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return nil, false, intent.ErrVersionNotFound
		}
	}
	end := min(start+limit, len(ids))
	versions := make([]intent.Version, 0, end-start)
	for _, id := range ids[start:end] {
		versions = append(versions, cloneVersion(ledger.state.versions[id]))
	}
	return versions, end < len(ids), nil
}

func (ledger *Ledger) PendingEvaluations(ctx context.Context, after intent.VersionID, limit int) ([]intent.Version, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return nil, false, errors.New("journal is closed")
	}
	start := 0
	if after != "" {
		start = -1
		for index, id := range ledger.state.evaluationIDs {
			if id == after {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return nil, false, intent.ErrVersionNotFound
		}
	}
	versions := make([]intent.Version, 0, limit)
	index := start
	for ; index < len(ledger.state.evaluationIDs) && len(versions) < limit; index++ {
		id := ledger.state.evaluationIDs[index]
		if _, pending := ledger.state.pendingEvaluations[id]; pending {
			versions = append(versions, cloneVersion(ledger.state.versions[id]))
		}
	}
	for ; index < len(ledger.state.evaluationIDs); index++ {
		if _, pending := ledger.state.pendingEvaluations[ledger.state.evaluationIDs[index]]; pending {
			return versions, true, nil
		}
	}
	return versions, false, nil
}

func (ledger *Ledger) ProposalByIdempotencyKey(ctx context.Context, key string) (intent.Proposed, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Proposed{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Proposed{}, false, errors.New("journal is closed")
	}
	record, found := ledger.state.idempotency[key]
	if !found {
		return intent.Proposed{}, false, nil
	}
	if record.operation != proposalOperation {
		return intent.Proposed{}, false, intent.ErrIdempotencyConflict
	}
	versionID := record.versionID
	version := cloneVersion(ledger.state.versions[versionID])
	return intent.Proposed{
		Change:  ledger.state.changes[version.ChangeID],
		Version: version,
	}, true, nil
}

func (ledger *Ledger) PendingPromotion(ctx context.Context) (intent.PreparedPromotion, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.PreparedPromotion{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.PreparedPromotion{}, false, errors.New("journal is closed")
	}
	prepared, found := ledger.state.prepared[ledger.state.pending]
	return prepared, found, nil
}

func (ledger *Ledger) CompletedPromotion(ctx context.Context, versionID intent.VersionID) (intent.Promoted, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Promoted{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Promoted{}, false, errors.New("journal is closed")
	}
	promotionID, found := ledger.state.completed[versionID]
	if !found {
		return intent.Promoted{}, false, nil
	}
	promotion := ledger.state.promotions[promotionID]
	return intent.Promoted{
		Promotion: promotion,
		Intent:    ledger.state.revisions[promotion.ToIntent],
	}, true, nil
}

func (ledger *Ledger) CompletedPromotionByIntent(ctx context.Context, intentID intent.RevisionID) (intent.Promoted, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Promoted{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Promoted{}, false, errors.New("journal is closed")
	}
	promotionID, found := ledger.state.byIntent[intentID]
	if !found {
		return intent.Promoted{}, false, nil
	}
	promotion := ledger.state.promotions[promotionID]
	return intent.Promoted{
		Promotion: promotion,
		Intent:    ledger.state.revisions[promotion.ToIntent],
	}, true, nil
}

func (ledger *Ledger) Initialize(ctx context.Context, initial intent.Revision) error {
	return ledger.append(ctx, journalRecord{
		Format:  journalFormat,
		Kind:    repositoryInitialized,
		Initial: &initial,
	})
}

func (ledger *Ledger) RecordProposal(ctx context.Context, idempotencyKey string, change intent.Change, version intent.Version) error {
	return ledger.append(ctx, journalRecord{
		Format:         journalFormat,
		Kind:           proposalRecorded,
		IdempotencyKey: idempotencyKey,
		Change:         &change,
		Version:        &version,
	})
}

func (ledger *Ledger) PreparePromotion(ctx context.Context, prepared intent.PreparedPromotion) error {
	return ledger.append(ctx, journalRecord{
		Format:     journalFormat,
		Kind:       promotionPrepared,
		Promotion:  &prepared.Promotion,
		NextIntent: &prepared.Intent,
	})
}

func (ledger *Ledger) CompletePromotion(ctx context.Context, promotionID intent.PromotionID) error {
	return ledger.append(ctx, journalRecord{
		Format:      journalFormat,
		Kind:        promotionCompleted,
		PromotionID: promotionID,
	})
}

func (ledger *Ledger) append(ctx context.Context, record journalRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return errors.New("journal is closed")
	}
	if ledger.failed != nil {
		return fmt.Errorf("journal cannot accept writes after a failed rollback: %w", ledger.failed)
	}

	if err := validateRecord(&ledger.state, record); err != nil {
		return err
	}
	if recordAlreadyApplied(&ledger.state, record) {
		return nil
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode journal record: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxRecordBytes {
		return errors.New("journal record is too large")
	}
	info, err := ledger.file.Stat()
	if err != nil {
		return fmt.Errorf("stat journal before append: %w", err)
	}
	written, writeErr := ledger.file.Write(data)
	if writeErr != nil || written != len(data) {
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		if rollbackErr := truncateAndSync(ledger.file, info.Size()); rollbackErr != nil {
			writeErr = errors.Join(writeErr, fmt.Errorf("rollback partial journal record: %w", rollbackErr))
			ledger.failed = writeErr
		}
		return fmt.Errorf("append journal record: %w", writeErr)
	}
	if syncErr := ledger.syncJournal(); syncErr != nil {
		if rollbackErr := truncateAndSync(ledger.file, info.Size()); rollbackErr != nil {
			ledger.failed = errors.Join(syncErr, fmt.Errorf("rollback unsynced journal record: %w", rollbackErr))
			return fmt.Errorf("sync journal record: %w", ledger.failed)
		}
		return fmt.Errorf("sync journal record: %w", syncErr)
	}
	applyValidatedRecord(&ledger.state, record)
	return nil
}

func (ledger *Ledger) replay() error {
	if err := truncateIncompleteTail(ledger.file); err != nil {
		return fmt.Errorf("recover journal tail: %w", err)
	}
	if _, err := ledger.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek journal: %w", err)
	}
	scanner := bufio.NewScanner(ledger.file)
	scanner.Buffer(make([]byte, 64*1024), maxRecordBytes)
	line := 0
	for scanner.Scan() {
		line++
		var record journalRecord
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return fmt.Errorf("decode journal line %d: %w", line, err)
		}
		var trailing json.RawMessage
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("multiple JSON values")
			}
			return fmt.Errorf("decode journal line %d: trailing data: %w", line, err)
		}
		if err := validateRecord(&ledger.state, record); err != nil {
			return fmt.Errorf("apply journal line %d: %w", line, err)
		}
		applyValidatedRecord(&ledger.state, record)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read journal: %w", err)
	}
	if _, err := ledger.file.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek journal end: %w", err)
	}
	return nil
}

func truncateIncompleteTail(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return nil
	}
	var last [1]byte
	if _, err := file.ReadAt(last[:], info.Size()-1); err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}

	const blockSize = int64(4096)
	end := info.Size()
	for end > 0 {
		start := end - blockSize
		if start < 0 {
			start = 0
		}
		block := make([]byte, end-start)
		if _, err := file.ReadAt(block, start); err != nil {
			return err
		}
		if newline := bytes.LastIndexByte(block, '\n'); newline >= 0 {
			return truncateAndSync(file, start+int64(newline)+1)
		}
		end = start
	}
	return truncateAndSync(file, 0)
}

func truncateAndSync(file *os.File, size int64) error {
	if err := file.Truncate(size); err != nil {
		return err
	}
	return file.Sync()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
