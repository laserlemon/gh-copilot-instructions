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

func TestResolveSpecOfflineCases(t *testing.T) {
	f := &refFetcher{}
	a := &App{F: f}
	// blob/-/ default branch, bare spec, and single-seg blob need no probing.
	for _, spec := range []string{"https://github.com/o/r/blob/-/a/b.md", "o/r:p", "https://github.com/o/r/blob/main/x.md"} {
		if _, err := a.ResolveSpec(spec); err != nil {
			t.Errorf("ResolveSpec(%q): %v", spec, err)
		}
	}
	if f.calls != 0 {
		t.Errorf("offline specs should make no API calls, got %d", f.calls)
	}
}
