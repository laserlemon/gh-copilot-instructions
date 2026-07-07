package gci

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProbe is a network-free accountProbe for doctor tests.
type fakeProbe struct {
	login    string
	err      error
	rem, lim int
}

func (p fakeProbe) Whoami(string) (string, error)      { return p.login, p.err }
func (p fakeProbe) RateLimit(string) (int, int, error) { return p.rem, p.lim, nil }

// testSched is a scheduler stub so doctor's auto-pull check is deterministic
// (unsupported => the check is skipped) regardless of the host machine.
type testSched struct{}

func (testSched) Enable(Cadence) error     { return nil }
func (testSched) Disable() error           { return nil }
func (testSched) Installed() (bool, error) { return false, nil }
func (testSched) Supported() bool          { return false }
func (testSched) Kind() string             { return "test" }

// unreachableFetcher fails every resolve, so every source looks unreachable.
type unreachableFetcher struct{}

func (unreachableFetcher) ResolveSHA(Source) (string, error) { return "", errors.New("not found") }
func (unreachableFetcher) Fetch(Source, func(string, int)) (string, []FetchedFile, error) {
	return "", nil, errors.New("not found")
}

// newDoctorApp builds a sandboxed App with a known token and stubbed probe and
// scheduler, so the checks never touch the network or the host's launchd.
func newDoctorApp(t *testing.T, f fetcher) *App {
	t.Helper()
	a := newTestApp(t, f)
	t.Setenv("GH_TOKEN", "x-test-token")
	a.Probe = fakeProbe{login: "octocat", rem: 5000, lim: 5000}
	a.Sched = testSched{}
	return a
}

func findCheck(results []checkResult, substr string) (checkResult, bool) {
	for _, r := range results {
		if strings.Contains(r.Check, substr) {
			return r, true
		}
	}
	return checkResult{}, false
}

func mustCheck(t *testing.T, results []checkResult, substr, want string) {
	t.Helper()
	r, ok := findCheck(results, substr)
	if !ok {
		t.Fatalf("no check matching %q; got %+v", substr, results)
	}
	if r.Status != want {
		t.Fatalf("check %q: status=%s, want %s", substr, r.Status, want)
	}
}

// installOne adds source o/r and pulls a single file, returning the App.
func installOne(t *testing.T, a *App) Source {
	t.Helper()
	src, _ := ParseSpec("o/r")
	if ff, ok := a.F.(*fakeFetcher); ok {
		ff.sha = map[string]string{src.ID(): "sha1111111111111111111111111111111111111"}
		ff.files = map[string][]FetchedFile{src.ID(): {{Rel: "a.instructions.md", Content: []byte("---\napplyTo: '**'\n---\nA")}}}
	}
	if err := a.Paths.AddSource(src); err != nil {
		t.Fatal(err)
	}
	if err := a.Pull("", false); err != nil {
		t.Fatal(err)
	}
	return src
}

func TestDoctorHealthy(t *testing.T) {
	a := newDoctorApp(t, &fakeFetcher{})
	installOne(t, a)

	res := a.diagnose()
	mustCheck(t, res, "Authenticated to GitHub as octocat", statusOK)
	mustCheck(t, res, "rate limit", statusOK)
	mustCheck(t, res, "source configured", statusOK)
	mustCheck(t, res, "permissions are correct", statusOK)
	mustCheck(t, res, "reachable on GitHub", statusOK)
	mustCheck(t, res, "Install directory is present", statusOK)
	mustCheck(t, res, "installed file", statusOK)

	if _, _, fail := tally(res); fail != 0 {
		t.Fatalf("healthy setup reported failures: %+v", res)
	}
}

func TestDoctorNoSources(t *testing.T) {
	a := newDoctorApp(t, &fakeFetcher{})
	res := a.diagnose()
	mustCheck(t, res, "No sources are configured", statusWarn)
}

func TestDoctorMissingFiles(t *testing.T) {
	a := newDoctorApp(t, &fakeFetcher{})
	installOne(t, a)
	// Remove the installed subtree but keep state -> files look missing.
	if err := os.RemoveAll(filepath.Join(a.Paths.InstallDir, FileDir)); err != nil {
		t.Fatal(err)
	}
	res := a.diagnose()
	mustCheck(t, res, "installed files are missing", statusFail)
	if _, _, fail := tally(res); fail == 0 {
		t.Fatal("missing files should fail the run")
	}
}

func TestDoctorOrphanFiles(t *testing.T) {
	a := newDoctorApp(t, &fakeFetcher{})
	installOne(t, a)
	orphan := filepath.Join(a.Paths.InstallDir, FileDir, "orphan.instructions.md")
	if err := os.WriteFile(orphan, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := a.diagnose()
	mustCheck(t, res, "not owned by any source", statusWarn)
}

func TestDoctorUnreachable(t *testing.T) {
	a := newDoctorApp(t, unreachableFetcher{})
	src, _ := ParseSpec("o/r")
	if err := a.Paths.AddSource(src); err != nil {
		t.Fatal(err)
	}
	res := a.diagnose()
	mustCheck(t, res, "unreachable", statusFail)
}

func TestDoctorAnonymous(t *testing.T) {
	a := newTestApp(t, &fakeFetcher{})
	a.Sched = testSched{}
	a.Probe = fakeProbe{rem: 60, lim: 60}
	// No token anywhere -> anonymous.
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	res := a.diagnose()
	mustCheck(t, res, "Not authenticated", statusWarn)
}

func TestDoctorUpdatesAvailable(t *testing.T) {
	a := newDoctorApp(t, &fakeFetcher{})
	src := installOne(t, a)
	// Advance the remote commit; the recorded state SHA now lags.
	a.F.(*fakeFetcher).sha[src.ID()] = "sha2222222222222222222222222222222222222"
	res := a.diagnose()
	mustCheck(t, res, "updates available", statusWarn)
}

func TestDoctorExitNonZeroOnFail(t *testing.T) {
	a := newDoctorApp(t, unreachableFetcher{})
	src, _ := ParseSpec("o/r")
	if err := a.Paths.AddSource(src); err != nil {
		t.Fatal(err)
	}
	if err := a.Doctor(true); !errors.Is(err, ErrReported) {
		t.Fatalf("Doctor should return ErrReported when a check fails, got %v", err)
	}
}

func TestDoctorFrontmatter(t *testing.T) {
	src, _ := ParseSpec("o/r")
	f := &fakeFetcher{
		sha:   map[string]string{src.ID(): "sha1111111111111111111111111111111111111"},
		files: map[string][]FetchedFile{src.ID(): {{Rel: "notes.instructions.md", Content: []byte("no frontmatter here")}}},
	}
	a := newDoctorApp(t, f)
	if err := a.Paths.AddSource(src); err != nil {
		t.Fatal(err)
	}
	if err := a.Pull("", false); err != nil {
		t.Fatal(err)
	}
	res := a.diagnose()
	mustCheck(t, res, "no applyTo frontmatter", statusWarn)
}

func TestDoctorInlineToken(t *testing.T) {
	a := newDoctorApp(t, &fakeFetcher{})
	if err := a.Paths.AddSource(Source{Repo: "o/r", Token: "ghp_secret"}); err != nil {
		t.Fatal(err)
	}
	res := a.diagnose()
	mustCheck(t, res, "inline token stored", statusWarn)
}

func TestPlur(t *testing.T) {
	if got := plur(1, "source", "sources"); got != "1 source" {
		t.Fatalf("plur(1) = %q", got)
	}
	if got := plur(3, "source", "sources"); got != "3 sources" {
		t.Fatalf("plur(3) = %q", got)
	}
}

func TestTreesEqual(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	write := func(root, rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(src, "a/x.md", "hello")
	write(dst, "a/x.md", "hello")
	if !treesEqual(src, dst) {
		t.Fatal("identical trees should be equal")
	}
	write(dst, "a/x.md", "changed")
	if treesEqual(src, dst) {
		t.Fatal("differing contents should not be equal")
	}
	write(dst, "a/x.md", "hello")
	write(dst, "b/y.md", "extra")
	if treesEqual(src, dst) {
		t.Fatal("extra file should make trees unequal")
	}
}
