package gci

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cli/go-gh/v2/pkg/term"
)

// fetcher abstracts content fetching so tests can inject a fake.
//
// Fetch reports incremental progress: onProgress, when non-nil, is called first
// with the resolved commit SHA (files=0) as soon as it's known, then again after
// each matched blob with the running file count. This lets callers fill in the
// SHA cell early and animate a live "files" counter during the (slow) fetch.
type fetcher interface {
	ResolveSHA(Source) (string, error)
	Fetch(s Source, onProgress func(sha string, files int)) (sha string, files []FetchedFile, err error)
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

// Add upserts a source into the local config file, then pulls just that source.
// On a terminal it shows the full instructions table with the new (last) row
// animating in place — gh's spinner in the state cell — and settles that row to
// its final state when the pull completes. Off a terminal it prints plain
// progress lines.
func (a *App) Add(s Source) error {
	if err := a.Paths.AddSource(s); err != nil {
		return err
	}
	if EnvSet() {
		a.warn("%s is set and overrides the config file; this entry applies once that variable is unset.", EnvSources)
	}
	st, err := a.Paths.LoadState()
	if err != nil {
		return err
	}

	t := term.FromEnv()
	if !t.IsTerminalOutput() {
		_, perr := a.pullOne(s, st, false, nil)
		if e := a.Paths.Save(st); e != nil {
			return e
		}
		if perr != nil {
			a.fail("%s: %v", s.Repo, perr)
			return perr
		}
		a.printCovered()
		return nil
	}

	rows, _, lerr := a.ListRows()
	if lerr != nil {
		return lerr
	}
	w, _, _ := t.Size()
	if w <= 0 {
		w = 80
	}
	cs := &ColorScheme{enabled: t.IsColorEnabled()}

	_, perr := a.addAnimated(s, st, rows, rowIndex(rows, s.ID()), cs, w)
	if e := a.Paths.Save(st); e != nil {
		return e
	}
	if perr != nil {
		a.fail("%s: %v", s.Repo, perr)
		return perr
	}
	a.printCovered()
	return nil
}

// spinnerFrames is gh's exact progress spinner: briandowns CharSets[11], the set
// gh's iostreams uses (see StartProgressIndicatorWithLabel). Advanced at gh's
// 120ms cadence and rendered in cyan, matching gh's spinner.WithColor("fgCyan").
var spinnerFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

// addAnimated renders the table once (rows[idx] is the in-progress new row),
// pulls that source in the background, and animates that row in place: a spinner
// in the state cell, a FILES counter that climbs as blobs download, the SHA
// filled in the moment it's resolved (before the blobs), and an elapsed-seconds
// timer ("0s", "1s", …) in PULLED that flips to the relative time when done.
// Each frame re-renders the table to a line buffer and repaints only what
// changed: normally just the new row's line, and the whole table only when a
// column actually reflows (e.g. the SHA first appears on a first-ever add).
// Returns the final row and the pull error (if any).
func (a *App) addAnimated(s Source, st *State, rows []Row, idx int, cs *ColorScheme, width int) (Row, error) {
	out := a.Out

	// Live values reported by the pull goroutine, read by the ticker.
	var sha atomic.Value // string
	sha.Store("")
	var filesCount atomic.Int64
	onProgress := func(s string, files int) {
		if s != "" {
			sha.Store(s)
		}
		filesCount.Store(int64(files))
	}

	start := time.Now()
	elapsed := func() string { return fmt.Sprintf("%ds", int(time.Since(start).Seconds())) }

	shown := a.tableLines(rows, width, cs, &rowAnim{idx, spinnerFrames[0], elapsed()})
	fmt.Fprint(out, strings.Join(shown, "\n"), "\n")
	fmt.Fprint(out, "\x1b[?25l")       // hide cursor
	defer fmt.Fprint(out, "\x1b[?25h") // restore cursor

	// paint diffs next against what's on screen: if only the new row's (last)
	// line changed, rewrite that one line; if a column reflowed, redraw the
	// whole table once. Either way the cursor ends back at home (below the table).
	paint := func(next []string) {
		if linesEqualExceptLast(shown, next) {
			fmt.Fprintf(out, "\x1b[1A\r\x1b[K%s\r\x1b[1B", next[len(next)-1])
		} else {
			fmt.Fprintf(out, "\x1b[%dA\x1b[J%s\n", len(shown), strings.Join(next, "\n"))
		}
		shown = next
	}

	type result struct {
		row Row
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, e := a.pullOne(s, st, true, onProgress)
		done <- result{r, e}
	}()

	ticker := time.NewTicker(120 * time.Millisecond) // gh's spinner cadence
	defer ticker.Stop()
	tick := 0
	for {
		select {
		case res := <-done:
			final := res.row
			if res.err != nil {
				final = a.rowFor(s, st)
				final.State = StateFailed
			}
			rows[idx] = final
			paint(a.tableLines(rows, width, cs, nil))
			return final, res.err
		case <-ticker.C:
			tick++
			rows[idx].SHA = sha.Load().(string)
			rows[idx].Files = int(filesCount.Load())
			anim := &rowAnim{
				idx:        idx,
				stateCell:  spinnerFrames[tick%len(spinnerFrames)],
				pulledCell: elapsed(),
			}
			paint(a.tableLines(rows, width, cs, anim))
		}
	}
}

// tableLines renders the table to a slice of lines (one per terminal row).
func (a *App) tableLines(rows []Row, width int, cs *ColorScheme, anim *rowAnim) []string {
	var buf bytes.Buffer
	_ = a.renderTable(&buf, rows, true, width, cs, anim)
	return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
}

// linesEqualExceptLast reports whether a and b match on every line but the last
// (i.e. only the final row's content differs — no column reflow).
func linesEqualExceptLast(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a)-1; i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// rowIndex returns the index of the row with the given id, or the last row.
func rowIndex(rows []Row, id string) int {
	for i, r := range rows {
		if r.ID == id {
			return i
		}
	}
	return len(rows) - 1
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
		if _, err := a.pullOne(s, st, false, nil); err != nil {
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

// pullOne pulls a single source into install state, updating st. When quiet is
// true it prints nothing (the animated `add` owns the output); otherwise it
// prints gh-style progress lines. onProgress, when non-nil, is called with the
// resolved SHA and running file count as blobs download, so `add` can fill in
// the SHA early and animate a live counter. It returns the resulting Row.
func (a *App) pullOne(s Source, st *State, quiet bool, onProgress func(sha string, files int)) (Row, error) {
	id := s.ID()
	prev, hasPrev := st.Sources[id]
	healthy := hasPrev && a.allFilesExist(prev.Files)

	if healthy {
		// Skip without any network call when the configured ref is an immutable
		// commit-ish (≥7 hex digits) that is a left-pinned prefix of the SHA we
		// already pulled — it can only point at that same commit.
		if refPinsTo(s.Ref, prev.SHA) {
			if !quiet {
				a.dim("  %s  up to date (%s)", s.Repo, short(prev.SHA))
			}
			return a.rowFor(s, st), nil
		}
		// Otherwise resolve the current tip (one API call) and compare.
		sha, err := a.F.ResolveSHA(s)
		if err != nil {
			return Row{}, err
		}
		if prev.SHA == sha {
			if !quiet {
				a.dim("  %s  up to date (%s)", s.Repo, short(sha))
			}
			return a.rowFor(s, st), nil
		}
	}

	sha, files, err := a.F.Fetch(s, onProgress)
	if err != nil {
		return Row{}, err
	}
	if len(files) == 0 && !quiet {
		a.warn("%s  no files matched %s", a.cs().Bold(s.Repo), a.cs().Gray(s.effectivePath()))
	}
	// Order files so that, when two normalize to the same install name, the
	// choice of which to keep is deterministic and explainable: prefer the file
	// that already ends in ".instructions.md", then ".md", then anything else;
	// ties break on the lexicographically lowest repo path. Keeping the first
	// occurrence per install name then implements that policy.
	sort.Slice(files, func(i, j int) bool {
		pi, pj := namePriority(files[i].Rel), namePriority(files[j].Rel)
		if pi != pj {
			return pi < pj
		}
		return files[i].Rel < files[j].Rel
	})
	var installed []string
	seen := map[string]string{} // dest path -> repo path that produced it
	for _, f := range files {
		rel := s.DestPath(f.Rel)
		if rel == "" {
			if !quiet {
				a.warn("%s  skipped unsafe path %s", a.cs().Bold(s.Repo), a.cs().Gray(f.Rel))
			}
			continue
		}
		if first, dup := seen[rel]; dup {
			if !quiet {
				a.warn("%s  %s and %s both map to %s; keeping %s",
					a.cs().Bold(s.Repo), a.cs().Gray(first), a.cs().Gray(f.Rel),
					a.cs().Gray(path.Base(rel)), a.cs().Gray(first))
			}
			continue
		}
		seen[rel] = f.Rel
		if err := a.writeInstall(rel, f.Content); err != nil {
			return Row{}, err
		}
		installed = append(installed, rel)
	}
	// Prune this source's files that are no longer produced.
	if hasPrev {
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
	if !quiet {
		a.success("%s  %s (%s)", a.cs().Bold(s.Repo), pluralFiles(len(installed)), a.cs().Gray(short(sha)))
	}
	return a.rowFor(s, st), nil
}

// writeInstall writes one file at a forward-slash dest path relative to the
// install dir, creating parent directories as needed (the nested layout).
func (a *App) writeInstall(rel string, content []byte) error {
	dest := filepath.Join(a.Paths.InstallDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, content, 0o644)
}

func (a *App) allFilesExist(files []string) bool {
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(a.Paths.InstallDir, filepath.FromSlash(f))); err != nil {
			return false
		}
	}
	return len(files) > 0
}

// prune removes files in old that are not in keep (only our own files), then
// tidies up any directories left empty under the install dir.
func (a *App) prune(old, keep []string) {
	keepSet := map[string]bool{}
	for _, k := range keep {
		keepSet[k] = true
	}
	for _, f := range old {
		if keepSet[f] || !isOurs(f) {
			continue
		}
		os.Remove(filepath.Join(a.Paths.InstallDir, filepath.FromSlash(f)))
		a.removeEmptyParents(f)
	}
}

// removeEmptyParents removes now-empty directories from a file's parent upward,
// stopping at the install dir (never removing it or anything outside it).
func (a *App) removeEmptyParents(rel string) {
	base := filepath.Clean(a.Paths.InstallDir)
	dir := filepath.Dir(filepath.Join(base, filepath.FromSlash(rel)))
	for dir != base && strings.HasPrefix(dir, base+string(filepath.Separator)) {
		if err := os.Remove(dir); err != nil {
			return // non-empty or error: stop walking up
		}
		dir = filepath.Dir(dir)
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
			os.RemoveAll(filepath.Join(a.Paths.InstallDir, FileDir, id))
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
	// Remove the entire namespace directory (nested layout).
	os.RemoveAll(filepath.Join(a.Paths.InstallDir, FileDir))
	// Belt and suspenders: remove any stray files we own, including legacy
	// flat-layout files from older versions.
	if entries, err := os.ReadDir(a.Paths.InstallDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && isOurs(e.Name()) {
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

// rowFor builds a Row for a source from the current on-disk state, computing the
// state the same way `list` does: PENDING (no record), PULLED (record + all files
// present), or FAILED (record but a file is missing or none matched).
func (a *App) rowFor(s Source, st *State) Row {
	r := Row{State: StatePending, ID: s.ID(), Repo: s.Repo, Ref: s.Ref}
	if ss, ok := st.Sources[s.ID()]; ok {
		r.SHA = ss.SHA
		r.PulledAt = ss.PulledAt
		r.Files = len(ss.Files)
		if a.allFilesExist(ss.Files) {
			r.State = StatePulled
		} else {
			r.State = StateFailed
		}
	}
	return r
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
		rows = append(rows, a.rowFor(s, st))
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

// isOurs reports whether a path (relative to the install dir) is one we manage,
// so prune/remove never touch the user's own hand-written instruction files. It
// recognizes both the nested layout ("gh-copilot-instructions/<id>/...") and
// legacy flat files ("gh-copilot-instructions.<id>.<name>.instructions.md").
func isOurs(name string) bool {
	name = filepath.ToSlash(name)
	nested := strings.HasPrefix(name, FileDir+"/")
	legacy := strings.HasPrefix(name, FileDir+".")
	return (nested || legacy) && strings.HasSuffix(name, ".instructions.md")
}

// short abbreviates a commit SHA for display. gh's convention is 8 characters
// (git/client.go ShortSHA), which is what this extension uses to match gh.
func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
