// Package shellquote quotes strings for safe interpolation into a POSIX shell
// command line.
package shellquote

import (
	"regexp"
	"strings"
)

// safeToken matches strings that need no shell quoting.
var safeToken = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)

// Quote returns s quoted for safe use in a POSIX shell command line: safe
// tokens are returned unchanged, others are wrapped in single quotes.
func Quote(s string) string {
	if s == "" {
		return `''`
	}
	if safeToken.MatchString(s) {
		return s
	}
	return `'` + strings.ReplaceAll(s, `'`, `'\''`) + `'`
}
