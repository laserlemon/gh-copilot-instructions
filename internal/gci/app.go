package gci

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cli/go-gh/v2/pkg/jsonpretty"
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
	Sched scheduler    // OS scheduler for auto-pull (nil => resolved per-platform)
	Out   io.Writer    // data (stdout)
	Err   io.Writer    // progress / messages (stderr)
	CS    *ColorScheme // color scheme for Err (stderr) messages

	// outTTY/outColor describe stdout, so --json output is pretty-printed and
	// syntax-highlighted on a terminal and stays compact when piped (matching
	// gh). They are detected once in New and default false (compact) in tests.
	outTTY   bool
	outColor bool
}

// New returns an App with default paths and the real API fetcher.
func New(out, errw io.Writer) *App {
	_, errCS := newSchemes()
	t := term.FromEnv()
	return &App{
		Paths:    DefaultPaths(),
		F:        Fetcher{},
		Sched:    newScheduler(DefaultPaths()),
		Out:      out,
		Err:      errw,
		CS:       errCS,
		outTTY:   t.IsTerminalOutput(),
		outColor: t.IsColorEnabled(),
	}
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

// blank prints an empty line. It separates primary output (the result) from the
// secondary block (supporting detail: warnings, hints, file locations).
func (a *App) blank() { a.msg("") }

// note prints a secondary warning line: a yellow "!" status icon (kept colored,
// because it communicates status) followed by muted gray supporting text. Use it
// for warnings that belong in the secondary block; use warn for a warning that
// is itself the primary result.
func (a *App) note(format string, args ...any) {
	a.msg("%s %s", a.cs().WarningIcon(), a.cs().Gray(fmt.Sprintf(format, args...)))
}

// Add upserts a source into the local config file, then pulls just that source.
// On a terminal it renders the full instructions table with that row animating
// (spinner + live SHA/FILES + elapsed) while every other row is dimmed, settling
// the row to its final state when the pull completes. Off a terminal it prints
// plain progress lines. With asJSON it pulls quietly and emits a one-element JSON
// array with the added source's result.
func (a *App) Add(s Source, asJSON bool) error {
	if err := a.Paths.AddSource(s); err != nil {
		return err
	}
	if EnvSet() && !asJSON {
		a.warn("%s is set and overrides the config file. This entry applies once that variable is unset.", EnvSources)
	}
	st, err := a.Paths.LoadState()
	if err != nil {
		return err
	}

	srcs, _, lerr := a.Paths.LoadSources()
	if lerr != nil && !asJSON {
		a.msg("%v", lerr)
	}
	target := indexOfID(srcs, s.ID())

	if asJSON {
		prev, has := st.Sources[s.ID()]
		out := a.pullSource(s, prev, has, nil)
		st.Sources[s.ID()] = out.newState // record every attempt (FAILED included)
		if e := a.Paths.Save(st); e != nil {
			return e
		}
		upd := map[string]bool{}
		if out.updated {
			upd[s.ID()] = true
		}
		if err := a.renderSourceListJSON(upd); err != nil {
			return err
		}
		return out.err
	}

	t := term.FromEnv()
	if !t.IsTerminalOutput() || target < 0 {
		prev, has := st.Sources[s.ID()]
		out := a.pullSource(s, prev, has, nil)
		a.printOutcome(s, out)
		st.Sources[s.ID()] = out.newState // record every attempt (FAILED included)
		if e := a.Paths.Save(st); e != nil {
			return e
		}
		if out.err != nil {
			return out.err
		}
		a.printPullFooter(out.warnings, s.ID())
		return nil
	}

	rows := a.rowsFor(srcs, st)
	cs, w := a.tableEnv(t)
	warnings, err := a.animate(rows, srcs, []int{target}, true, st, cs, w)
	if e := a.Paths.Save(st); e != nil {
		return e
	}
	if err != nil {
		a.fail("%s: %v", s.Repo, err)
		return err
	}
	a.printPullFooter(warnings, s.ID())
	return nil
}

// Pull pulls all configured sources, or just those matching filter (id or
// owner/repo). On a terminal every targeted row animates in the full-color
// table; off a terminal it prints plain per-source progress lines. With asJSON
// it instead pulls quietly and emits a JSON array of per-source results.
func (a *App) Pull(filter string, asJSON bool) error {
	srcs, origin, err := a.Paths.LoadSources()
	if err != nil {
		a.msg("%v", err) // report malformed lines but continue with the rest
	}
	if origin == OriginNone || len(srcs) == 0 {
		if asJSON {
			return a.writeJSON([]sourceJSON{})
		}
		a.dim("No Copilot instructions sources added.")
		a.blank()
		a.dim("Add a source: gh copilot-instructions add <owner/repo>")
		return nil
	}
	st, err := a.Paths.LoadState()
	if err != nil {
		return err
	}

	var targets []int
	for i, s := range srcs {
		if filter == "" || s.ID() == filter || s.Repo == filter {
			targets = append(targets, i)
		}
	}
	if filter != "" && len(targets) == 0 {
		return fmt.Errorf("no configured source matches %q", filter)
	}

	if asJSON {
		upd := map[string]bool{}
		var firstErr error
		for _, i := range targets {
			s := srcs[i]
			prev, has := st.Sources[s.ID()]
			out := a.pullSource(s, prev, has, nil)
			st.Sources[s.ID()] = out.newState // record every attempt (FAILED included)
			if out.updated {
				upd[s.ID()] = true
			}
			if out.err != nil && firstErr == nil {
				firstErr = out.err
			}
		}
		if err := a.Paths.Save(st); err != nil {
			return err
		}
		if err := a.renderSourceListJSON(upd); err != nil {
			return err
		}
		return firstErr
	}

	t := term.FromEnv()
	if !t.IsTerminalOutput() {
		var firstErr error
		var warnings []string
		for _, i := range targets {
			s := srcs[i]
			prev, has := st.Sources[s.ID()]
			out := a.pullSource(s, prev, has, nil)
			a.printOutcome(s, out)
			warnings = append(warnings, out.warnings...)
			st.Sources[s.ID()] = out.newState // record every attempt (FAILED included)
			if out.err != nil && firstErr == nil {
				firstErr = out.err
			}
		}
		if err := a.Paths.Save(st); err != nil {
			return err
		}
		a.printPullFooter(warnings, "")
		return firstErr
	}

	rows := a.rowsFor(srcs, st)
	cs, w := a.tableEnv(t)
	// A filtered pull focuses the matched rows (and dims the rest), like add; an
	// unfiltered pull animates every row with no dimming.
	warnings, err := a.animate(rows, srcs, targets, filter != "", st, cs, w)
	if e := a.Paths.Save(st); e != nil {
		return e
	}
	if err != nil {
		return err
	}
	a.printPullFooter(warnings, "")
	return err
}

// writeJSON marshals v (always a slice for our commands) and writes it the way
// gh's own --json does: pretty-printed and syntax-highlighted on a terminal,
// compact (single line + newline) when piped.
func (a *App) writeJSON(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if a.outTTY {
		return jsonpretty.Format(a.Out, bytes.NewReader(raw), "  ", a.outColor)
	}
	if _, err := a.Out.Write(raw); err != nil {
		return err
	}
	_, err = a.Out.Write([]byte{'\n'})
	return err
}

// tableEnv returns the color scheme and width for the animated table.
func (a *App) tableEnv(t term.Term) (*ColorScheme, int) {
	w, _, _ := t.Size()
	if w <= 0 {
		w = 80
	}
	return &ColorScheme{enabled: t.IsColorEnabled()}, w
}

// rowsFor builds the table rows (one per source) from the current state.
func (a *App) rowsFor(srcs []Source, st *State) []Row {
	rows := make([]Row, len(srcs))
	for i, s := range srcs {
		rows[i] = a.rowFor(s, st)
	}
	return rows
}

// printOutcome prints the plain (non-animated) per-source result line for a
// pull: the primary output for that source. Warnings are not printed here - the
// caller collects them across sources and prints them in the secondary block
// (see printPullFooter).
func (a *App) printOutcome(s Source, out pullOutcome) {
	if out.err != nil {
		a.fail("%s: %v", s.Repo, out.err)
		return
	}
	if out.skipped {
		a.dim("  %s  up to date (%s)", s.Repo, short(out.newState.SHA))
		return
	}
	a.success("%s  %s (%s)", a.cs().Bold(s.Repo), pluralFiles(len(out.newState.Files)), a.cs().Gray(short(out.newState.SHA)))
}

// spinnerFrames is gh's exact progress spinner: briandowns CharSets[11], the set
// gh's iostreams uses (see StartProgressIndicatorWithLabel). Advanced at gh's
// 120ms cadence and rendered in cyan, matching gh's spinner.WithColor("fgCyan").
var spinnerFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

// liveRow holds the in-flight progress of one animating row, written by its pull
// goroutine and read by the render ticker.
type liveRow struct {
	mu       sync.Mutex
	sha      string
	updated  bool
	files    int
	start    time.Time
	done     bool
	final    Row
	newState SourceState
	warnings []string
	err      error
}

// animate pulls the targeted rows ONE AT A TIME (sequentially) while rendering
// the live table: the current row shows a yellow spinner, rows queued behind it
// show the yellow "•" pending icon, finished rows settle to their final state,
// and non-target rows render dimmed when dimOthers (the `add` focus effect) or
// full-color static. Results are applied to st; it returns the warnings
// collected across the pulled sources (for the caller's secondary block) and the
// first pull error.
func (a *App) animate(rows []Row, srcs []Source, targets []int, dimOthers bool, st *State, cs *ColorScheme, width int) ([]string, error) {
	out := a.Out

	// Per-target state, indexed by position in the targets order.
	lives := make([]*liveRow, len(targets))
	prevs := make([]SourceState, len(targets))
	hasPrev := make([]bool, len(targets))
	pos := make(map[int]int, len(targets)) // row index -> position in targets
	for p, i := range targets {
		lives[p] = &liveRow{}
		ss, ok := st.Sources[srcs[i].ID()]
		prevs[p], hasPrev[p] = ss, ok
		pos[i] = p
	}

	current := 0 // position of the row currently pulling
	doneCh := make(chan struct{}, 1)
	launch := func(p int) {
		i, lr := targets[p], lives[p]
		lr.mu.Lock()
		lr.start = time.Now()
		lr.mu.Unlock()
		go func() {
			onProgress := func(sha string, files int) {
				lr.mu.Lock()
				if sha != "" {
					lr.sha = sha
					lr.updated = hasPrev[p] && sha != prevs[p].SHA
				}
				lr.files = files
				lr.mu.Unlock()
			}
			o := a.pullSource(srcs[i], prevs[p], hasPrev[p], onProgress)
			final := o.row
			if o.err != nil {
				final = a.rowForState(srcs[i], prevs[p], hasPrev[p])
				final.State = StateFailed
			}
			lr.mu.Lock()
			lr.done = true
			lr.err = o.err
			lr.final = final
			lr.newState = o.newState
			lr.updated = o.updated
			lr.warnings = o.warnings
			lr.mu.Unlock()
			doneCh <- struct{}{}
		}()
	}

	frame := 0
	build := func() []rowView {
		views := make([]rowView, len(rows))
		for idx := range rows {
			p, ok := pos[idx]
			if !ok {
				views[idx] = rowView{Row: rows[idx], dim: dimOthers}
				continue
			}
			lr := lives[p]
			lr.mu.Lock()
			switch {
			case lr.done:
				views[idx] = rowView{Row: lr.final, updated: lr.updated}
			case p == current: // actively pulling
				rv := rows[idx] // keeps the PREVIOUS sha until the new one resolves
				resolved := lr.sha != ""
				if resolved {
					rv.SHA = lr.sha
				}
				rv.Files = lr.files
				views[idx] = rowView{
					Row:         rv,
					loading:     true,
					spinner:     spinnerFrames[frame%len(spinnerFrames)],
					elapsed:     elapsedSince(lr.start),
					updated:     lr.updated,
					shaResolved: resolved,
				}
			default: // queued behind the current row
				views[idx] = rowView{Row: rows[idx], pending: true}
			}
			lr.mu.Unlock()
		}
		return views
	}

	if len(targets) > 0 {
		launch(0)
	}
	shown := a.renderViews(build(), width, cs)
	fmt.Fprint(out, strings.Join(shown, "\n"), "\n")
	fmt.Fprint(out, "\x1b[?25l")       // hide cursor
	defer fmt.Fprint(out, "\x1b[?25h") // restore cursor

	repaint := func() {
		next := a.renderViews(build(), width, cs)
		a.paintDiff(out, shown, next)
		shown = next
	}

	ticker := time.NewTicker(120 * time.Millisecond) // gh's spinner cadence
	defer ticker.Stop()
	for current < len(targets) {
		select {
		case <-doneCh:
			current++
			if current < len(targets) {
				launch(current)
			}
			repaint()
		case <-ticker.C:
			frame++
			repaint()
		}
	}
	repaint() // final settled frame

	// Apply results (all goroutines have finished). Every attempt is recorded -
	// a failed pull of a new source persists as FAILED (not PENDING); an
	// existing source keeps its prior good install (pullSource's failState).
	var firstErr error
	var warnings []string
	for p, i := range targets {
		lr := lives[p]
		if lr.err != nil && firstErr == nil {
			firstErr = lr.err
		}
		st.Sources[srcs[i].ID()] = lr.newState
		warnings = append(warnings, lr.warnings...)
	}
	return warnings, firstErr
}

// paintDiff repaints the table in place, rewriting only the lines that changed
// (the cursor starts and ends at home, one line below the table).
func (a *App) paintDiff(out io.Writer, shown, next []string) {
	if len(shown) != len(next) {
		fmt.Fprintf(out, "\x1b[%dA\x1b[J%s\n", len(shown), strings.Join(next, "\n"))
		return
	}
	n := len(next)
	fmt.Fprintf(out, "\x1b[%dA", n) // to the first table line
	for i := 0; i < n; i++ {
		if next[i] != shown[i] {
			fmt.Fprintf(out, "\r\x1b[K%s\r", next[i])
		}
		if i < n-1 {
			fmt.Fprint(out, "\x1b[1B")
		}
	}
	fmt.Fprint(out, "\x1b[1B\r") // back down to home
}

// elapsedSince formats a running duration as whole seconds ("0s", "1s", …).
func elapsedSince(start time.Time) string {
	return fmt.Sprintf("%ds", int(time.Since(start).Seconds()))
}

// indexOfID returns the index of the source with the given id, or -1.
func indexOfID(srcs []Source, id string) int {
	for i, s := range srcs {
		if s.ID() == id {
			return i
		}
	}
	return -1
}

// pullOutcome is the result of pulling one source (see pullSource).
type pullOutcome struct {
	row      Row         // the resulting row
	newState SourceState // state to record (== prev when skipped)
	updated  bool        // an existing source's SHA moved (new/unchanged => false)
	skipped  bool        // already up to date; no fetch happened
	warnings []string    // non-fatal messages (no match, collisions, unsafe paths)
	err      error
}

// pullSource pulls one source given its previously recorded state. It is pure
// with respect to shared State: it writes files and returns the new SourceState
// for the caller to apply, and never mutates *State or prints (so it is safe to
// run concurrently for distinct sources). onProgress, when non-nil, reports the
// resolved SHA (early, before blobs) and the running file count.
func (a *App) pullSource(s Source, prev SourceState, hasPrev bool, onProgress func(sha string, files int)) pullOutcome {
	now := time.Now().UTC()
	// failState is what we persist when an attempt fails: a brand-new source
	// becomes a recorded FAILED row (no files) stamped with the attempt time, so
	// `list` shows it as FAILED rather than PENDING. An existing source keeps its
	// prior good install - one transient re-pull error shouldn't destroy a
	// working install or downgrade it.
	failState := func() pullOutcome {
		ns := prev
		if !hasPrev {
			ns = SourceState{Repo: s.Repo, Ref: s.Ref, Path: s.Path, PulledAt: now}
		}
		return pullOutcome{newState: ns, row: a.rowForState(s, ns, true)}
	}

	healthy := hasPrev && a.allFilesExist(prev.Files)
	if healthy {
		// Skip without any network call when the configured ref is an immutable
		// commit-ish (≥7 hex digits) that is a left-pinned prefix of the SHA we
		// already pulled - it can only point at that same commit.
		if refPinsTo(s.Ref, prev.SHA) {
			ns := prev
			ns.PulledAt = now // PULLED tracks the last attempt, even a no-op
			return pullOutcome{row: a.rowForState(s, ns, true), newState: ns, skipped: true}
		}
		// Otherwise resolve the current tip (one API call) and compare.
		sha, err := a.F.ResolveSHA(s)
		if err != nil {
			o := failState()
			o.err = err
			return o
		}
		if prev.SHA == sha {
			ns := prev
			ns.PulledAt = now // PULLED tracks the last attempt, even a no-op
			return pullOutcome{row: a.rowForState(s, ns, true), newState: ns, skipped: true}
		}
	}

	sha, files, err := a.F.Fetch(s, onProgress)
	if err != nil {
		o := failState()
		o.err = err
		return o
	}
	var warnings []string
	if len(files) == 0 {
		warnings = append(warnings, fmt.Sprintf("%s  no files matched %s", s.Repo, s.effectivePath()))
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
	missingApplyTo := 0         // installed files with no applyTo frontmatter value
	for _, f := range files {
		rel := s.DestPath(f.Rel)
		if rel == "" {
			warnings = append(warnings, fmt.Sprintf("%s  skipped unsafe path %s", s.Repo, f.Rel))
			continue
		}
		if first, dup := seen[rel]; dup {
			warnings = append(warnings, fmt.Sprintf("%s  %s and %s both map to %s, keeping %s", s.Repo, first, f.Rel, path.Base(rel), first))
			continue
		}
		seen[rel] = f.Rel
		if err := a.writeInstall(rel, f.Content); err != nil {
			o := failState()
			o.err = err
			return o
		}
		if !hasApplyTo(f.Content) {
			missingApplyTo++
		}
		installed = append(installed, rel)
	}
	// Files without an applyTo value are copied verbatim like any other, but VS
	// Code won't auto-apply a user-level file that lacks one - so flag them once,
	// per source, with the fix.
	if missingApplyTo > 0 {
		warnings = append(warnings, fmt.Sprintf("%s  %d of %d installed %s no applyTo value, so VS Code won't auto-apply %s (add applyTo: '**').",
			s.Repo, missingApplyTo, len(installed), have(missingApplyTo), them(missingApplyTo)))
	}
	// Prune this source's files that are no longer produced.
	if hasPrev {
		a.prune(prev.Files, installed)
	}
	sort.Strings(installed)
	ns := SourceState{
		Repo:     s.Repo,
		Ref:      s.Ref,
		Path:     s.Path,
		SHA:      sha,
		PulledAt: now,
		Files:    installed,
	}
	return pullOutcome{
		row:      a.rowForState(s, ns, true),
		newState: ns,
		updated:  hasPrev && sha != prev.SHA, // only an existing source moving counts as "updated"
		warnings: warnings,
	}
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

// Remove deletes the source identified by target - its slug, or an add-style
// spec/URL that resolves to it - from the config file and prunes its installed
// files. With asJSON it emits a JSON array of the remaining sources (like
// `list --json`).
func (a *App) Remove(target string, asJSON bool) error {
	removedFromFile, err := a.Paths.RemoveSource(target)
	if err != nil {
		return err
	}
	st, err := a.Paths.LoadState()
	if err != nil {
		return err
	}
	var removedIDs []string
	for id, ss := range st.Sources {
		if targetMatches(target, id, ss.Repo, ss.Ref, ss.Path) {
			a.prune(ss.Files, nil)
			os.RemoveAll(filepath.Join(a.Paths.InstallDir, FileDir, id))
			delete(st.Sources, id)
			removedIDs = append(removedIDs, id)
		}
	}
	if err := a.Paths.Save(st); err != nil {
		return err
	}
	if asJSON {
		return a.renderRemainingJSON()
	}
	if len(removedFromFile) == 0 && len(removedIDs) == 0 {
		a.dim("No source matched %q.", target)
		return nil
	}
	a.success("Removed %s", a.cs().Bold(target))
	if EnvSet() && len(removedFromFile) > 0 {
		a.blank()
		a.note("%s is set and overrides the config file.", EnvSources)
	}
	return nil
}

// renderSourceListJSON emits the current source list as JSON (the shape of
// `list --json`); ids in `updated` whose state is PULLED show as UPDATED. Every
// command's --json output uses this, so they all return the current sources.
func (a *App) renderSourceListJSON(updated map[string]bool) error {
	rows, _, err := a.ListRows()
	if err != nil {
		return err
	}
	return a.renderListJSONUpdated(rows, updated)
}

// renderRemainingJSON emits the current source list as JSON (the same shape as
// `list --json`); used by remove/remove --all to report the post-removal state.
func (a *App) renderRemainingJSON() error {
	rows, _, err := a.ListRows()
	if err != nil {
		return err
	}
	return a.renderListJSON(rows)
}

// RemoveAll clears all configured sources and removes every file we installed.
// With asJSON it emits a JSON array of the remaining sources (always empty).
func (a *App) RemoveAll(asJSON bool) error {
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
	if asJSON {
		return a.renderRemainingJSON()
	}
	a.success("Removed all configured sources and every installed file.")
	a.blank()
	a.dim("To remove the command itself: gh extension remove gh-copilot-instructions")
	return nil
}

// Source states. Casing convention (#5): the state word is UPPERCASE on every
// surface that spells it out - the piped TSV column and --json - matching gh's
// state strings (OPEN/MERGED). The colored TTY table shows a glyph instead of
// the word, so casing is moot there. pull/add add an UPDATED state in --json.
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

// rowForState builds a Row for a source from a given SourceState (present=false
// => a PENDING row with no recorded state): PULLED when every installed file is
// present, FAILED when a file is missing or none matched.
func (a *App) rowForState(s Source, ss SourceState, present bool) Row {
	r := Row{State: StatePending, ID: s.ID(), Repo: s.Repo, Ref: s.Ref}
	if present {
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

// rowFor builds a Row for a source from the current on-disk state.
func (a *App) rowFor(s Source, st *State) Row {
	ss, ok := st.Sources[s.ID()]
	return a.rowForState(s, ss, ok)
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

// printPullFooter prints the secondary block that follows a pull/add's primary
// output: a separating blank line, any warnings collected across the pulled
// sources (yellow "!" + gray, one topic per line), then the muted install-
// location line. When id is non-empty (a single source, as in `add`) the
// location points at that source's install directory; otherwise (a full `pull`)
// it points at the base install directory.
func (a *App) printPullFooter(warnings []string, id string) {
	a.blank()
	for _, w := range warnings {
		a.note("%s", w)
	}
	dir := a.Paths.InstallDir
	if id != "" {
		dir = filepath.Join(a.Paths.InstallDir, FileDir, id)
	}
	a.dim("Instructions installed to: %s", dir)
}

func pluralFiles(n int) string {
	if n == 1 {
		return "pulled 1 file"
	}
	return fmt.Sprintf("pulled %d files", n)
}

// have renders "file has"/"files have" for the applyTo warning's subject.
func have(n int) string {
	if n == 1 {
		return "file has"
	}
	return "files have"
}

// them renders the matching object pronoun for the applyTo warning.
func them(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
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
