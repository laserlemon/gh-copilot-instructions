package gci

import (
	"errors"
	"strings"
)

// ErrReported signals that a command has already shown its failure(s) to the
// user (e.g. a failure summary below the table). main() exits non-zero on it
// without printing the error again, so the user never sees a raw, duplicated
// message.
var ErrReported = errors.New("reported")

// failure is one source that failed to pull, paired with a friendly reason.
type failure struct {
	repo string
	msg  string
}

// friendlyError turns a raw pull error into a short, user-understandable
// sentence. It first strips the duplicate "owner/repo: " prefix the fetcher
// prepends, then maps the common cases; anything unrecognized falls through
// verbatim (minus the prefix) so we never hide a real message.
func friendlyError(s Source, err error) string {
	msg := strings.TrimPrefix(err.Error(), s.Repo+": ")
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "tree too large"), strings.Contains(low, "truncated"):
		return "too many files to scan - narrow it with a path, e.g. " + s.Repo + ":path/to/dir"
	case strings.Contains(low, "404"), strings.Contains(low, "not found"):
		return "repository not found, or your token can't read it"
	case strings.Contains(low, "401"), strings.Contains(low, "403"),
		strings.Contains(low, "unauthorized"), strings.Contains(low, "forbidden"):
		return "not authorized - pass --token with Contents: read for a private source"
	case strings.Contains(low, "no commits"):
		return "the repository has no commits yet"
	default:
		return msg
	}
}

// printFailures writes a failure summary to stderr, separated from the table
// above by a blank line: one red ✗ line per source (bold repository + a gray,
// human-readable reason). No-op when there were no failures.
func (a *App) printFailures(fails []failure) {
	if len(fails) == 0 {
		return
	}
	a.msg("") // blank line separates the summary from the table
	for _, f := range fails {
		a.fail("%s  %s", a.cs().Bold(f.repo), a.cs().Gray(f.msg))
	}
}
