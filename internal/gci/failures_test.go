package gci

import (
	"errors"
	"testing"
)

func TestFriendlyError(t *testing.T) {
	s, _ := ParseSpec("github/github")
	cases := []struct{ in, want string }{
		{"github/github: tree too large (truncated); narrow the path",
			"too many files to scan - narrow it with a path, e.g. github/github:path/to/dir"},
		{"HTTP 404: Not Found (https://api.github.com/...)",
			"repository not found, or your token can't read it"},
		{"github/github: HTTP 403: Forbidden",
			"not authorized - pass --token with Contents: read for a private source"},
		{"github/github: something weird happened",
			"something weird happened"}, // unknown -> verbatim (prefix stripped)
	}
	for _, c := range cases {
		if got := friendlyError(s, errors.New(c.in)); got != c.want {
			t.Errorf("friendlyError(%q)\n = %q\nwant %q", c.in, got, c.want)
		}
	}
}

// TestFailedAddReturnsErrReported: a failed add reports its own summary and
// returns ErrReported so main() won't print a second raw error.
func TestFailedAddReturnsErrReported(t *testing.T) {
	s, _ := ParseSpec("o/bad")
	a := newTestApp(t, &errFetcher{err: errors.New("boom")})
	if err := a.Add(s, false); !errors.Is(err, ErrReported) {
		t.Errorf("failed add should return ErrReported, got %v", err)
	}
}
