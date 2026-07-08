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
// carries its own ref and path, while owner/repo takes them from flags. A
// gist.github.com URL is handled separately (see IsGistURL); it is fetched via
// the Gists API, not the repo contents API.
func IsGitHubURL(arg string) bool {
	u := strings.TrimSpace(arg)
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	return strings.HasPrefix(u, "github.com/")
}

// gistPrefix marks a Source's Repo as a gist ("gist/<id>") rather than an
// owner/repo. github.com/gist is a reserved namespace, so "gist/<id>" can never
// be a real repo - which is what makes this prefix safe. It also keeps the slash
// that lets targetMatches tell a spec from a slug and lets the canonical config
// line round-trip through ParseSpec (an owner/repo-shaped spec).
const gistPrefix = "gist/"

// gistRepo builds the Repo field for a gist id.
func gistRepo(id string) string { return gistPrefix + id }

// IsGist reports whether the source is a gist (fetched via the Gists API rather
// than the repo contents API).
func (s Source) IsGist() bool { return strings.HasPrefix(s.Repo, gistPrefix) }

// GistID returns the gist id for a gist source (the part after "gist/").
func (s Source) GistID() string { return strings.TrimPrefix(s.Repo, gistPrefix) }

// validGistID reports whether id is a plausible gist id: non-empty and made up
// entirely of alphanumeric characters (gist ids are hex, but stay lenient for
// GitHub Enterprise variants).
func validGistID(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range id {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		default:
			return false
		}
	}
	return true
}

// IsGistURL reports whether arg is a gist.github.com web URL. The bare gist/<id>
// form needs no special detection - it is owner/repo-shaped, so it flows through
// ParseRepo/ParseSpec and IsGist routes it - but a gist URL is not, so the source
// commands use this to route it to ParseGist.
func IsGistURL(arg string) bool {
	u := strings.TrimSpace(arg)
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	return strings.HasPrefix(u, "gist.github.com/")
}

// ParseGist parses a gist.github.com web URL into a gist Source (scheme
// optional):
//
//	gist.github.com/<id>
//	gist.github.com/<user>/<id>[/<revision>]
//
// The result is a gist Source (Repo "gist/<id>"). A 40-hex <revision> segment
// becomes the Ref (a pinned version); otherwise Ref is empty (the latest
// version). A ref or filename glob within the gist is given with --ref/--path,
// so ParseGist itself never sets Path. The bare gist/<id> form is parsed by
// ParseRepo/ParseSpec, not here.
func ParseGist(arg string) (Source, error) {
	u := strings.TrimSpace(arg)
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if !strings.HasPrefix(u, "gist.github.com/") {
		return Source{}, fmt.Errorf("not a gist URL: %q", arg)
	}
	u = strings.TrimPrefix(u, "gist.github.com/")
	if i := strings.IndexAny(u, "?#"); i >= 0 { // drop query / #fragment
		u = u[:i]
	}
	parts := strings.Split(strings.Trim(u, "/"), "/")
	var id, ref string
	if len(parts) == 1 {
		id = parts[0] // gist.github.com/<id>
	} else {
		id = parts[1] // gist.github.com/<user>/<id>
		if len(parts) >= 3 && isFullSHA(parts[2]) {
			ref = parts[2] // gist.github.com/<user>/<id>/<revision>
		}
	}
	id = strings.TrimSuffix(id, ".git")
	if !validGistID(id) {
		return Source{}, fmt.Errorf("invalid gist URL: %q", arg)
	}
	return Source{Repo: gistRepo(id), Ref: ref}, nil
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
// blob or tree URL (e.g. https://github.com/owner/repo/blob/main/path/file.md or
// https://github.com/owner/repo/tree/main/dir) is also accepted and normalized to
// the same Source; a tree URL's directory becomes a prefix over the default glob.
func ParseSpec(spec string) (Source, error) {
	var s Source
	rest := strings.TrimSpace(spec)
	if rest == "" {
		return s, fmt.Errorf("empty source")
	}
	if g, ok, err := parseGitHubURL(rest); ok {
		if err != nil {
			return Source{}, err
		}
		return g.offline(""), nil
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

// githubURL is a parsed github.com web URL - a bare repo, or a blob/tree URL -
// before any network ref disambiguation. seg holds the path segments after the
// "owner/repo/<blob|tree>/" prefix: seg[0] is the ref (or "-" for the default
// branch) and the remainder is the file path (blob) or directory (tree).
type githubURL struct {
	repo string
	kind string // "", "blob", or "tree"
	seg  []string
}

// parseGitHubURL recognizes a github.com web URL and returns its parsed form. It
// returns ok=false (so ParseSpec falls back to spec parsing) for anything that
// isn't a github.com URL.
//
// Supported shapes (scheme optional):
//
//	github.com/owner/repo                     -> whole repo, default branch
//	github.com/owner/repo/blob/<ref>/<path>   -> a file at <ref>
//	github.com/owner/repo/tree/<ref>/<dir>    -> a directory at <ref>, used as a
//	                                             path prefix over the glob
//
// A ref of "-" means the default branch (GitHub redirects /blob/-/... and, since
// github/github#438511, /tree/-/... there), mapping to an empty Ref. A slashed
// ref (e.g. "feature/x") can't be split from the path offline: ResolveSpec probes
// the API to disambiguate, while the offline callers (config lines, remove) treat
// the first segment as the ref (the slug is the escape hatch for those).
func parseGitHubURL(spec string) (githubURL, bool, error) {
	u := strings.TrimPrefix(strings.TrimPrefix(spec, "https://"), "http://")
	if !strings.HasPrefix(u, "github.com/") {
		return githubURL{}, false, nil
	}
	u = strings.TrimPrefix(u, "github.com/")
	if i := strings.IndexAny(u, "?#"); i >= 0 { // drop query / #fragment
		u = u[:i]
	}
	parts := strings.Split(strings.Trim(u, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return githubURL{}, true, fmt.Errorf("invalid GitHub URL: %q", spec)
	}
	g := githubURL{repo: parts[0] + "/" + parts[1]}
	if !validRepo(g.repo) {
		return g, true, fmt.Errorf("invalid GitHub URL repository: %q", g.repo)
	}
	if len(parts) >= 3 {
		g.kind = parts[2]
		if g.kind != "blob" && g.kind != "tree" {
			return g, true, fmt.Errorf("only blob and tree URLs are supported (not /%s/): %q", g.kind, spec)
		}
		g.seg = parts[3:]
	}
	return g, true, nil
}

// source builds a Source from a resolved ref and the segments after it (the file
// path for a blob, the directory for a tree). For a tree URL the directory is a
// prefix composed with path (the add-time --path, "" elsewhere); blob and bare
// repo URLs carry their own path, so path is ignored for them. ref is already
// normalized ("" for the default branch).
func (g githubURL) source(ref string, rest []string, path string) Source {
	p := strings.Join(rest, "/")
	if g.kind == "tree" {
		return Source{Repo: g.repo, Ref: ref, Path: joinPathPrefix(p, path)}
	}
	return Source{Repo: g.repo, Ref: ref, Path: p}
}

// offline resolves the URL without touching the network, treating the first
// segment as the ref. It's exact for a bare repo, a "-" default-branch URL, and a
// single-segment ref; for a slashed ref it's a best-effort guess (see ambiguous).
func (g githubURL) offline(path string) Source {
	if g.kind == "" || len(g.seg) == 0 {
		return g.source("", nil, path)
	}
	ref := g.seg[0]
	if ref == "-" { // "-" => default branch
		ref = ""
	}
	return g.source(ref, g.seg[1:], path)
}

// ambiguous reports whether the ref/path split needs the network: a multi-segment
// URL whose leading segment isn't the "-" default-branch marker could have a
// slashed ref. A blob's file is its last segment (so >=3 segments are needed for a
// slashed ref to be possible); a tree's directory can be empty (so >=2 suffice).
func (g githubURL) ambiguous() bool {
	if g.kind == "" || len(g.seg) == 0 || g.seg[0] == "-" {
		return false
	}
	if g.kind == "tree" {
		return len(g.seg) >= 2
	}
	return len(g.seg) >= 3
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

// joinPathPrefix composes a directory prefix (from a tree URL) with a path glob.
// A leading/trailing slash on the directory and a leading slash on the path are
// ignored. An empty path defaults to DefaultPath, so a bare tree directory scopes
// the default glob to that directory (e.g. dir "instructions" -> the source
// "instructions/**/*.instructions.md"); a non-empty path narrows within it (dir
// "instructions" + path "style/*.md" -> "instructions/style/*.md").
func joinPathPrefix(dir, path string) string {
	dir = strings.Trim(dir, "/")
	path = strings.TrimPrefix(strings.TrimSpace(path), "/")
	if path == "" {
		path = DefaultPath
	}
	if dir == "" {
		return path
	}
	return dir + "/" + path
}
