// Package controlhttp projects GRD repository facts over HTTP.
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
	"strings"
	"time"

	"github.com/sky-valley/grd/internal/intent"
)

const IntentSchema = "grd.intent/v1"

const maxResponseBytes = 64 * 1024
const defaultRequestTimeout = 30 * time.Second

type IntentReader interface {
	CurrentIntent(ctx context.Context, repositoryID string) (intent.Revision, error)
}

type IntentFact struct {
	Schema         string  `json:"schema"`
	Repository     string  `json:"repository"`
	Intent         string  `json:"intent"`
	PreviousIntent string  `json:"previousIntent,omitempty"`
	Content        Content `json:"content"`
}

type Content struct {
	Engine   string `json:"engine"`
	Revision string `json:"revision"`
}

type errorFact struct {
	Schema  string `json:"schema"`
	Message string `json:"message"`
}

func NewHandler(repositoryID string, reader IntentReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/v1/intent" {
			writeError(w, http.StatusNotFound, "control endpoint not found")
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		accepted, err := reader.CurrentIntent(r.Context(), repositoryID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "accepted Intent could not be read")
			return
		}
		writeJSON(w, http.StatusOK, IntentFact{
			Schema:         IntentSchema,
			Repository:     repositoryID,
			Intent:         string(accepted.ID),
			PreviousIntent: string(accepted.PreviousID),
			Content: Content{
				Engine:   accepted.Content.Engine,
				Revision: accepted.Content.Revision,
			},
		})
	})
}

type Client struct {
	Server     string
	HTTPClient *http.Client
}

func (client Client) Intent(ctx context.Context) (IntentFact, error) {
	endpoint, err := intentEndpoint(client.Server)
	if err != nil {
		return IntentFact{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return IntentFact{}, fmt.Errorf("create Intent request: %w", err)
	}
	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultRequestTimeout}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return IntentFact{}, fmt.Errorf("read accepted Intent: %w", err)
	}
	defer response.Body.Close()

	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	encoded, err := io.ReadAll(limited)
	if err != nil {
		return IntentFact{}, fmt.Errorf("read Intent response: %w", err)
	}
	if len(encoded) > maxResponseBytes {
		return IntentFact{}, errors.New("Intent response exceeds 64 KiB")
	}
	if response.StatusCode != http.StatusOK {
		return IntentFact{}, fmt.Errorf("read accepted Intent: server returned %s", response.Status)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return IntentFact{}, errors.New("Intent response is not application/json")
	}
	var fact IntentFact
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err := decoder.Decode(&fact); err != nil {
		return IntentFact{}, fmt.Errorf("decode Intent response: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return IntentFact{}, errors.New("Intent response must contain a single JSON value")
	}
	if fact.Schema != IntentSchema {
		return IntentFact{}, fmt.Errorf("unsupported intent schema %q", fact.Schema)
	}
	if fact.Repository == "" || fact.Intent == "" || fact.Content.Engine == "" || fact.Content.Revision == "" {
		return IntentFact{}, errors.New("Intent response is missing required identity or content")
	}
	return fact, nil
}

func intentEndpoint(server string) (string, error) {
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
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1/intent"
	return parsed.String(), nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorFact{Schema: "grd.error/v1", Message: message})
}
