package gci

import (
	"fmt"
	"strings"
)

// ResolveSpec parses a source spec. For a GitHub blob or tree URL whose branch
// name contains slashes, it probes candidate refs against the API (longest-first)
// and keeps the one that exists - mirroring how github.com itself disambiguates -
// so a plain spec, a bare repo URL, or a "-" default-branch URL needs no network.
func (a *App) ResolveSpec(spec string) (Source, error) {
	return a.ResolveSpecWithPath(spec, "")
}

// ResolveSpecWithPath is ResolveSpec with an explicit path to compose onto a tree
// URL's directory: the tree URL contributes a directory prefix and path (the
// --path flag) narrows within it, defaulting to the whole-directory glob when
// empty. path is ignored for blob and bare-repo URLs and for plain specs, which
// already carry their own path.
func (a *App) ResolveSpecWithPath(spec, path string) (Source, error) {
	g, ok, err := parseGitHubURL(spec)
	if !ok {
		return ParseSpec(spec) // not a github web URL
	}
	if err != nil {
		return Source{}, err
	}
	if !g.ambiguous() {
		return g.offline(path), nil
	}
	// A slashed ref is possible: the ref is some leading run of the segments and
	// the rest is the file (blob) or directory (tree). git forbids one ref from
	// being a prefix directory of another, so at most one candidate can exist. A
	// tree's directory may be empty, so its ref can span every segment; a blob's
	// file is the last segment, so its ref stops one short.
	maxRef := len(g.seg) - 1
	if g.kind == "tree" {
		maxRef = len(g.seg)
	}
	for k := maxRef; k >= 1; k-- {
		ref := strings.Join(g.seg[:k], "/")
		if _, err := a.F.ResolveSHA(Source{Repo: g.repo, Ref: ref}); err == nil {
			return g.source(ref, g.seg[k:], path), nil
		}
	}
	return Source{}, fmt.Errorf("no branch, tag, or commit matched in %q", spec)
}
