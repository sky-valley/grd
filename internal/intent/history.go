package intent

import (
	"context"
	"errors"
)

var ErrHistoryCursorNotFound = errors.New("history cursor not found")

type HistoryCursor uint64
type HistoryKind string

const (
	HistoryIntentInitialized      HistoryKind = "intent_initialized"
	HistoryVersionProposed        HistoryKind = "version_proposed"
	HistoryEvaluationRecorded     HistoryKind = "evaluation_recorded"
	HistoryRequirementResponded   HistoryKind = "requirement_responded"
	HistoryVersionPromoted        HistoryKind = "version_promoted"
	HistoryVersionAmended         HistoryKind = "version_amended"
	HistoryDependentReconciled    HistoryKind = "dependent_reconciled"
	HistoryHeldVersionRebased     HistoryKind = "held_version_rebased"
	HistoryConflictRecorded       HistoryKind = "reconciliation_conflict_recorded"
	HistoryReconciliationResolved HistoryKind = "reconciliation_resolved"
)

// HistoryFact is one immutable semantic fact in repository order. Exactly the
// payloads relevant to Kind are populated.
type HistoryFact struct {
	Cursor                   HistoryCursor
	Kind                     HistoryKind
	Intent                   *Revision
	Change                   *Change
	Version                  *Version
	Evaluation               *Evaluation
	RequirementResponse      *RequirementResponse
	Promotion                *Promotion
	Amendment                *Amendment
	DependentReconciliation  *DependentReconciliation
	HeldVersionRebase        *HeldVersionRebase
	ReconciliationConflict   *ReconciliationConflict
	ReconciliationResolution *ReconciliationResolution
}

type HistoryQuery struct {
	After HistoryCursor
	Limit int
}

type HistoryPage struct {
	Facts      []HistoryFact
	NextCursor HistoryCursor
}

func (repository *Repository) History(ctx context.Context, query HistoryQuery) (HistoryPage, error) {
	if query.Limit < 1 || query.Limit > 100 {
		return HistoryPage{}, errors.New("history page limit must be between one and 100")
	}
	facts, more, err := repository.history.History(ctx, query.After, query.Limit)
	if err != nil {
		return HistoryPage{}, err
	}
	page := HistoryPage{Facts: CloneHistoryFacts(facts)}
	if more && len(facts) > 0 {
		page.NextCursor = facts[len(facts)-1].Cursor
	}
	return page, nil
}

func historyPage(facts []HistoryFact, after HistoryCursor, limit int) ([]HistoryFact, bool, error) {
	start := 0
	if after != 0 {
		start = -1
		for index, fact := range facts {
			if fact.Cursor == after {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return nil, false, ErrHistoryCursorNotFound
		}
	}
	end := min(start+limit, len(facts))
	return CloneHistoryFacts(facts[start:end]), end < len(facts), nil
}

func appendHistoryFact(facts *[]HistoryFact, fact HistoryFact) {
	fact.Cursor = HistoryCursor(len(*facts) + 1)
	*facts = append(*facts, cloneHistoryFact(fact))
}

// CloneHistoryFacts returns a deep copy for storage adapters implementing the
// HistoryStore contract.
func CloneHistoryFacts(facts []HistoryFact) []HistoryFact {
	result := make([]HistoryFact, len(facts))
	for index, fact := range facts {
		result[index] = cloneHistoryFact(fact)
	}
	return result
}

func cloneHistoryFact(fact HistoryFact) HistoryFact {
	if fact.Intent != nil {
		copy := *fact.Intent
		fact.Intent = &copy
	}
	if fact.Change != nil {
		copy := *fact.Change
		fact.Change = &copy
	}
	if fact.Version != nil {
		copy := cloneVersion(*fact.Version)
		fact.Version = &copy
	}
	if fact.Evaluation != nil {
		copy := cloneEvaluation(*fact.Evaluation)
		fact.Evaluation = &copy
	}
	if fact.RequirementResponse != nil {
		copy := *fact.RequirementResponse
		fact.RequirementResponse = &copy
	}
	if fact.Promotion != nil {
		copy := *fact.Promotion
		fact.Promotion = &copy
	}
	if fact.Amendment != nil {
		copy := *fact.Amendment
		fact.Amendment = &copy
	}
	if fact.DependentReconciliation != nil {
		copy := *fact.DependentReconciliation
		fact.DependentReconciliation = &copy
	}
	if fact.HeldVersionRebase != nil {
		copy := *fact.HeldVersionRebase
		fact.HeldVersionRebase = &copy
	}
	if fact.ReconciliationConflict != nil {
		copy := cloneReconciliationConflict(*fact.ReconciliationConflict)
		fact.ReconciliationConflict = &copy
	}
	if fact.ReconciliationResolution != nil {
		copy := *fact.ReconciliationResolution
		fact.ReconciliationResolution = &copy
	}
	return fact
}
