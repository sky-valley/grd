package controlhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
)

const maxResponseBytes = 2 * 1024 * 1024
const defaultRequestTimeout = 30 * time.Second

type Client struct {
	Server     string
	HTTPClient *http.Client
}

func (client Client) Intent(ctx context.Context) (IntentFact, error) {
	var fact IntentFact
	if err := client.doJSON(ctx, http.MethodGet, "/v1/intent", nil, "", &fact); err != nil {
		return IntentFact{}, fmt.Errorf("read accepted Intent: %w", err)
	}
	if fact.Schema != IntentSchema {
		return IntentFact{}, fmt.Errorf("unsupported intent schema %q", fact.Schema)
	}
	if fact.Repository == "" || fact.Intent == "" || fact.Content.Engine == "" || fact.Content.Revision == "" {
		return IntentFact{}, errors.New("Intent response is missing required identity or content")
	}
	return fact, nil
}

func (client Client) Propose(ctx context.Context, idempotencyKey string, proposal ProposalRequest) (ProposalReceipt, error) {
	if !canonicalText(idempotencyKey, 256) {
		return ProposalReceipt{}, errors.New("Idempotency-Key must be canonical text of at most 256 bytes")
	}
	if proposal.Schema != ProposalSchema {
		return ProposalReceipt{}, fmt.Errorf("unsupported proposal schema %q", proposal.Schema)
	}
	encoded, err := json.Marshal(proposal)
	if err != nil {
		return ProposalReceipt{}, fmt.Errorf("encode proposal: %w", err)
	}
	var receipt ProposalReceipt
	if err := client.doJSON(ctx, http.MethodPost, "/v1/proposals", encoded, idempotencyKey, &receipt); err != nil {
		return ProposalReceipt{}, fmt.Errorf("propose content: %w", err)
	}
	if receipt.Schema != ProposalReceiptSchema {
		return ProposalReceipt{}, fmt.Errorf("unsupported proposal receipt schema %q", receipt.Schema)
	}
	if receipt.Repository == "" || receipt.Change.ID == "" || receipt.Version.ID == "" || receipt.Version.Change != receipt.Change.ID ||
		receipt.Version.BaseIntent == "" || receipt.Version.Content.Engine == "" || receipt.Version.Content.Revision == "" || receipt.Version.Producer == "" {
		return ProposalReceipt{}, errors.New("proposal receipt is missing required identity, content, or provenance")
	}
	if receipt.Version.BaseIntent != proposal.BaseIntent || receipt.Version.Content != proposal.Content || !slices.Equal(receipt.Version.Dependencies, proposal.Dependencies) {
		return ProposalReceipt{}, errors.New("proposal receipt does not match the proposed content and dependencies")
	}
	return receipt, nil
}

func (client Client) Version(ctx context.Context, versionID string) (VersionInspection, error) {
	if !canonicalText(versionID, 256) || strings.Contains(versionID, "/") {
		return VersionInspection{}, errors.New("Version id must be canonical text of at most 256 bytes")
	}
	var inspection VersionInspection
	if err := client.doJSON(ctx, http.MethodGet, "/v1/versions/"+url.PathEscape(versionID), nil, "", &inspection); err != nil {
		return VersionInspection{}, fmt.Errorf("inspect Version: %w", err)
	}
	if inspection.Schema != VersionSchema {
		return VersionInspection{}, fmt.Errorf("unsupported Version schema %q", inspection.Schema)
	}
	if inspection.Repository == "" || inspection.Version.ID != versionID || inspection.Version.Change == "" || inspection.Version.BaseIntent == "" ||
		inspection.Version.Content.Engine == "" || inspection.Version.Content.Revision == "" || inspection.Version.Producer == "" {
		return VersionInspection{}, errors.New("Version inspection is missing required identity, content, or provenance")
	}
	if inspection.Evaluation != nil && inspection.Evaluation.GoverningIntent != inspection.Version.BaseIntent {
		return VersionInspection{}, errors.New("Version Evaluation does not match its governing Intent")
	}
	if len(inspection.Requirements) > 0 && inspection.Evaluation == nil {
		return VersionInspection{}, errors.New("Version Requirements have no recorded Evaluation")
	}
	if inspection.Promotion != nil && (inspection.Evaluation == nil || inspection.Promotion.Version != versionID) {
		return VersionInspection{}, errors.New("Version Promotion does not match its recorded Evaluation")
	}
	return inspection, nil
}

func (client Client) doJSON(ctx context.Context, method string, path string, body []byte, idempotencyKey string, target any) error {
	endpoint, err := controlEndpoint(client.Server, path)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create control request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultRequestTimeout}
	}
	redirectSafeClient := *httpClient
	redirectSafeClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("control client refuses redirects")
	}
	response, err := redirectSafeClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read control response: %w", err)
	}
	if len(encoded) > maxResponseBytes {
		return errors.New("control response exceeds 2 MiB")
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK {
		if mediaErr == nil && mediaType == "application/json" {
			var problem errorFact
			if json.Unmarshal(encoded, &problem) == nil && problem.Schema == errorSchema && canonicalText(problem.Message, 4096) {
				return errors.New(problem.Message)
			}
		}
		return fmt.Errorf("server returned %s", response.Status)
	}
	if mediaErr != nil || mediaType != "application/json" {
		return errors.New("control response is not application/json")
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		return fmt.Errorf("decode control response: response must contain a single JSON value: %w", err)
	}
	return nil
}

func controlEndpoint(server string, path string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(server))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("server must be an absolute HTTP URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("server URL must not contain credentials, a query, or a fragment")
	}
	address, err := netip.ParseAddr(parsed.Hostname())
	if err != nil || !address.IsLoopback() {
		return "", errors.New("server must use a numeric loopback address")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	return parsed.String(), nil
}
