package principal_test

import (
	"testing"

	"github.com/sky-valley/grd/internal/principal"
)

func TestCanonicalPreservesOpaqueProviderSubject(t *testing.T) {
	got, valid := principal.Canonical("  auth0|GitHub:User=12345  ")
	if !valid || got != "auth0|GitHub:User=12345" {
		t.Fatalf("Canonical() = %q, %t", got, valid)
	}
}

func TestCanonicalRejectsMissingMultilineOrWhitespaceSubjects(t *testing.T) {
	for _, invalid := range []string{"", "assignee job", "github:user\nsecond"} {
		if got, valid := principal.Canonical(invalid); valid || got != "" {
			t.Fatalf("Canonical(%q) = %q, %t", invalid, got, valid)
		}
	}
}
