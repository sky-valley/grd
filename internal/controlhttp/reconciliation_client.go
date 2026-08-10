package controlhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/sky-valley/grd/internal/principal"
)

func (client Client) Change(ctx context.Context, changeID string) (ChangeInspection, error) {
	if !canonicalText(changeID, 256) || strings.Contains(changeID, "/") {
		return ChangeInspection{}, errors.New("Change id must be canonical text of at most 256 bytes")
	}
	var inspection ChangeInspection
	if err := client.doJSON(ctx, http.MethodGet, "/v1/changes/"+url.PathEscape(changeID), nil, "", &inspection); err != nil {
		return ChangeInspection{}, fmt.Errorf("inspect Change: %w", err)
	}
	if inspection.Schema != ChangeSchema || inspection.Repository == "" || inspection.Change.ID != changeID || inspection.LatestVersion.Change != changeID || !validVersionFact(inspection.LatestVersion) {
		return ChangeInspection{}, errors.New("Change inspection contains invalid identity or Version facts")
	}
	if inspection.LatestAmendment != nil && (!validAmendmentFact(*inspection.LatestAmendment) || inspection.LatestAmendment.ToVersion != inspection.LatestVersion.ID) {
		return ChangeInspection{}, errors.New("Change inspection contains an invalid latest Amendment")
	}
	if inspection.LatestPromotion != nil && (!validPromotionFact(*inspection.LatestPromotion) || inspection.LatestPromotion.Version != inspection.LatestVersion.ID) {
		return ChangeInspection{}, errors.New("Change inspection contains an invalid latest Promotion")
	}
	return inspection, nil
}

func (client Client) Amend(ctx context.Context, key string, request AmendmentRequest) (AmendmentReceipt, error) {
	var receipt AmendmentReceipt
	if err := client.mutate(ctx, "/v1/amendments", key, request, &receipt); err != nil {
		return AmendmentReceipt{}, fmt.Errorf("record Amendment: %w", err)
	}
	if receipt.Schema != AmendmentReceiptSchema || receipt.Repository == "" || !validVersionFact(receipt.Version) || receipt.Change.ID != request.Change || receipt.Version.Change != request.Change || !validAmendmentFact(receipt.Amendment) || receipt.Amendment.FromVersion != request.ExpectedVersion || receipt.Amendment.ToVersion != receipt.Version.ID || receipt.Amendment.Rationale != request.Rationale || receipt.Version.Content != request.Content {
		return AmendmentReceipt{}, errors.New("Amendment receipt does not match the requested immutable facts")
	}
	return receipt, nil
}

func (client Client) RebaseHeldVersion(ctx context.Context, key string, request HeldVersionRebaseRequest) (HeldVersionRebaseReceipt, error) {
	var receipt HeldVersionRebaseReceipt
	if err := client.mutate(ctx, "/v1/held-version-rebases", key, request, &receipt); err != nil {
		return HeldVersionRebaseReceipt{}, fmt.Errorf("record held Version rebase: %w", err)
	}
	if receipt.Schema != HeldVersionRebaseReceiptSchema || receipt.Repository == "" || !validVersionFact(receipt.Version) || receipt.Change.ID != receipt.Version.Change || receipt.Rebase.FromVersion != request.ExpectedVersion || receipt.Rebase.ToVersion != receipt.Version.ID || receipt.Rebase.ToIntent != request.ExpectedIntent || receipt.Rebase.Rationale != request.Rationale || receipt.Version.Content != request.Content {
		return HeldVersionRebaseReceipt{}, errors.New("held Version rebase receipt does not match the requested immutable facts")
	}
	return receipt, nil
}

func (client Client) ReconcileDependent(ctx context.Context, key string, request DependentReconciliationRequest) (DependentReconciliationReceipt, error) {
	var receipt DependentReconciliationReceipt
	if err := client.mutate(ctx, "/v1/dependent-reconciliations", key, request, &receipt); err != nil {
		return DependentReconciliationReceipt{}, fmt.Errorf("record dependent reconciliation: %w", err)
	}
	reconciliation := receipt.Reconciliation
	if receipt.Schema != DependentReconciliationReceiptSchema || receipt.Repository == "" || !validVersionFact(receipt.Version) || receipt.Change.ID != receipt.Version.Change || reconciliation.FromVersion != request.ExpectedVersion || reconciliation.ToVersion != receipt.Version.ID || reconciliation.ReplacedDependency != request.ReplacedDependency || reconciliation.AcceptedVersion != request.AcceptedVersion || reconciliation.BaseIntent != request.ExpectedIntent || reconciliation.Rationale != request.Rationale || receipt.Version.Content != request.Content {
		return DependentReconciliationReceipt{}, errors.New("dependent reconciliation receipt does not match the requested immutable facts")
	}
	return receipt, nil
}

func (client Client) RecordReconciliationConflict(ctx context.Context, key string, request ReconciliationConflictRequest) (ReconciliationConflictReceipt, error) {
	var receipt ReconciliationConflictReceipt
	if err := client.mutate(ctx, "/v1/reconciliation-conflicts", key, request, &receipt); err != nil {
		return ReconciliationConflictReceipt{}, fmt.Errorf("record reconciliation conflict: %w", err)
	}
	conflict := receipt.Conflict
	if receipt.Schema != ReconciliationConflictReceiptSchema || receipt.Repository == "" || !validConflictFact(conflict) || conflict.FromVersion != request.FromVersion || conflict.ToVersion != request.ToVersion || conflict.Version.ID != request.DescendantVersion || !slices.Equal(conflict.AffectedPaths, request.AffectedPaths) || (request.ExpectedIntent != "" && conflict.BaseIntent != request.ExpectedIntent) {
		return ReconciliationConflictReceipt{}, errors.New("reconciliation conflict receipt does not match the requested immutable facts")
	}
	return receipt, nil
}

func (client Client) ResolveReconciliationConflict(ctx context.Context, key string, request ReconciliationResolutionRequest) (ReconciliationResolutionReceipt, error) {
	var receipt ReconciliationResolutionReceipt
	if err := client.mutate(ctx, "/v1/reconciliation-resolutions", key, request, &receipt); err != nil {
		return ReconciliationResolutionReceipt{}, fmt.Errorf("record reconciliation resolution: %w", err)
	}
	resolution := receipt.Resolution
	if receipt.Schema != ReconciliationResolutionReceiptSchema || receipt.Repository == "" || !validVersionFact(receipt.Version) || receipt.Change.ID != receipt.Version.Change || resolution.ConflictID != request.Conflict || resolution.FromVersion != request.ExpectedVersion || resolution.ToVersion != receipt.Version.ID || resolution.BaseIntent != request.ExpectedIntent || resolution.Rationale != request.Rationale || receipt.Version.Content != request.Content {
		return ReconciliationResolutionReceipt{}, errors.New("reconciliation resolution receipt does not match the requested immutable facts")
	}
	return receipt, nil
}

func (client Client) mutate(ctx context.Context, path, key string, request any, receipt any) error {
	if !canonicalText(key, 256) {
		return errors.New("Idempotency-Key must be canonical text of at most 256 bytes")
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode mutation: %w", err)
	}
	return client.doJSON(ctx, http.MethodPost, path, encoded, key, receipt)
}

func validVersionFact(version VersionFact) bool {
	producer, validProducer := principal.Canonical(version.Producer)
	if !canonicalText(version.ID, 256) || !canonicalText(version.Change, 256) || !canonicalText(version.BaseIntent, 256) || !validContent(version.Content) || !validProducer || producer != version.Producer {
		return false
	}
	seen := make(map[string]struct{}, len(version.Dependencies))
	for _, dependency := range version.Dependencies {
		if !canonicalText(dependency, 256) || dependency == version.ID {
			return false
		}
		if _, duplicate := seen[dependency]; duplicate {
			return false
		}
		seen[dependency] = struct{}{}
	}
	return true
}
