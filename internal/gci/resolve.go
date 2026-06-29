package gci

import (
	"fmt"
	"strings"
)

// ResolveSpec parses a source spec, resolving the ref/path split for a GitHub
// blob URL that has a slashed branch name (e.g. blob/feature/x/file.md). It
// probes candidate refs longest-first via the API and keeps the one that
// exists - mirroring how github.com itself disambiguates - so a non-blob spec,
// a bare repo URL, or a blob/-/ (default branch) URL needs no network.
func (a *App) ResolveSpec(spec string) (Source, error) {
	tail, ok := blobTail(spec)
	if !ok {
		return ParseSpec(spec) // not a non-default-branch blob URL
	}
	// tail = the segments after .../blob/ ; the last is the file, so a ref of
	// every leading prefix is a candidate (longest-first). git forbids one ref
	// from being a prefix directory of another, so at most one can exist.
	for k := len(tail) - 1; k >= 1; k-- {
		ref := strings.Join(tail[:k], "/")
		path := strings.Join(tail[k:], "/")
		if _, err := a.F.ResolveSHA(Source{Repo: blobRepo(spec), Ref: ref}); err == nil {
			return Source{Repo: blobRepo(spec), Ref: ref, Path: path}, nil
		}
	}
	return Source{}, fmt.Errorf("no branch, tag, or commit matched in %q", spec)
}

// blobRepo returns "owner/repo" for a github blob URL.
func blobRepo(spec string) string {
	u := strings.TrimPrefix(strings.TrimPrefix(spec, "https://"), "http://")
	u = strings.TrimPrefix(u, "github.com/")
	p := strings.Split(u, "/")
	if len(p) < 2 {
		return ""
	}
	return p[0] + "/" + p[1]
}

// blobTail returns the path segments after "owner/repo/blob/" for a non-default
// (non "-") blob URL with a multi-segment ref to disambiguate. It returns
// ok=false for everything ParseSpec already handles offline (plain specs, bare
// repo URLs, single-segment refs, and the blob/-/ default-branch form).
func blobTail(spec string) ([]string, bool) {
	u := strings.TrimPrefix(strings.TrimPrefix(spec, "https://"), "http://")
	if !strings.HasPrefix(u, "github.com/") {
		return nil, false
	}
	u = strings.TrimPrefix(u, "github.com/")
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	p := strings.Split(strings.Trim(u, "/"), "/")
	if len(p) < 6 || p[2] != "blob" || p[3] == "-" {
		return nil, false // ParseSpec covers these; ≥6 means a possible slashed ref
	}
	return p[3:], true // segments after owner/repo/blob/; includes ref+path
}
