package gci

import (
	"bytes"
	"encoding/json"
	"errors"
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
	if err := a.Pull("", false); err != nil {
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
	if err := a.Pull("", false); err != nil {
		t.Fatal(err)
	}
	if f.fetches != 1 {
		t.Fatalf("skip-when-unchanged failed: fetches=%d", f.fetches)
	}

	// New commit drops a file -> re-pull installs new set and prunes the old one.
	f.sha[id] = "sha2222222222222222222222222222222222222"
	f.files[id] = []FetchedFile{{Rel: "general.instructions.md", Content: []byte("A2")}}
	if err := a.Pull("", false); err != nil {
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
	if err := a.Pull("", false); err != nil {
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
	if err := a.Pull("", false); err != nil {
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
	if err := a.Pull("", false); err != nil {
		t.Fatal(err)
	}

	// A hand-written user instruction file that we must never touch.
	userFile := filepath.Join(instDir(a), "my-own.instructions.md")
	if err := os.WriteFile(userFile, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := a.RemoveAll(false); err != nil {
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
	if err := a.Pull("", false); err != nil {
		t.Fatal(err)
	}
	if len(ls(t, instDir(a))) != 2 {
		t.Fatalf("setup: want 2 files, got %v", ls(t, instDir(a)))
	}
	if err := a.Remove("o/one", false); err != nil {
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
	if strings.Contains(got, "\x1b") || strings.Contains(got, "#") || strings.Contains(got, "SLUG") {
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
	if err := a.Pull("", false); err != nil {
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
	if err := a.Pull("", false); err != nil {
		t.Fatal(err)
	}
	if f.fetches != 1 {
		t.Fatalf("first pull fetches = %d, want 1", f.fetches)
	}
	resolvesAfterFirst := f.resolves

	// Second pull must skip with NO network at all: the pinned ref is a prefix
	// of the recorded SHA, so neither ResolveSHA nor Fetch should be called.
	if err := a.Pull("", false); err != nil {
		t.Fatal(err)
	}
	if f.fetches != 1 {
		t.Errorf("second pull triggered a fetch (fetches=%d)", f.fetches)
	}
	if f.resolves != resolvesAfterFirst {
		t.Errorf("second pull made a ResolveSHA call (resolves went %d -> %d)", resolvesAfterFirst, f.resolves)
	}
}

func TestPullSourceUpdatedFlag(t *testing.T) {
	src, _ := ParseSpec("o/r")
	id := src.ID()
	a := newTestApp(t, &fakeFetcher{
		sha:   map[string]string{id: "newsha1111111111111111111111111111111111"},
		files: map[string][]FetchedFile{id: {{Rel: "a.instructions.md", Content: []byte("a")}}},
	})

	// Brand-new source (no prior state) is "pulled", not "updated".
	if out := a.pullSource(src, SourceState{}, false, nil); out.updated {
		t.Errorf("brand-new source should not be updated")
	}
	// Existing source whose SHA moved is "updated".
	if out := a.pullSource(src, SourceState{Repo: "o/r", SHA: "oldsha"}, true, nil); !out.updated {
		t.Errorf("moved existing source should be updated")
	}
	// Existing source at the same SHA is not "updated".
	if out := a.pullSource(src, SourceState{Repo: "o/r", SHA: "newsha1111111111111111111111111111111111"}, true, nil); out.updated {
		t.Errorf("unchanged existing source should not be updated")
	}
}

func TestPullJSON(t *testing.T) {
	sNew, _ := ParseSpec("o/new")
	sMoved, _ := ParseSpec("o/moved")
	a := newTestApp(t, &fakeFetcher{
		sha: map[string]string{
			sNew.ID():   "sha1111111111111111111111111111111111111",
			sMoved.ID(): "sha2222222222222222222222222222222222222",
		},
		files: map[string][]FetchedFile{
			sNew.ID():   {{Rel: "a.instructions.md", Content: []byte("a")}},
			sMoved.ID(): {{Rel: "b.instructions.md", Content: []byte("b")}},
		},
	})
	if err := a.Paths.AddSource(sNew); err != nil {
		t.Fatal(err)
	}
	if err := a.Paths.AddSource(sMoved); err != nil {
		t.Fatal(err)
	}
	// Pre-seed the "moved" source with an older SHA.
	st, _ := a.Paths.LoadState()
	st.Sources[sMoved.ID()] = SourceState{Repo: "o/moved", SHA: "oldshaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := a.Paths.Save(st); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	a.Out = &buf
	if err := a.Pull("", true); err != nil {
		t.Fatal(err)
	}
	var res []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	got := map[string]string{}
	refs := map[string]string{}
	for _, r := range res {
		got[r["repository"].(string)] = r["state"].(string)
		refs[r["repository"].(string)] = r["ref"].(string)
	}
	if got["o/new"] != "PULLED" {
		t.Errorf("new source state = %q, want PULLED", got["o/new"])
	}
	if got["o/moved"] != "UPDATED" {
		t.Errorf("moved source state = %q, want UPDATED", got["o/moved"])
	}
	// A default-branch ref is rendered as "-" (a usable GitHub blob URL ref).
	if refs["o/new"] != "-" {
		t.Errorf("default-branch ref = %q, want \"-\"", refs["o/new"])
	}
}

func TestAddJSON(t *testing.T) {
	s, _ := ParseSpec("o/added")
	a := newTestApp(t, &fakeFetcher{
		sha:   map[string]string{s.ID(): "sha1111111111111111111111111111111111111"},
		files: map[string][]FetchedFile{s.ID(): {{Rel: "a.instructions.md", Content: []byte("a")}}},
	})
	var buf bytes.Buffer
	a.Out = &buf
	if err := a.Add(s, true); err != nil {
		t.Fatal(err)
	}
	var res []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if len(res) != 1 {
		t.Fatalf("add --json: want 1 element, got %d", len(res))
	}
	if res[0]["repository"] != "o/added" || res[0]["state"] != "PULLED" {
		t.Errorf("add --json element = %v, want o/added PULLED", res[0])
	}
}

func TestRemoveJSONReturnsRemaining(t *testing.T) {
	s1, _ := ParseSpec("o/one")
	s2, _ := ParseSpec("o/two")
	a := newTestApp(t, &fakeFetcher{
		sha: map[string]string{s1.ID(): "111", s2.ID(): "222"},
		files: map[string][]FetchedFile{
			s1.ID(): {{Rel: "a.instructions.md", Content: []byte("a")}},
			s2.ID(): {{Rel: "b.instructions.md", Content: []byte("b")}},
		},
	})
	if err := a.Paths.AddSource(s1); err != nil {
		t.Fatal(err)
	}
	if err := a.Paths.AddSource(s2); err != nil {
		t.Fatal(err)
	}
	if err := a.Pull("", false); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	a.Out = &buf
	if err := a.Remove("o/one", true); err != nil {
		t.Fatal(err)
	}
	var res []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if len(res) != 1 {
		t.Fatalf("remove --json: want 1 remaining source, got %d: %s", len(res), buf.String())
	}
	if res[0]["repository"] != "o/two" {
		t.Errorf("remaining source = %v, want o/two", res[0]["repository"])
	}
}

func TestRemoveAllJSONReturnsEmptyArray(t *testing.T) {
	s, _ := ParseSpec("o/one")
	a := newTestApp(t, &fakeFetcher{
		sha:   map[string]string{s.ID(): "111"},
		files: map[string][]FetchedFile{s.ID(): {{Rel: "a.instructions.md", Content: []byte("a")}}},
	})
	if err := a.Paths.AddSource(s); err != nil {
		t.Fatal(err)
	}
	if err := a.Pull("", false); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	a.Out = &buf
	if err := a.RemoveAll(true); err != nil {
		t.Fatal(err)
	}
	// Must be an empty JSON array, never null.
	if got := strings.TrimSpace(buf.String()); got != "[]" {
		t.Errorf("remove --all --json = %q, want []", got)
	}
}

func TestPullFilterTargetsOneSource(t *testing.T) {
	s1, _ := ParseSpec("o/one")
	s2, _ := ParseSpec("o/two")
	f := &fakeFetcher{
		sha: map[string]string{s1.ID(): "111", s2.ID(): "222"},
		files: map[string][]FetchedFile{
			s1.ID(): {{Rel: "a.instructions.md", Content: []byte("a")}},
			s2.ID(): {{Rel: "b.instructions.md", Content: []byte("b")}},
		},
	}
	a := newTestApp(t, f)
	if err := a.Paths.AddSource(s1); err != nil {
		t.Fatal(err)
	}
	if err := a.Paths.AddSource(s2); err != nil {
		t.Fatal(err)
	}
	// A filtered pull touches only the matched source, but --json now returns the
	// full current source list (o/one pulled, o/two still pending).
	var buf bytes.Buffer
	a.Out = &buf
	if err := a.Pull("o/one", true); err != nil {
		t.Fatal(err)
	}
	var res []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	byRepo := map[string]string{}
	for _, r := range res {
		byRepo[r["repository"].(string)] = r["state"].(string)
	}
	if byRepo["o/one"] != "PULLED" || byRepo["o/two"] != "PENDING" {
		t.Fatalf("pull o/one --json = %s, want o/one PULLED + o/two PENDING", buf.String())
	}
	if f.fetches != 1 {
		t.Errorf("fetches = %d, want 1 (only the matched source)", f.fetches)
	}
}

func TestWriteJSONCompact(t *testing.T) {
	a := newTestApp(t, &fakeFetcher{})
	var buf bytes.Buffer
	a.Out = &buf
	// A non-TTY App (the test default) writes compact JSON plus one newline.
	if err := a.writeJSON([]sourceJSON{{State: "pulled", ID: "abc", Repo: "o/r"}}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, "\n  ") {
		t.Errorf("piped JSON should be compact, got:\n%s", got)
	}
	if !strings.HasSuffix(got, "}]\n") {
		t.Errorf("piped JSON should end with a single trailing newline, got %q", got)
	}
}

// errFetcher fails every fetch, simulating a hard error (repo not found, tree
// too large, network, ...).
type errFetcher struct{ err error }

func (f *errFetcher) ResolveSHA(Source) (string, error) { return "", f.err }
func (f *errFetcher) Fetch(Source, func(string, int)) (string, []FetchedFile, error) {
	return "", nil, f.err
}

// TestFailedAddRecordsFailedState is the #9 regression: a failed add must be
// recorded as FAILED (not left PENDING), and the attempt must stamp PulledAt.
func TestFailedAddRecordsFailedState(t *testing.T) {
	s, _ := ParseSpec("o/bad")
	a := newTestApp(t, &errFetcher{err: errors.New("tree too large (truncated)")})
	if err := a.Add(s, false); err == nil {
		t.Fatal("a failing add should return the pull error")
	}
	st, _ := a.Paths.LoadState()
	ss, ok := st.Sources[s.ID()]
	if !ok {
		t.Fatal("a failed add should record a state entry, not leave the source PENDING")
	}
	if ss.PulledAt.IsZero() {
		t.Error("a failed attempt should stamp PulledAt (the attempt time)")
	}
	rows, _, err := a.ListRows()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].State != StateFailed {
		t.Fatalf("list should show the failed source as FAILED, got %+v", rows)
	}
	if rows[0].PulledAt.IsZero() {
		t.Error("the FAILED row should carry the attempt's PulledAt")
	}
}

// TestFailedRepullKeepsExistingInstall verifies a transient re-pull error does
// not destroy a previously-good install or downgrade it to FAILED.
func TestFailedRepullKeepsExistingInstall(t *testing.T) {
	src, _ := ParseSpec("o/r")
	id := src.ID()
	f := &fakeFetcher{
		sha:   map[string]string{id: "sha1111111111111111111111111111111111111"},
		files: map[string][]FetchedFile{id: {{Rel: "a.instructions.md", Content: []byte("a")}}},
	}
	a := newTestApp(t, f)
	if err := a.Paths.AddSource(src); err != nil {
		t.Fatal(err)
	}
	if err := a.Pull("", false); err != nil {
		t.Fatal(err)
	}
	// Now make re-pull fail, and pull again.
	a.F = &errFetcher{err: errors.New("network")}
	_ = a.Pull("", false)
	rows, _, _ := a.ListRows()
	if len(rows) != 1 || rows[0].State != StatePulled {
		t.Fatalf("an existing healthy source should stay PULLED after a failed re-pull, got %+v", rows)
	}
}

// TestJSONFieldNames locks the public --json field names: slug/repository/
// fileCount (not the old id/repo/files, and not the internal state.json keys).
func TestJSONFieldNames(t *testing.T) {
	a := newTestApp(t, &fakeFetcher{})
	var buf bytes.Buffer
	a.Out = &buf
	if err := a.writeJSON([]sourceJSON{{State: "pulled", ID: "abc", Repo: "o/r", SHA: "deadbeef", Files: 3}}); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	for _, k := range []string{`"slug"`, `"repository"`, `"fileCount"`} {
		if !strings.Contains(s, k) {
			t.Errorf("missing %s in %s", k, s)
		}
	}
	for _, k := range []string{`"id"`, `"repo"`, `"files"`} {
		if strings.Contains(s, k) {
			t.Errorf("unexpected old key %s in %s", k, s)
		}
	}
}

// TestStateCasingConvention locks state casing: the state word is UPPERCASE on
// every surface that shows it as a word (piped TSV and JSON); the TTY uses a
// glyph instead.
func TestStateCasingConvention(t *testing.T) {
	a := &App{}
	cs := &ColorScheme{enabled: false}
	rows := []Row{
		{State: StatePulled, ID: "a", Repo: "o/r"},
		{State: StatePending, ID: "b", Repo: "o/s"},
		{State: StateFailed, ID: "c", Repo: "o/t"},
	}
	var tsv bytes.Buffer
	if err := a.renderTable(&tsv, staticViews(rows), false, 100, cs); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"PULLED", "PENDING", "FAILED"} {
		if !strings.Contains(tsv.String(), w) {
			t.Errorf("piped TSV should contain uppercase %q:\n%s", w, tsv.String())
		}
	}
	var jb bytes.Buffer
	a.Out = &jb
	if err := a.renderListJSON(rows); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{`"PULLED"`, `"PENDING"`, `"FAILED"`} {
		if !strings.Contains(jb.String(), w) {
			t.Errorf("--json should contain uppercase %s:\n%s", w, jb.String())
		}
	}
	if strings.Contains(jb.String(), `"pulled"`) {
		t.Errorf("--json should not contain a lowercase state:\n%s", jb.String())
	}
}

// TestPullJSONReturnsFullListWithUpdated: pull --json returns the full source
// list, marking a moved source UPDATED and leaving an untouched one PENDING.
func TestPullJSONReturnsFullListWithUpdated(t *testing.T) {
	s1, _ := ParseSpec("o/one")
	s2, _ := ParseSpec("o/two")
	a := newTestApp(t, &fakeFetcher{
		sha:   map[string]string{s1.ID(): "aaa1111111111111111111111111111111111111"},
		files: map[string][]FetchedFile{s1.ID(): {{Rel: "a.instructions.md", Content: []byte("a")}}},
	})
	a.Paths.AddSource(s1)
	a.Paths.AddSource(s2)
	st, _ := a.Paths.LoadState()
	st.Sources[s1.ID()] = SourceState{Repo: "o/one", SHA: "old", Files: []string{"x"}}
	a.Paths.Save(st)
	var buf bytes.Buffer
	a.Out = &buf
	_ = a.Pull("o/one", true)
	var res []map[string]any
	json.Unmarshal(buf.Bytes(), &res)
	got := map[string]string{}
	for _, r := range res {
		got[r["repository"].(string)] = r["state"].(string)
	}
	if got["o/one"] != "UPDATED" || got["o/two"] != "PENDING" {
		t.Errorf("got %v, want o/one UPDATED + o/two PENDING", got)
	}
}

func TestPullWarnsMissingApplyTo(t *testing.T) {
	src, _ := ParseSpec("o/r")
	id := src.ID()
	f := &fakeFetcher{
		sha: map[string]string{id: "sha1111111111111111111111111111111111111"},
		files: map[string][]FetchedFile{id: {
			{Rel: "with.instructions.md", Content: []byte("---\napplyTo: '**'\n---\nok")},
			{Rel: "without.instructions.md", Content: []byte("no frontmatter here")},
		}},
	}
	a := newTestApp(t, f)
	if err := a.Paths.AddSource(src); err != nil {
		t.Fatal(err)
	}
	if err := a.Pull("", false); err != nil {
		t.Fatal(err)
	}
	msg := a.Err.(*bytes.Buffer).String()
	if !strings.Contains(msg, "applyTo") || !strings.Contains(msg, "1 of 2") {
		t.Fatalf("expected an applyTo warning mentioning 1 of 2, got: %q", msg)
	}
}

func TestPullNoApplyToWarningWhenAllTagged(t *testing.T) {
	src, _ := ParseSpec("o/r2")
	id := src.ID()
	f := &fakeFetcher{
		sha: map[string]string{id: "sha2222222222222222222222222222222222222"},
		files: map[string][]FetchedFile{id: {
			{Rel: "a.instructions.md", Content: []byte("---\napplyTo: '**'\n---\na")},
		}},
	}
	a := newTestApp(t, f)
	if err := a.Paths.AddSource(src); err != nil {
		t.Fatal(err)
	}
	if err := a.Pull("", false); err != nil {
		t.Fatal(err)
	}
	if msg := a.Err.(*bytes.Buffer).String(); strings.Contains(msg, "applyTo") {
		t.Fatalf("did not expect an applyTo warning, got: %q", msg)
	}
}

func TestRemoveBySlugAndVariant(t *testing.T) {
	def, _ := ParseSpec("o/one")
	v2, _ := ParseSpec("o/one@v2")
	f := &fakeFetcher{
		sha: map[string]string{def.ID(): "d100000000000000000000000000000000000000", v2.ID(): "v200000000000000000000000000000000000000"},
		files: map[string][]FetchedFile{
			def.ID(): {{Rel: "a.instructions.md", Content: []byte("---\napplyTo: '**'\n---\na")}},
			v2.ID():  {{Rel: "b.instructions.md", Content: []byte("---\napplyTo: '**'\n---\nb")}},
		},
	}
	a := newTestApp(t, f)
	if err := a.Paths.AddSource(def); err != nil {
		t.Fatal(err)
	}
	if err := a.Paths.AddSource(v2); err != nil {
		t.Fatal(err)
	}
	if err := a.Pull("", false); err != nil {
		t.Fatal(err)
	}

	// A bare owner/repo targets only the default variant (parity with add), not @v2.
	if err := a.Remove("o/one", false); err != nil {
		t.Fatal(err)
	}
	st, _ := a.Paths.LoadState()
	if _, ok := st.Sources[def.ID()]; ok {
		t.Fatal("default variant should be removed by owner/repo")
	}
	if _, ok := st.Sources[v2.ID()]; !ok {
		t.Fatal("the @v2 variant should remain (not a fuzzy repo match)")
	}

	// The @v2 variant is removable by its exact spec or its slug.
	if err := a.Remove(v2.ID(), false); err != nil {
		t.Fatal(err)
	}
	st, _ = a.Paths.LoadState()
	if _, ok := st.Sources[v2.ID()]; ok {
		t.Fatal("@v2 should be removed by slug")
	}
	srcs, _, _ := a.Paths.LoadSources()
	if len(srcs) != 0 {
		t.Fatalf("config should be empty after removing both, got %d", len(srcs))
	}
}

func TestRemoveByFullSpec(t *testing.T) {
	v2, _ := ParseSpec("o/one@v2:sub")
	f := &fakeFetcher{
		sha:   map[string]string{v2.ID(): "v200000000000000000000000000000000000000"},
		files: map[string][]FetchedFile{v2.ID(): {{Rel: "sub/b.instructions.md", Content: []byte("---\napplyTo: '**'\n---\nb")}}},
	}
	a := newTestApp(t, f)
	if err := a.Paths.AddSource(v2); err != nil {
		t.Fatal(err)
	}
	if err := a.Pull("", false); err != nil {
		t.Fatal(err)
	}
	if err := a.Remove("o/one@v2:sub", false); err != nil {
		t.Fatal(err)
	}
	st, _ := a.Paths.LoadState()
	if _, ok := st.Sources[v2.ID()]; ok {
		t.Fatal("source should be removed by its full spec")
	}
}

// TestFailedAddReportsAndHints verifies a permission-style failure is reported
// once (ErrReported, so main won't double-print), with a red ✗ line and the gray
// --token hint; a non-permission failure reports without the hint.
func TestFailedAddReportsAndHints(t *testing.T) {
	s, _ := ParseSpec("o/bad")

	// 404-style: expect the ✗ line and the --token hint.
	a := newTestApp(t, &errFetcher{err: errors.New("HTTP 404: Not Found")})
	err := a.Add(s, false)
	if !errors.Is(err, ErrReported) {
		t.Fatalf("failed add should return ErrReported, got %v", err)
	}
	out := a.Err.(*bytes.Buffer).String()
	if !strings.Contains(out, "o/bad: HTTP 404") {
		t.Errorf("missing failure line: %q", out)
	}
	if !strings.Contains(out, "--token") {
		t.Errorf("permission failure should include the --token hint: %q", out)
	}

	// Non-permission error: reported, but no --token hint.
	a2 := newTestApp(t, &errFetcher{err: errors.New("tree too large (truncated)")})
	if err := a2.Add(s, false); !errors.Is(err, ErrReported) {
		t.Fatalf("failed add should return ErrReported, got %v", err)
	}
	if out := a2.Err.(*bytes.Buffer).String(); strings.Contains(out, "--token") {
		t.Errorf("non-permission failure should not include the --token hint: %q", out)
	}
}
