package gci

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeFetcher serves canned content keyed by source id, with a controllable SHA
// and a call counter to assert skip-when-unchanged.
type fakeFetcher struct {
	sha      map[string]string
	files    map[string][]FetchedFile
	fetches  int
	resolves int
}

func (f *fakeFetcher) ResolveSHA(s Source) (string, error) {
	f.resolves++
	return f.sha[s.ID()], nil
}

func (f *fakeFetcher) Fetch(s Source, onProgress func(sha string, files int)) (string, []FetchedFile, error) {
	f.fetches++
	sha := f.sha[s.ID()]
	files := f.files[s.ID()]
	if onProgress != nil {
		onProgress(sha, 0)
		for i := range files {
			onProgress(sha, i+1)
		}
	}
	return sha, files, nil
}

func newTestApp(t *testing.T, f fetcher) *App {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(EnvSources, "")
	t.Setenv(EnvToken, "")
	t.Setenv(EnvRef, "")
	return &App{Paths: DefaultPaths(), F: f, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
}

func instDir(a *App) string { return a.Paths.InstallDir }

// ls returns the relative paths of all files (recursively) under dir, so tests
// see our nested-layout install files regardless of directory depth.
func ls(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		names = append(names, rel)
		return nil
	})
	return names
}

func TestPullInstallSkipPrune(t *testing.T) {
	src, _ := ParseSpec("o/r")
	id := src.ID()
	f := &fakeFetcher{
		sha: map[string]string{id: "sha1111111111111111111111111111111111111"},
		files: map[string][]FetchedFile{id: {
			{Rel: "general.instructions.md", Content: []byte("---\napplyTo: '**'\n---\nA")},
			{Rel: "ruby.instructions.md", Content: []byte("B")},
		}},
	}
	a := newTestApp(t, f)
	if err := a.Paths.AddSource(src); err != nil {
		t.Fatal(err)
	}

	// First pull installs both files.
	if err := a.Pull(""); err != nil {
		t.Fatal(err)
	}
	got := ls(t, instDir(a))
	if len(got) != 2 {
		t.Fatalf("expected 2 installed files, got %v", got)
	}
	if f.fetches != 1 {
		t.Fatalf("expected 1 fetch, got %d", f.fetches)
	}

	// Second pull at the same SHA must SKIP (no new fetch).
	if err := a.Pull(""); err != nil {
		t.Fatal(err)
	}
	if f.fetches != 1 {
		t.Fatalf("skip-when-unchanged failed: fetches=%d", f.fetches)
	}

	// New commit drops a file -> re-pull installs new set and prunes the old one.
	f.sha[id] = "sha2222222222222222222222222222222222222"
	f.files[id] = []FetchedFile{{Rel: "general.instructions.md", Content: []byte("A2")}}
	if err := a.Pull(""); err != nil {
		t.Fatal(err)
	}
	if f.fetches != 2 {
		t.Fatalf("expected re-pull, fetches=%d", f.fetches)
	}
	got = ls(t, instDir(a))
	if len(got) != 1 {
		t.Fatalf("prune failed, files=%v", got)
	}

	// Deleting an installed file must trigger a re-pull even at the same SHA.
	os.Remove(filepath.Join(instDir(a), got[0]))
	if err := a.Pull(""); err != nil {
		t.Fatal(err)
	}
	if f.fetches != 3 {
		t.Fatalf("missing-file should re-pull, fetches=%d", f.fetches)
	}
}

func TestPullCollisionKeepsFirst(t *testing.T) {
	src, _ := ParseSpec("o/r:**")
	id := src.ID()
	// "foo", "foo.md", and "foo.instructions.md" all normalize to the same
	// install name. By lexicographic path "foo" sorts first, but the suffix
	// priority must keep "foo.instructions.md" instead.
	f := &fakeFetcher{
		sha: map[string]string{id: "sha0000000000000000000000000000000000000"},
		files: map[string][]FetchedFile{id: {
			{Rel: "foo", Content: []byte("from-bare")},
			{Rel: "foo.md", Content: []byte("from-md")},
			{Rel: "foo.instructions.md", Content: []byte("from-instructions")},
		}},
	}
	a := newTestApp(t, f)
	var errBuf bytes.Buffer
	a.Err = &errBuf
	if err := a.Paths.AddSource(src); err != nil {
		t.Fatal(err)
	}
	if err := a.Pull(""); err != nil {
		t.Fatal(err)
	}
	got := ls(t, instDir(a))
	if len(got) != 1 {
		t.Fatalf("collision should install exactly one file, got %v", got)
	}
	// ".instructions.md" beats ".md" beats bare, regardless of path ordering.
	data, _ := os.ReadFile(filepath.Join(instDir(a), filepath.FromSlash(src.DestPath("foo.instructions.md"))))
	if string(data) != "from-instructions" {
		t.Errorf("expected the .instructions.md source to win, content = %q", data)
	}
	if !strings.Contains(errBuf.String(), "both map to") {
		t.Errorf("expected a collision warning, stderr = %q", errBuf.String())
	}
}

func TestRemoveAllLeavesUserFile(t *testing.T) {
	src, _ := ParseSpec("o/r")
	id := src.ID()
	f := &fakeFetcher{
		sha:   map[string]string{id: "abc1234def"},
		files: map[string][]FetchedFile{id: {{Rel: "x.instructions.md", Content: []byte("x")}}},
	}
	a := newTestApp(t, f)
	if err := a.Paths.AddSource(src); err != nil {
		t.Fatal(err)
	}
	if err := a.Pull(""); err != nil {
		t.Fatal(err)
	}

	// A hand-written user instruction file that we must never touch.
	userFile := filepath.Join(instDir(a), "my-own.instructions.md")
	if err := os.WriteFile(userFile, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := a.RemoveAll(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(userFile); err != nil {
		t.Fatalf("user file was removed: %v", err)
	}
	for _, n := range ls(t, instDir(a)) {
		if isOurs(n) {
			t.Fatalf("our file survived remove --all: %s", n)
		}
	}
}

func TestRemoveOneByRepo(t *testing.T) {
	a := newTestApp(t, &fakeFetcher{
		sha: map[string]string{},
	})
	s1, _ := ParseSpec("o/one")
	s2, _ := ParseSpec("o/two")
	a.F = &fakeFetcher{
		sha: map[string]string{s1.ID(): "111", s2.ID(): "222"},
		files: map[string][]FetchedFile{
			s1.ID(): {{Rel: "a.instructions.md", Content: []byte("a")}},
			s2.ID(): {{Rel: "b.instructions.md", Content: []byte("b")}},
		},
	}
	if err := a.Paths.AddSource(s1); err != nil {
		t.Fatal(err)
	}
	if err := a.Paths.AddSource(s2); err != nil {
		t.Fatal(err)
	}
	if err := a.Pull(""); err != nil {
		t.Fatal(err)
	}
	if len(ls(t, instDir(a))) != 2 {
		t.Fatalf("setup: want 2 files, got %v", ls(t, instDir(a)))
	}
	if err := a.Remove("o/one"); err != nil {
		t.Fatal(err)
	}
	files := ls(t, instDir(a))
	if len(files) != 1 {
		t.Fatalf("remove one: want 1 file, got %v", files)
	}
	st, _ := a.Paths.LoadState()
	if _, ok := st.Sources[s1.ID()]; ok {
		t.Fatal("state for removed source remains")
	}
	if _, ok := st.Sources[s2.ID()]; !ok {
		t.Fatal("state for kept source missing")
	}
}

func TestEnvOverridesFile(t *testing.T) {
	a := newTestApp(t, &fakeFetcher{})
	fileSrc, _ := ParseSpec("o/fromfile")
	if err := a.Paths.AddSource(fileSrc); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvSources, "o/fromenv")
	srcs, origin, err := a.Paths.LoadSources()
	if err != nil {
		t.Fatal(err)
	}
	if origin != OriginEnv || len(srcs) != 1 || srcs[0].Repo != "o/fromenv" {
		t.Fatalf("env should override file: origin=%v srcs=%+v", origin, srcs)
	}
}

func TestRenderRaw(t *testing.T) {
	a := newTestApp(t, &fakeFetcher{})
	// Two sources: one with an inline token, one without; plus a ref+path.
	s1, _ := ParseSpec("acme/standards@main:**/*.instructions.md")
	s1.Token = "github_pat_SECRET"
	s2, _ := ParseSpec("oss/public-rules")
	if err := a.Paths.AddSource(s1); err != nil {
		t.Fatal(err)
	}
	if err := a.Paths.AddSource(s2); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	a.Out = &buf
	if err := a.RenderList(false, true); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	want := "acme/standards@main:**/*.instructions.md   github_pat_SECRET\noss/public-rules\n"
	if got != want {
		t.Fatalf("renderRaw =\n%q\nwant\n%q", got, want)
	}
	// Must be free of color, headers, and comments (pasteable as-is).
	if strings.Contains(got, "\x1b") || strings.Contains(got, "#") || strings.Contains(got, "ID") {
		t.Errorf("raw output not clean: %q", got)
	}
}

func TestRenderRawRoundTripsThroughEnv(t *testing.T) {
	a := newTestApp(t, &fakeFetcher{})
	s, _ := ParseSpec("o/r@v1:dir")
	s.Token = "tok123"
	if err := a.Paths.AddSource(s); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	a.Out = &buf
	if err := a.RenderList(false, true); err != nil {
		t.Fatal(err)
	}
	// Feeding the raw output back through the env var yields the same source.
	t.Setenv(EnvSources, buf.String())
	srcs, origin, err := a.Paths.LoadSources()
	if err != nil || origin != OriginEnv || len(srcs) != 1 {
		t.Fatalf("round-trip load: origin=%v srcs=%+v err=%v", origin, srcs, err)
	}
	if srcs[0].ID() != s.ID() || srcs[0].Token != "tok123" {
		t.Fatalf("round-trip mismatch: %+v", srcs[0])
	}
}

func TestListRowState(t *testing.T) {
	src, _ := ParseSpec("o/r")
	id := src.ID()
	f := &fakeFetcher{
		sha:   map[string]string{id: "abc1234def5678"},
		files: map[string][]FetchedFile{id: {{Rel: "a.instructions.md", Content: []byte("a")}}},
	}
	a := newTestApp(t, f)

	// Configured but never pulled => PENDING.
	if err := a.Paths.AddSource(src); err != nil {
		t.Fatal(err)
	}
	rows, _, err := a.ListRows()
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[0].State != StatePending {
		t.Errorf("never-pulled state = %q, want %q", rows[0].State, StatePending)
	}

	// After a successful pull => PULLED.
	if err := a.Pull(""); err != nil {
		t.Fatal(err)
	}
	rows, _, _ = a.ListRows()
	if rows[0].State != StatePulled {
		t.Errorf("pulled state = %q, want %q", rows[0].State, StatePulled)
	}

	// Installed file removed out from under us => FAILED.
	for _, ss := range mustState(t, a).Sources {
		for _, fn := range ss.Files {
			os.Remove(filepath.Join(a.Paths.InstallDir, filepath.FromSlash(fn)))
		}
	}
	rows, _, _ = a.ListRows()
	if rows[0].State != StateFailed {
		t.Errorf("broken-install state = %q, want %q", rows[0].State, StateFailed)
	}
}

func mustState(t *testing.T, a *App) *State {
	t.Helper()
	st, err := a.Paths.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestPullSkipsViaShaPrefixWithoutNetwork(t *testing.T) {
	// Source pinned to a 7-hex prefix of the commit it resolves to.
	src, _ := ParseSpec("o/r@e28eb6d")
	id := src.ID()
	f := &fakeFetcher{
		sha:   map[string]string{id: "e28eb6df72fb90a84015cb6fda9104bff345ae48"},
		files: map[string][]FetchedFile{id: {{Rel: "a.instructions.md", Content: []byte("a")}}},
	}
	a := newTestApp(t, f)
	if err := a.Paths.AddSource(src); err != nil {
		t.Fatal(err)
	}

	// First pull fetches once and records the full SHA.
	if err := a.Pull(""); err != nil {
		t.Fatal(err)
	}
	if f.fetches != 1 {
		t.Fatalf("first pull fetches = %d, want 1", f.fetches)
	}
	resolvesAfterFirst := f.resolves

	// Second pull must skip with NO network at all: the pinned ref is a prefix
	// of the recorded SHA, so neither ResolveSHA nor Fetch should be called.
	if err := a.Pull(""); err != nil {
		t.Fatal(err)
	}
	if f.fetches != 1 {
		t.Errorf("second pull triggered a fetch (fetches=%d)", f.fetches)
	}
	if f.resolves != resolvesAfterFirst {
		t.Errorf("second pull made a ResolveSHA call (resolves went %d -> %d)", resolvesAfterFirst, f.resolves)
	}
}
