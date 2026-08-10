package controlhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sky-valley/grd/internal/controlhttp"
	"github.com/sky-valley/grd/internal/intent"
)

func TestIntentEndpointProjectsTheAcceptedRepositoryFact(t *testing.T) {
	accepted := intent.Revision{
		ID:         "intent_current",
		PreviousID: "intent_previous",
		Content:    intent.ContentRef{Engine: "git", Revision: "0123456789abcdef"},
	}
	handler := controlhttp.NewHandler("repo_example", staticIntentReader{accepted: accepted})
	request := httptest.NewRequest(http.MethodGet, "/v1/intent", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q, want application/json", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache control = %q, want no-store", got)
	}
	var fact controlhttp.IntentFact
	decoder := json.NewDecoder(recorder.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fact); err != nil {
		t.Fatalf("decode intent fact: %v", err)
	}
	if fact.Schema != controlhttp.IntentSchema || fact.Repository != "repo_example" {
		t.Fatalf("intent envelope = %#v", fact)
	}
	if fact.Intent != string(accepted.ID) || fact.PreviousIntent != string(accepted.PreviousID) {
		t.Fatalf("intent identity = %#v", fact)
	}
	if fact.Content.Engine != "git" || fact.Content.Revision != accepted.Content.Revision {
		t.Fatalf("intent content = %#v", fact.Content)
	}
}

func TestClientReadsAndValidatesIntentFact(t *testing.T) {
	handler := controlhttp.NewHandler("repo_example", staticIntentReader{accepted: intent.Revision{
		ID:      "intent_current",
		Content: intent.ContentRef{Engine: "git", Revision: "0123456789abcdef"},
	}})
	server := httptest.NewServer(handler)
	defer server.Close()

	fact, err := (controlhttp.Client{Server: server.URL}).Intent(context.Background())
	if err != nil {
		t.Fatalf("read intent: %v", err)
	}
	if fact.Schema != controlhttp.IntentSchema || fact.Intent != "intent_current" {
		t.Fatalf("intent fact = %#v", fact)
	}
}

func TestClientRejectsAnUnknownIntentSchema(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema":"grd.intent/v2","repository":"repo_example","intent":"intent_current","content":{"engine":"git","revision":"0123456789abcdef"}}`))
	}))
	defer server.Close()

	_, err := (controlhttp.Client{Server: server.URL}).Intent(context.Background())
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("unsupported intent schema")) {
		t.Fatalf("error = %v, want unsupported schema", err)
	}
}

func TestClientRejectsTrailingJSONAfterTheIntentFact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema":"grd.intent/v1","repository":"repo_example","intent":"intent_current","content":{"engine":"git","revision":"0123456789abcdef"}} {"hidden":true}`))
	}))
	defer server.Close()

	_, err := (controlhttp.Client{Server: server.URL}).Intent(context.Background())
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("single JSON value")) {
		t.Fatalf("error = %v, want trailing JSON rejection", err)
	}
}

func TestClientRejectsANonLoopbackServerBeforeSending(t *testing.T) {
	called := false
	client := controlhttp.Client{
		Server: "http://192.0.2.1:8787",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			called = true
			return nil, errors.New("unexpected request")
		})},
	}

	_, err := client.Intent(context.Background())
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("loopback")) {
		t.Fatalf("error = %v, want loopback rejection", err)
	}
	if called {
		t.Fatal("client sent a request to a non-loopback server")
	}
}

type staticIntentReader struct {
	accepted intent.Revision
}

func (reader staticIntentReader) CurrentIntent(context.Context, string) (intent.Revision, error) {
	return reader.accepted, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
