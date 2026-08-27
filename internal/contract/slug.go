package contract

import (
	"fmt"
	"regexp"
	"unicode/utf8"
)

// MaxSlugLen bounds slug values so they stay filename-safe on every target
// filesystem; mirrored by maxLength in both schemas.
const MaxSlugLen = 128

// The anchors are \A and \z rather than ^ and $ because the contract's
// full-string rule is strict end-of-input: Go reads an unflagged $ that way
// today, but adding (?m) to this pattern would silently start accepting
// "pr-comments\n" as a slug, and a slug reaches a filesystem path.
var slugPattern = regexp.MustCompile(`\A[a-z0-9][a-z0-9._-]*\z`)

// ValidateSlug checks a canonical lowercase path-safe slug — the shared
// grammar for `source`, manifest `name`, and CLI `consumer` values
// (event-contract.md §Event, §Manifest, §Spool and cursors). It must pass
// before the value participates in any path: the charset contains
// no separators and the first character may not be a dot, so `..`, absolute
// paths, hidden files, and case-folded aliases of another slug cannot
// validate. role names the field being checked, for error messages only.
func ValidateSlug(role, value string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", role)
	}
	// Scalar values, the metric every length limit in the contract counts.
	// The grammar below is ASCII, so the two metrics agree on anything that
	// validates — they part ways only on the input this rejects.
	if n := utf8.RuneCountInString(value); n > MaxSlugLen {
		return fmt.Errorf("%s is %d characters, over the %d-character bound", role, n, MaxSlugLen)
	}
	if !slugPattern.MatchString(value) {
		return fmt.Errorf("%s %q is not a canonical lowercase slug (want ^[a-z0-9][a-z0-9._-]*$)", role, value)
	}
	return nil
}
