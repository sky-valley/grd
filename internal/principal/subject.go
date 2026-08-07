package principal

import (
	"strings"
	"unicode"
)

const maxSubjectBytes = 256

// Canonical validates an authority-issued principal subject without
// interpreting its identity scheme. Authentication adapters own
// scheme-specific parsing and normalization; the repository only requires
// bounded, stable, one-line text.
func Canonical(subject string) (string, bool) {
	canonical := strings.TrimSpace(subject)
	if len(canonical) < 1 || len(canonical) > maxSubjectBytes {
		return "", false
	}
	for _, char := range canonical {
		if unicode.IsControl(char) || unicode.IsSpace(char) {
			return "", false
		}
	}
	return canonical, true
}
