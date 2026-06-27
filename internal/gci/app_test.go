package gci

import (
	"bytes"
	"os"
	"path/filepath"
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

func (f *fakeFetcher) Fetch(s Source) (string, string, []FetchedFile, error) {
	f.fetches++
	return f.sha[s.ID()], "main", f.files[s.ID()], nil
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

func ls(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return names
	}
	for _, e := range entries {
		names = append(names, e.Name())
	}
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
