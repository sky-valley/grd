package evaluatorprotocol_test

import (
	"testing"

	"github.com/sky-valley/grd/internal/evaluatorprotocol"
)

func TestSchemasUseGRDNamespace(t *testing.T) {
	if evaluatorprotocol.RequestSchema != "grd.evaluator-request/v1" {
		t.Fatalf("request schema = %q", evaluatorprotocol.RequestSchema)
	}
	if evaluatorprotocol.ResultSchema != "grd.evaluator-result/v1" {
		t.Fatalf("result schema = %q", evaluatorprotocol.ResultSchema)
	}
}
