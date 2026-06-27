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
	Fetch(Source) (string, []FetchedFile, error)
}

// App holds the wiring for a command invocation.
type App struct {
	Paths Paths
	F     fetcher
	Out   io.Writer // data (stdout)
	Err   io.Writer // progress / messages (stderr)
}

// New returns an App with default paths and the real API fetcher.
func New(out, errw io.Writer) *App {
	return &App{Paths: DefaultPaths(), F: Fetcher{}, Out: out, Err: errw}
}

func (a *App) msg(format string, args ...any) {
	fmt.Fprintf(a.Err, format+"\n", args...)
}

// Add upserts a source into the local config file and then pulls.
func (a *App) Add(s Source) error {
	if err := a.Paths.AddSource(s); err != nil {
		return err
	}
	a.msg("Added %s [%s]", s.Spec(), s.ID())
	if EnvSet() {
		a.msg("note: %s is set and overrides the config file; this entry takes effect once that variable is unset.", EnvSources)
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
		a.msg("No sources configured. Add one with: gh copilot-instructions add <owner/repo[:path]>")
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
			a.msg("error: %s: %v", s.Repo, err)
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
		a.msg("  %s  up to date (%s)", s.Repo, short(sha))
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
		a.msg("  %s  warning: no files matched %q", s.Repo, s.effectivePath())
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
	a.msg("  %s  pulled %d file(s) (%s)", s.Repo, len(installed), short(sha))
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
		a.msg("No source matched %q.", idOrRepo)
		return nil
	}
	a.msg("Removed %q.", idOrRepo)
	if EnvSet() && len(removedFromFile) > 0 {
		a.msg("note: %s is set and overrides the config file.", EnvSources)
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
	a.msg("Removed all configured sources and every installed file.")
	a.msg("To remove the command itself: gh extension remove gh-copilot-instructions")
	return nil
}

// Row is one line of `list` output.
type Row struct {
	ID       string
	Repo     string
	Ref      string
	SHA      string
	PulledAt time.Time
	HasToken bool
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
	envToken := strings.TrimSpace(os.Getenv(EnvToken)) != ""
	var rows []Row
	for _, s := range srcs {
		r := Row{
			ID:       s.ID(),
			Repo:     s.Repo,
			Ref:      s.Ref,
			HasToken: s.Token != "" || envToken,
		}
		if ss, ok := st.Sources[s.ID()]; ok {
			r.SHA = ss.SHA
			r.PulledAt = ss.PulledAt
			r.Files = len(ss.Files)
		}
		rows = append(rows, r)
	}
	return rows, origin, nil
}

func (a *App) printCovered() {
	a.msg("")
	a.msg("Instructions installed to %s", a.Paths.InstallDir)
	a.msg("Now applied automatically in: Copilot CLI, VS Code (local/Remote/Codespaces), the GitHub Copilot desktop app.")
	a.msg("Reload VS Code / restart the desktop app to pick up changes.")
}

func isOurs(name string) bool {
	return strings.HasPrefix(name, FilePrefix+".") && strings.HasSuffix(name, ".instructions.md")
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
