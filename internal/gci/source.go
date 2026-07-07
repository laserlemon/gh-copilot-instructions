package gci

import (
	"crypto/sha256"
	"fmt"
	"math/big"
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

// IsGitHubURL reports whether arg looks like a github.com web URL (with or
// without an http(s) scheme), as opposed to a bare owner/repo argument. The
// source commands use it to decide how to parse a positional argument: a URL
// carries its own ref and path, while owner/repo takes them from flags.
func IsGitHubURL(arg string) bool {
	u := strings.TrimSpace(arg)
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	return strings.HasPrefix(u, "github.com/")
}

// ParseRepo parses a bare "owner/repo" argument - the CLI input form for the
// source commands. It rejects the older combined forms (an @ref or :path suffix,
// a GitHub blob URL); a ref or path within the repo is given with the --ref and
// --path flags instead.
func ParseRepo(arg string) (Source, error) {
	r := strings.TrimSpace(arg)
	if r == "" {
		return Source{}, fmt.Errorf("a repository is required (owner/repo)")
	}
	if strings.ContainsAny(r, "@: \t") || strings.Contains(r, "//") {
		return Source{}, fmt.Errorf("provide just owner/repo; use --ref and --path for a ref or path within the repository")
	}
	if !validRepo(r) {
		return Source{}, fmt.Errorf("invalid repository %q (expected owner/repo)", r)
	}
	return Source{Repo: r}, nil
}

// ParseSpec parses an "owner/repo[@ref][:path]" source spec (no token). A GitHub
// blob/tree URL (e.g. https://github.com/owner/repo/blob/main/path/file.md) is
// also accepted and normalized to the same Source.
func ParseSpec(spec string) (Source, error) {
	var s Source
	rest := strings.TrimSpace(spec)
	if rest == "" {
		return s, fmt.Errorf("empty source")
	}
	if s, ok, err := parseGitHubURL(rest); ok {
		return s, err
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

// parseGitHubURL recognizes a github.com web URL and normalizes it to a Source.
// It returns ok=false (so ParseSpec falls back to spec parsing) for anything
// that isn't a github.com URL.
//
// Supported shapes (scheme optional):
//
//	github.com/owner/repo                          -> whole repo, default branch
//	github.com/owner/repo/blob/<ref>/<path>        -> a file at <ref>
//
// A ref of "-" means the default branch (GitHub redirects /blob/-/… there), so
// it maps to an empty Ref. A ref containing a slash (e.g. "feature/x") can't be
// told apart from the path in a web URL and isn't supported - use the
// owner/repo@ref:path spec for those. tree/ (directory) URLs are intentionally
// not accepted yet (no "-" parity, and the target files are under-specified).
func parseGitHubURL(spec string) (Source, bool, error) {
	u := strings.TrimPrefix(strings.TrimPrefix(spec, "https://"), "http://")
	if !strings.HasPrefix(u, "github.com/") {
		return Source{}, false, nil
	}
	u = strings.TrimPrefix(u, "github.com/")
	if i := strings.IndexAny(u, "?#"); i >= 0 { // drop query / #fragment
		u = u[:i]
	}
	parts := strings.Split(strings.Trim(u, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return Source{}, true, fmt.Errorf("invalid GitHub URL: %q", spec)
	}
	s := Source{Repo: parts[0] + "/" + parts[1]}
	if !validRepo(s.Repo) {
		return s, true, fmt.Errorf("invalid GitHub URL repository: %q", s.Repo)
	}
	if len(parts) >= 3 {
		if parts[2] != "blob" {
			return s, true, fmt.Errorf("only blob URLs are supported (not /%s/): %q", parts[2], spec)
		}
		if len(parts) >= 4 {
			if ref := parts[3]; ref != "-" { // "-" => default branch
				s.Ref = ref
			}
		}
		if len(parts) >= 5 {
			s.Path = strings.Join(parts[4:], "/")
		}
	}
	return s, true, nil
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

// ID is the deterministic identity (slug) of a source: the first 8 base36 chars
// of sha256("repo\nref\npath"). Stable across machines and re-pulls; unique per
// distinct repo+ref+path; computable offline. Base36 (lowercase 0-9a-z) keeps
// ids from looking like commit SHAs and stays stable on case-insensitive file
// systems, while packing more entropy per character than hex.
//
// Invariant: a slug never contains a slash. Base36 already guarantees this, and
// any future custom slugs (see #8) must preserve it - it's what lets a command
// tell a slug apart from a source spec (owner/repo[@ref][:path]) with no
// ambiguity (see targetMatches).
func (s Source) ID() string {
	sum := sha256.Sum256([]byte(s.Repo + "\n" + s.Ref + "\n" + s.Path))
	// A 256-bit value is at most 50 base36 digits; left-pad so the id always
	// has 8 chars even when the leading digits are zero.
	b36 := new(big.Int).SetBytes(sum[:]).Text(36)
	if len(b36) < 8 {
		b36 = strings.Repeat("0", 8-len(b36)) + b36
	}
	return b36[:8]
}

// targetMatches reports whether a configured source - identified by its slug id
// and its repo/ref/path coordinates - is the one a remove target refers to.
//
// A slug never contains a slash and a source spec always does (owner/repo), so a
// remove target is unambiguously one or the other. See Source.ID for the
// no-slash slug invariant that custom slugs must also honor.
func targetMatches(target, id, repo, ref, path string) bool {
	if strings.Contains(target, "/") {
		// A spec/URL: match by coordinates, not by recomputing a slug, so this
		// stays correct once slugs can be custom (non-deterministic).
		spec, err := ParseSpec(target)
		return err == nil && spec.Repo == repo && spec.Ref == ref && spec.Path == path
	}
	return id == target
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
