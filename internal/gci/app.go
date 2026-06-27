package gci

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// fetcher abstracts content fetching so tests can inject a fake.
type fetcher interface {
	ResolveSHA(Source) (string, error)
	Fetch(Source) (sha string, files []FetchedFile, err error)
}

// App holds the wiring for a command invocation.
type App struct {
	Paths Paths
	F     fetcher
	Out   io.Writer    // data (stdout)
	Err   io.Writer    // progress / messages (stderr)
	CS    *ColorScheme // color scheme for Err (stderr) messages
}

// New returns an App with default paths and the real API fetcher.
func New(out, errw io.Writer) *App {
	_, errCS := newSchemes()
	return &App{Paths: DefaultPaths(), F: Fetcher{}, Out: out, Err: errw, CS: errCS}
}

func (a *App) cs() *ColorScheme {
	if a.CS == nil {
		return &ColorScheme{} // disabled (e.g. in tests)
	}
	return a.CS
}

func (a *App) msg(format string, args ...any) {
	fmt.Fprintf(a.Err, format+"\n", args...)
}

// success prints a green-check status line.
func (a *App) success(format string, args ...any) {
	a.msg("%s %s", a.cs().SuccessIcon(), fmt.Sprintf(format, args...))
}

// warn prints a yellow-bang status line.
func (a *App) warn(format string, args ...any) {
	a.msg("%s %s", a.cs().WarningIcon(), fmt.Sprintf(format, args...))
}

// fail prints a red-cross status line.
func (a *App) fail(format string, args ...any) {
	a.msg("%s %s", a.cs().FailureIcon(), fmt.Sprintf(format, args...))
}

// dim prints muted secondary text.
func (a *App) dim(format string, args ...any) {
	a.msg("%s", a.cs().Gray(fmt.Sprintf(format, args...)))
}

// Add upserts a source into the local config file and then pulls.
func (a *App) Add(s Source) error {
	if err := a.Paths.AddSource(s); err != nil {
		return err
	}
	a.success("Added %s %s", a.cs().Bold(s.Spec()), a.cs().Gray("("+s.ID()+")"))
	if EnvSet() {
		a.warn("%s is set and overrides the config file; this entry applies once that variable is unset.", EnvSources)
	}
	return a.Pull("")
}

// Pull pulls all configured sources, or just one when filter (id or owner/repo)
// is non-empty.
func (a *App) Pull(filter string) error {
	srcs, origin, err := a.Paths.LoadSources()
	if err != nil {
		a.msg("%v", err) // report malformed lines but continue with the rest
	}
	if origin == OriginNone || len(srcs) == 0 {
		a.dim("No sources configured. Add one with: gh copilot-instructions add <owner/repo[:path]>")
		return nil
	}
	st, err := a.Paths.LoadState()
	if err != nil {
		return err
	}
	matched := false
	var firstErr error
	for _, s := range srcs {
		if filter != "" && s.ID() != filter && s.Repo != filter {
			continue
		}
		matched = true
		if err := a.pullOne(s, st); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			a.fail("%s: %v", s.Repo, err)
		}
	}
	if filter != "" && !matched {
		return fmt.Errorf("no configured source matches %q", filter)
	}
	if err := a.Paths.Save(st); err != nil {
		return err
	}
	a.printCovered()
	return firstErr
}

func (a *App) pullOne(s Source, st *State) error {
	id := s.ID()
	sha, err := a.F.ResolveSHA(s)
	if err != nil {
		return err
	}
	if prev, ok := st.Sources[id]; ok && prev.SHA == sha && a.allFilesExist(prev.Files) {
		a.dim("  %s  up to date (%s)", s.Repo, short(sha))
		return nil
	}
	gotSHA, files, err := a.F.Fetch(s)
	if err != nil {
		return err
	}
	if gotSHA != "" {
		sha = gotSHA
	}
	if len(files) == 0 {
		a.warn("%s  no files matched %s", a.cs().Bold(s.Repo), a.cs().Gray(s.effectivePath()))
	}
	var installed []string
	for _, f := range files {
		name := s.DestFile(f.Rel)
		if err := a.writeInstall(name, f.Content); err != nil {
			return err
		}
		installed = append(installed, name)
	}
	// Prune this source's files that are no longer produced.
	if prev, ok := st.Sources[id]; ok {
		a.prune(prev.Files, installed)
	}
	sort.Strings(installed)
	st.Sources[id] = SourceState{
		Repo:     s.Repo,
		Ref:      s.Ref,
		Path:     s.Path,
		SHA:      sha,
		PulledAt: time.Now().UTC(),
		Files:    installed,
	}
	a.success("%s  %s (%s)", a.cs().Bold(s.Repo), pluralFiles(len(installed)), a.cs().Gray(short(sha)))
	return nil
}

func (a *App) writeInstall(name string, content []byte) error {
	if err := os.MkdirAll(a.Paths.InstallDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(a.Paths.InstallDir, name), content, 0o644)
}

func (a *App) allFilesExist(files []string) bool {
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(a.Paths.InstallDir, f)); err != nil {
			return false
		}
	}
	return len(files) > 0
}

// prune removes files in old that are not in keep (only our own files).
func (a *App) prune(old, keep []string) {
	keepSet := map[string]bool{}
	for _, k := range keep {
		keepSet[k] = true
	}
	for _, f := range old {
		if keepSet[f] || !isOurs(f) {
			continue
		}
		os.Remove(filepath.Join(a.Paths.InstallDir, f))
	}
}

// Remove deletes sources matching idOrRepo from the config file and prunes their
// installed files (by id), regardless of whether the config came from file/env.
func (a *App) Remove(idOrRepo string) error {
	removedFromFile, err := a.Paths.RemoveSource(idOrRepo)
	if err != nil {
		return err
	}
	st, err := a.Paths.LoadState()
	if err != nil {
		return err
	}
	var removedIDs []string
	for id, ss := range st.Sources {
		if id == idOrRepo || ss.Repo == idOrRepo {
			a.prune(ss.Files, nil)
			delete(st.Sources, id)
			removedIDs = append(removedIDs, id)
		}
	}
	if err := a.Paths.Save(st); err != nil {
		return err
	}
	if len(removedFromFile) == 0 && len(removedIDs) == 0 {
		a.dim("No source matched %q.", idOrRepo)
		return nil
	}
	a.success("Removed %s", a.cs().Bold(idOrRepo))
	if EnvSet() && len(removedFromFile) > 0 {
		a.warn("%s is set and overrides the config file.", EnvSources)
	}
	return nil
}

// RemoveAll clears all configured sources and removes every file we installed.
func (a *App) RemoveAll() error {
	st, err := a.Paths.LoadState()
	if err != nil {
		return err
	}
	for _, ss := range st.Sources {
		a.prune(ss.Files, nil)
	}
	// Belt and suspenders: remove any stray prefix-owned files too.
	if entries, err := os.ReadDir(a.Paths.InstallDir); err == nil {
		for _, e := range entries {
			if isOurs(e.Name()) {
				os.Remove(filepath.Join(a.Paths.InstallDir, e.Name()))
			}
		}
	}
	if err := a.Paths.ClearSources(); err != nil {
		return err
	}
	if err := a.Paths.Save(&State{Sources: map[string]SourceState{}}); err != nil {
		return err
	}
	a.success("Removed all configured sources and every installed file.")
	a.dim("To remove the command itself: gh extension remove gh-copilot-instructions")
	return nil
}

// Source states, as shown in the list output (uppercase, matching GitHub's
// state-string convention).
const (
	StatePulled  = "PULLED"  // recorded and every installed file is present
	StatePending = "PENDING" // configured but not yet pulled
	StateFailed  = "FAILED"  // pulled but the install is broken or matched no files
)

// Row is one line of `list` output.
type Row struct {
	State    string // StatePulled, StatePending, or StateFailed
	ID       string
	Repo     string
	Ref      string // the branch/tag/SHA shown in the REF column ("" => default branch)
	SHA      string
	PulledAt time.Time
	Files    int
}

// ListRows returns the configured sources joined with their pulled state.
func (a *App) ListRows() ([]Row, ConfigOrigin, error) {
	srcs, origin, err := a.Paths.LoadSources()
	if err != nil {
		a.msg("%v", err)
	}
	st, sErr := a.Paths.LoadState()
	if sErr != nil {
		return nil, origin, sErr
	}
	var rows []Row
	for _, s := range srcs {
		r := Row{
			State: StatePending,
			ID:    s.ID(),
			Repo:  s.Repo,
			Ref:   s.Ref,
		}
		if ss, ok := st.Sources[s.ID()]; ok {
			r.SHA = ss.SHA
			r.PulledAt = ss.PulledAt
			r.Files = len(ss.Files)
			// pulled = state recorded and every installed file is present;
			// otherwise the install is broken or matched nothing => failed.
			if a.allFilesExist(ss.Files) {
				r.State = StatePulled
			} else {
				r.State = StateFailed
			}
		}
		rows = append(rows, r)
	}
	return rows, origin, nil
}

func (a *App) printCovered() {
	cs := a.cs()
	a.msg("")
	a.msg("%s instructions installed to %s", cs.SuccessIcon(), cs.Bold(a.Paths.InstallDir))
	a.dim("Applied automatically in Copilot CLI, VS Code (local/Remote/Codespaces), and the GitHub Copilot desktop app.")
	a.dim("Reload VS Code / restart the desktop app to pick up changes.")
}

func pluralFiles(n int) string {
	if n == 1 {
		return "pulled 1 file"
	}
	return fmt.Sprintf("pulled %d files", n)
}

func isOurs(name string) bool {
	return strings.HasPrefix(name, FilePrefix+".") && strings.HasSuffix(name, ".instructions.md")
}

// short abbreviates a commit SHA for display. gh's convention is 8 characters
// (git/client.go ShortSHA), which is what this extension uses to match gh.
func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
