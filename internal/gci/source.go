package gci

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Source is one configured instructions source.
//
// Wire format (one per line, in the config file or the GH_COPILOT_INSTRUCTIONS
// env var):
//
//	owner/repo[@ref][:path]   [token]
//
// The token, when present, is the last whitespace-separated field.
type Source struct {
	Repo  string // "owner/repo"
	Ref   string // branch, tag, or full commit SHA; empty = remote default branch
	Path  string // glob/file/dir within the repo; empty = DefaultPath
	Token string // inline token; empty = resolve from env/gh
}

// DefaultPath is used when a source omits an explicit path.
const DefaultPath = "**/*.instructions.md"

// FileDir is the namespace directory, under ~/.copilot/instructions, that holds
// every file this tool installs. Keeping our files under a single directory means
// prune/remove never touch the user's own hand-written instruction files.
const FileDir = "gh-copilot-instructions"

// ParseSpec parses an "owner/repo[@ref][:path]" source spec (no token).
func ParseSpec(spec string) (Source, error) {
	var s Source
	rest := strings.TrimSpace(spec)
	if rest == "" {
		return s, fmt.Errorf("empty source")
	}
	// :path  (first colon; owner/repo and refs never contain ':')
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		s.Path = strings.TrimSpace(rest[i+1:])
		rest = rest[:i]
	}
	// @ref  (first '@'; owner/repo never contains '@')
	if i := strings.IndexByte(rest, '@'); i >= 0 {
		s.Ref = strings.TrimSpace(rest[i+1:])
		rest = rest[:i]
	}
	s.Repo = strings.TrimSpace(rest)
	if !validRepo(s.Repo) {
		return s, fmt.Errorf("invalid repo %q (expected owner/repo)", s.Repo)
	}
	// Normalize a leading slash on paths ("/instructions" == "instructions").
	s.Path = strings.TrimPrefix(s.Path, "/")
	return s, nil
}

// ParseLine parses a full config line: a spec plus an optional trailing token.
// Returns ok=false for blank lines and comments.
func ParseLine(line string) (Source, bool, error) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return Source{}, false, nil
	}
	spec := t
	token := ""
	if i := strings.IndexAny(t, " \t"); i >= 0 {
		spec = t[:i]
		token = strings.TrimSpace(t[i+1:])
	}
	s, err := ParseSpec(spec)
	if err != nil {
		return Source{}, false, err
	}
	s.Token = token
	return s, true, nil
}

func validRepo(r string) bool {
	parts := strings.Split(r, "/")
	if len(parts) != 2 {
		return false
	}
	return parts[0] != "" && parts[1] != ""
}

// Owner returns the owner ("owner" from "owner/repo").
func (s Source) Owner() string {
	if i := strings.IndexByte(s.Repo, '/'); i >= 0 {
		return s.Repo[:i]
	}
	return s.Repo
}

// Name returns the repo name ("repo" from "owner/repo").
func (s Source) Name() string {
	if i := strings.IndexByte(s.Repo, '/'); i >= 0 {
		return s.Repo[i+1:]
	}
	return s.Repo
}

// ID is the deterministic identity of a source: the first 8 hex chars of
// sha256("repo\nref\npath"). Stable across machines and re-pulls; unique per
// distinct repo+ref+path; computable offline.
func (s Source) ID() string {
	sum := sha256.Sum256([]byte(s.Repo + "\n" + s.Ref + "\n" + s.Path))
	return hex.EncodeToString(sum[:])[:8]
}

// Spec renders the canonical "owner/repo[@ref][:path]" (no token).
func (s Source) Spec() string {
	var b strings.Builder
	b.WriteString(s.Repo)
	if s.Ref != "" {
		b.WriteByte('@')
		b.WriteString(s.Ref)
	}
	if s.Path != "" {
		b.WriteByte(':')
		b.WriteString(s.Path)
	}
	return b.String()
}

// Line renders the canonical config line (spec plus optional token).
func (s Source) Line() string {
	if s.Token != "" {
		return s.Spec() + "   " + s.Token
	}
	return s.Spec()
}

// effectivePath returns the configured path or the default.
func (s Source) effectivePath() string {
	if s.Path == "" {
		return DefaultPath
	}
	return s.Path
}
