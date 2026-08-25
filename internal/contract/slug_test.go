package contract

import (
	"strings"
	"testing"
)

func TestValidateSlug(t *testing.T) {
	valid := []string{"pr-comments", "ci-status", "team.ci_status-v2", "0numeric", "a", strings.Repeat("a", MaxSlugLen)}
	for _, value := range valid {
		if err := ValidateSlug("source", value); err != nil {
			t.Errorf("%q rejected: %v", value, err)
		}
	}

	// Every rejection here must happen before any state path could be
	// formed (event-contract.md §Spool and cursors).
	invalid := map[string]string{
		"empty":               "",
		"uppercase alias":     "PR-Comments",
		"traversal":           "../../etc/passwd",
		"separator":           "a/b",
		"windows separator":   `a\b`,
		"leading dot":         ".hidden",
		"dot-dot":             "..",
		"space":               "pr comments",
		"over length":         strings.Repeat("a", MaxSlugLen+1),
		"null byte":           "a\x00b",
		"non-ascii lowercase": "prüfung",
		// §Event states the full-string rule as strict end-of-input for the
		// sake of engines whose $ matches before a final line terminator.
		// Nothing else in the table would notice this pattern gaining (?m).
		"trailing newline": "pr-comments\n",
	}
	for name, value := range invalid {
		if err := ValidateSlug("consumer", value); err == nil {
			t.Errorf("%s: %q accepted", name, value)
		}
	}
}
