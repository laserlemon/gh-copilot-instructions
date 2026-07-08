package gci

import (
	"errors"
	"testing"
)

// refFetcher exists only to answer "does this ref exist?" for ResolveSpec.
type refFetcher struct {
	exists map[string]bool
	calls  int
}

func (f *refFetcher) ResolveSHA(s Source) (string, error) {
	f.calls++
	if f.exists[s.Ref] {
		return "deadbeef", nil
	}
	return "", errors.New("not found")
}
func (f *refFetcher) Fetch(Source, func(string, int)) (string, []FetchedFile, error) {
	return "", nil, nil
}

func TestResolveSpecSlashedRef(t *testing.T) {
	f := &refFetcher{exists: map[string]bool{"foo": true}}
	a := &App{F: f}
	s, err := a.ResolveSpec("https://github.com/o/r/blob/foo/bar/baz")
	if err != nil {
		t.Fatal(err)
	}
	if s.Repo != "o/r" || s.Ref != "foo" || s.Path != "bar/baz" {
		t.Errorf("got {repo:%q ref:%q path:%q}, want {o/r foo bar/baz}", s.Repo, s.Ref, s.Path)
	}
}

// TestResolveSpecTreeSlashedRef probes a tree URL whose ref ("foo") is followed
// by a directory ("bar"); the directory becomes a prefix over the default glob.
func TestResolveSpecTreeSlashedRef(t *testing.T) {
	f := &refFetcher{exists: map[string]bool{"foo": true}}
	a := &App{F: f}
	s, err := a.ResolveSpec("https://github.com/o/r/tree/foo/bar")
	if err != nil {
		t.Fatal(err)
	}
	if s.Repo != "o/r" || s.Ref != "foo" || s.Path != "bar/**/*.instructions.md" {
		t.Errorf("got {repo:%q ref:%q path:%q}, want {o/r foo bar/**/*.instructions.md}", s.Repo, s.Ref, s.Path)
	}
}

// TestResolveSpecTreeRefSpansAllSegments verifies a tree URL can resolve to a
// slashed ref with an empty directory (the whole tail is the ref), so the source
// falls back to the default glob at that ref.
func TestResolveSpecTreeRefSpansAllSegments(t *testing.T) {
	f := &refFetcher{exists: map[string]bool{"foo/bar": true}}
	a := &App{F: f}
	s, err := a.ResolveSpec("https://github.com/o/r/tree/foo/bar")
	if err != nil {
		t.Fatal(err)
	}
	if s.Repo != "o/r" || s.Ref != "foo/bar" || s.Path != "**/*.instructions.md" {
		t.Errorf("got {repo:%q ref:%q path:%q}, want {o/r foo/bar **/*.instructions.md}", s.Repo, s.Ref, s.Path)
	}
}

// TestResolveSpecWithPathTree covers a tree URL combined with an explicit --path:
// the tree directory is prepended to the path glob. The "-" default-branch form
// needs no network.
func TestResolveSpecWithPathTree(t *testing.T) {
	f := &refFetcher{}
	a := &App{F: f}
	s, err := a.ResolveSpecWithPath("https://github.com/o/r/tree/-/instructions", "topics/*.md")
	if err != nil {
		t.Fatal(err)
	}
	if s.Repo != "o/r" || s.Ref != "" || s.Path != "instructions/topics/*.md" {
		t.Errorf("got {repo:%q ref:%q path:%q}, want {o/r \"\" instructions/topics/*.md}", s.Repo, s.Ref, s.Path)
	}
	if f.calls != 0 {
		t.Errorf("a -/ tree URL should make no API calls, got %d", f.calls)
	}
}

// TestResolveSpecWithPathIgnoredForBlob verifies --path does not leak onto a blob
// URL (which already carries its own path).
func TestResolveSpecWithPathIgnoredForBlob(t *testing.T) {
	f := &refFetcher{}
	a := &App{F: f}
	s, err := a.ResolveSpecWithPath("https://github.com/o/r/blob/main/x.md", "ignored/*.md")
	if err != nil {
		t.Fatal(err)
	}
	if s.Path != "x.md" {
		t.Errorf("blob path = %q, want x.md (--path ignored)", s.Path)
	}
}

func TestResolveSpecOfflineCases(t *testing.T) {
	f := &refFetcher{}
	a := &App{F: f}
	// blob/-/ default branch, bare spec, single-seg blob, a "-" tree, and a
	// single-segment tree ref all need no probing.
	for _, spec := range []string{
		"https://github.com/o/r/blob/-/a/b.md",
		"o/r:p",
		"https://github.com/o/r/blob/main/x.md",
		"https://github.com/o/r/tree/-/instructions",
		"https://github.com/o/r/tree/main",
	} {
		if _, err := a.ResolveSpec(spec); err != nil {
			t.Errorf("ResolveSpec(%q): %v", spec, err)
		}
	}
	if f.calls != 0 {
		t.Errorf("offline specs should make no API calls, got %d", f.calls)
	}
}
