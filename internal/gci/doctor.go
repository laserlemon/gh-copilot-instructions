package gci

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cli/go-gh/v2/pkg/tableprinter"
	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/cli/go-gh/v2/pkg/text"
)

// checkResult is one doctor check outcome. It maps directly to a table row
// (status icon, the finding, and a fix command) and to the --json output.
type checkResult struct {
	Status string `json:"status"` // statusOK | statusWarn | statusFail
	Check  string `json:"check"`  // the finding (what is true right now)
	Fix    string `json:"fix,omitempty"`
}

const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
)

// accountProbe answers "who am I" and rate-limit questions against the GitHub
// API. It is split out from the content fetcher so doctor's auth and rate-limit
// checks can be tested without a network.
type accountProbe interface {
	Whoami(token string) (login string, err error)
	RateLimit(token string) (remaining, limit int, err error)
}

// apiProbe is the real accountProbe, talking to the GitHub REST API.
type apiProbe struct{}

func (apiProbe) Whoami(token string) (string, error) {
	client, err := newClient(token)
	if err != nil {
		return "", err
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := client.Get("user", &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

func (apiProbe) RateLimit(token string) (int, int, error) {
	client, err := newClient(token)
	if err != nil {
		return 0, 0, err
	}
	var rl struct {
		Resources struct {
			Core struct {
				Limit     int `json:"limit"`
				Remaining int `json:"remaining"`
			} `json:"core"`
		} `json:"resources"`
	}
	if err := client.Get("rate_limit", &rl); err != nil {
		return 0, 0, err
	}
	return rl.Resources.Core.Remaining, rl.Resources.Core.Limit, nil
}

func (a *App) probe() accountProbe {
	if a.Probe != nil {
		return a.Probe
	}
	return apiProbe{}
}

// ambientToken resolves the token the tool would use for a source that carries
// no inline token: GH_COPILOT_INSTRUCTIONS_TOKEN, else gh auth, else empty.
func ambientToken() string { return resolveToken(Source{}) }

// Doctor runs every diagnostic check, renders the results (a table, or --json),
// and returns ErrReported when any check failed so the process exits non-zero.
func (a *App) Doctor(asJSON bool) error {
	results := a.diagnose()
	if asJSON {
		if err := a.writeJSON(results); err != nil {
			return err
		}
	} else {
		a.renderDoctor(results)
	}
	for _, r := range results {
		if r.Status == statusFail {
			return ErrReported
		}
	}
	return nil
}

// diagnose runs all checks in a sensible order (auth -> config -> reachability
// -> install/state -> surfaces -> auto-pull) and returns the results. Checks
// that don't apply to this machine (no VS Code, not in a Codespace, ...) are
// omitted rather than shown as passing.
func (a *App) diagnose() []checkResult {
	var out []checkResult
	add := func(r checkResult) { out = append(out, r) }
	maybe := func(r checkResult, ok bool) {
		if ok {
			out = append(out, r)
		}
	}

	token := ambientToken()
	add(a.checkAuth(token))
	maybe(a.checkRateLimit(token))

	srcs, origin, serr := a.Paths.LoadSources()
	add(a.checkSources(srcs, origin, serr))
	maybe(a.checkEnvOverride())
	maybe(a.checkSourcesPerms())

	// Resolve every source once; reuse the SHAs for the updates check.
	shas := map[string]string{}
	if serr == nil && len(srcs) > 0 {
		add(a.checkReachable(srcs, shas))
	}

	st, sterr := a.Paths.LoadState()
	add(a.checkInstallDir())
	add(a.checkInstalledFiles(st, sterr))
	maybe(a.checkNeverPulled(srcs, st, sterr))
	maybe(a.checkUpdates(srcs, st, shas, sterr))
	maybe(a.checkStaleState(srcs, st, sterr))

	maybe(a.checkVSCode())
	maybe(a.checkCodespaces())
	maybe(a.checkAutoPull(st, sterr))

	return out
}

// --- individual checks -------------------------------------------------------

func (a *App) checkAuth(token string) checkResult {
	if token == "" {
		return checkResult{statusWarn,
			"Not authenticated to GitHub (anonymous: public repos only, low rate limit)",
			"gh auth login"}
	}
	login, err := a.probe().Whoami(token)
	if err != nil {
		return checkResult{statusFail,
			"GitHub token is invalid or the API is unreachable",
			"gh auth login (or check GH_COPILOT_INSTRUCTIONS_TOKEN)"}
	}
	return checkResult{statusOK, fmt.Sprintf("Authenticated to GitHub as %s", login), ""}
}

func (a *App) checkRateLimit(token string) (checkResult, bool) {
	rem, lim, err := a.probe().RateLimit(token)
	if err != nil || lim == 0 {
		return checkResult{}, false // API unreachable: the auth check already covers it
	}
	msg := fmt.Sprintf("GitHub API rate limit: %d/%d requests remaining", rem, lim)
	switch {
	case rem == 0:
		return checkResult{statusFail, "GitHub API rate limit is exhausted",
			"Wait for it to reset, or authenticate for a higher limit: gh auth login"}, true
	case rem*10 < lim: // under 10% left
		return checkResult{statusWarn, msg + " (running low)",
			"Authenticate for a higher limit: gh auth login"}, true
	}
	return checkResult{statusOK, msg, ""}, true
}

func (a *App) checkSources(srcs []Source, origin ConfigOrigin, err error) checkResult {
	if err != nil {
		return checkResult{statusFail, fmt.Sprintf("Couldn't read your sources (%v)", err),
			"Check " + abbrevHome(a.Paths.SourcesFile)}
	}
	if origin == OriginNone || len(srcs) == 0 {
		return checkResult{statusWarn, "No sources are configured",
			"gh copilot-instructions source add <owner/repo>"}
	}
	where := "your config file"
	if origin == OriginEnv {
		where = "the GH_COPILOT_INSTRUCTIONS env var"
	}
	return checkResult{statusOK,
		fmt.Sprintf("%s configured (from %s)", plur(len(srcs), "source", "sources"), where), ""}
}

func (a *App) checkEnvOverride() (checkResult, bool) {
	if !EnvSet() {
		return checkResult{}, false
	}
	if _, err := os.Stat(a.Paths.SourcesFile); err == nil {
		return checkResult{statusWarn,
			"GH_COPILOT_INSTRUCTIONS is set and overrides your sources file",
			"Unset it to use the file, or ignore this if intended"}, true
	}
	return checkResult{}, false
}

func (a *App) checkSourcesPerms() (checkResult, bool) {
	fi, err := os.Stat(a.Paths.SourcesFile)
	if err != nil {
		return checkResult{}, false // no file (env mode / fresh install): nothing to check
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return checkResult{statusWarn,
			"Your sources file is readable by other users (it can hold tokens)",
			fmt.Sprintf("chmod 600 %s", abbrevHome(a.Paths.SourcesFile))}, true
	}
	return checkResult{statusOK, "Sources file permissions are correct (600)", ""}, true
}

func (a *App) checkReachable(srcs []Source, shas map[string]string) checkResult {
	var unreachable []string
	for _, s := range srcs {
		sha, err := a.F.ResolveSHA(s)
		if err != nil {
			unreachable = append(unreachable, s.Repo)
			continue
		}
		shas[s.ID()] = sha
	}
	if len(unreachable) == 0 {
		return checkResult{statusOK,
			fmt.Sprintf("All %s reachable on GitHub", plur(len(srcs), "source is", "sources are")), ""}
	}
	return checkResult{statusFail,
		fmt.Sprintf("%d of %d sources are unreachable: %s", len(unreachable), len(srcs), strings.Join(dedupe(unreachable), ", ")),
		"Verify the repo and ref, and your access (gh auth / --token)"}
}

func (a *App) checkInstallDir() checkResult {
	dir := a.Paths.InstallDir
	fi, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return checkResult{statusWarn, "The install directory doesn't exist yet (nothing pulled)",
			"gh copilot-instructions source pull"}
	}
	if err != nil || !fi.IsDir() {
		return checkResult{statusFail, fmt.Sprintf("Install directory is not usable: %s", abbrevHome(dir)),
			"Check permissions on ~/.copilot"}
	}
	probe := filepath.Join(dir, ".gci-doctor-write-test")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return checkResult{statusFail, fmt.Sprintf("Install directory is not writable: %s", abbrevHome(dir)),
			"Check permissions on " + abbrevHome(dir)}
	}
	f.Close()
	os.Remove(probe)
	return checkResult{statusOK, fmt.Sprintf("Install directory is present and writable (%s)", abbrevHome(dir)), ""}
}

func (a *App) checkInstalledFiles(st *State, sterr error) checkResult {
	if sterr != nil {
		return checkResult{statusFail, fmt.Sprintf("state.json is unreadable (%v)", sterr),
			"gh copilot-instructions source pull (regenerates it)"}
	}
	recorded := map[string]bool{}
	total, missing := 0, 0
	for _, ss := range st.Sources {
		for _, rel := range ss.Files {
			recorded[rel] = true
			total++
			if _, err := os.Stat(filepath.Join(a.Paths.InstallDir, rel)); err != nil {
				missing++
			}
		}
	}
	if total == 0 {
		return checkResult{statusOK, "No instruction files are installed yet", ""}
	}
	if missing > 0 {
		return checkResult{statusFail,
			fmt.Sprintf("%d of %d installed files are missing", missing, total),
			"gh copilot-instructions source pull"}
	}
	if orphans := a.orphanFiles(recorded); len(orphans) > 0 {
		return checkResult{statusWarn,
			fmt.Sprintf("%s in the install dir not owned by any source", plur(len(orphans), "file", "files")),
			"gh copilot-instructions source pull (or delete them)"}
	}
	return checkResult{statusOK, fmt.Sprintf("All %s present", plur(total, "installed file is", "installed files are")), ""}
}

func (a *App) checkNeverPulled(srcs []Source, st *State, sterr error) (checkResult, bool) {
	if sterr != nil || len(srcs) == 0 {
		return checkResult{}, false
	}
	never := 0
	for _, s := range srcs {
		if _, ok := st.Sources[s.ID()]; !ok {
			never++
		}
	}
	if never == 0 {
		return checkResult{}, false
	}
	return checkResult{statusWarn,
		fmt.Sprintf("%d of %d sources have never been pulled", never, len(srcs)),
		"gh copilot-instructions source pull"}, true
}

func (a *App) checkUpdates(srcs []Source, st *State, shas map[string]string, sterr error) (checkResult, bool) {
	if sterr != nil {
		return checkResult{}, false
	}
	updates := 0
	for _, s := range srcs {
		cur, ok := shas[s.ID()]
		if !ok {
			continue // unreachable or not resolved
		}
		ss, ok := st.Sources[s.ID()]
		if !ok || ss.SHA == "" {
			continue // never pulled: covered by checkNeverPulled
		}
		if cur != ss.SHA {
			updates++
		}
	}
	if updates == 0 {
		return checkResult{}, false
	}
	return checkResult{statusWarn,
		fmt.Sprintf("%d of %d sources have updates available", updates, len(srcs)),
		"gh copilot-instructions source pull"}, true
}

func (a *App) checkStaleState(srcs []Source, st *State, sterr error) (checkResult, bool) {
	if sterr != nil {
		return checkResult{}, false
	}
	configured := map[string]bool{}
	for _, s := range srcs {
		configured[s.ID()] = true
	}
	stale := 0
	for id := range st.Sources {
		if !configured[id] {
			stale++
		}
	}
	if stale == 0 {
		return checkResult{}, false
	}
	return checkResult{statusWarn,
		fmt.Sprintf("%s left over from sources you removed", plur(stale, "state entry", "state entries")),
		"gh copilot-instructions source pull (reconciles state)"}, true
}

func (a *App) checkVSCode() (checkResult, bool) {
	dirs := vscodePromptDirs()
	if len(dirs) == 0 {
		return checkResult{}, false // VS Code isn't installed: nothing to mirror
	}
	src := filepath.Join(a.Paths.InstallDir, FileDir)
	outOfSync := 0
	for _, d := range dirs {
		if !treesEqual(src, filepath.Join(d, FileDir)) {
			outOfSync++
		}
	}
	if outOfSync > 0 {
		return checkResult{statusWarn,
			fmt.Sprintf("VS Code prompts are out of sync (%s)", plur(outOfSync, "directory", "directories")),
			"gh copilot-instructions source pull"}, true
	}
	return checkResult{statusOK,
		fmt.Sprintf("VS Code prompts are in sync (%s)", plur(len(dirs), "directory", "directories")), ""}, true
}

func (a *App) checkCodespaces() (checkResult, bool) {
	if os.Getenv("CODESPACES") != "true" {
		return checkResult{}, false
	}
	if EnvSet() {
		return checkResult{statusOK, "Running in a Codespace with GH_COPILOT_INSTRUCTIONS set", ""}, true
	}
	return checkResult{statusWarn,
		"In a Codespace but GH_COPILOT_INSTRUCTIONS isn't set (instructions won't apply here)",
		"Add it as a Codespaces secret (see the README)"}, true
}

func (a *App) checkAutoPull(st *State, sterr error) (checkResult, bool) {
	if sterr != nil {
		return checkResult{}, false
	}
	sc := a.sched()
	if !sc.Supported() {
		return checkResult{}, false // only meaningful where we can schedule
	}
	installed, err := sc.Installed()
	if err != nil {
		return checkResult{}, false
	}
	enabled := st.AutoPull != nil && st.AutoPull.Enabled
	switch {
	case enabled && !installed:
		return checkResult{statusFail, "Auto-pull is enabled but its scheduled job is missing",
			"gh copilot-instructions auto-pull enable"}, true
	case !enabled && installed:
		return checkResult{statusWarn, "A scheduled auto-pull job is installed but auto-pull is disabled",
			"gh copilot-instructions auto-pull disable"}, true
	case enabled && installed:
		return checkResult{statusOK, "Auto-pull is enabled and its scheduled job is installed", ""}, true
	}
	return checkResult{}, false // disabled and no job: nothing to report
}

// --- rendering ---------------------------------------------------------------

func (a *App) renderDoctor(results []checkResult) {
	t := term.FromEnv()
	isTTY := t.IsTerminalOutput()
	w, _, _ := t.Size()
	if w <= 0 {
		w = 80
	}
	cs := &ColorScheme{enabled: t.IsColorEnabled()}

	a.renderDoctorTable(a.Out, results, isTTY, w, cs)

	if !isTTY {
		return
	}
	ok, warn, fail := tally(results)
	a.blank()
	switch {
	case fail > 0:
		a.msg("%s %s, %s.", cs.Red("✗"),
			plur(fail, "check is failing", "checks are failing"),
			plur(warn, "warning", "warnings"))
	case warn > 0:
		a.msg("%s %s (%d ok).", cs.Yellow("!"), plur(warn, "warning", "warnings"), ok)
	default:
		a.success("Everything looks healthy.")
	}
}

func (a *App) renderDoctorTable(w io.Writer, results []checkResult, isTTY bool, width int, cs *ColorScheme) {
	padRight := tableprinter.WithPadding(text.PadRight)
	tp := tableprinter.New(w, isTTY, width)
	if isTTY {
		tp.AddField("", tableprinter.WithColor(cs.Header))
		tp.AddField("CHECK", tableprinter.WithColor(cs.Header), padRight)
		tp.AddField("FIX", tableprinter.WithColor(cs.Header), padRight)
		tp.EndRow()
	}
	for _, r := range results {
		if isTTY {
			glyph, color := doctorIcon(r.Status, cs)
			tp.AddField(glyph, tableprinter.WithColor(color))
		} else {
			tp.AddField(r.Status)
		}
		tp.AddField(r.Check)
		tp.AddField(r.Fix)
		tp.EndRow()
	}
	_ = tp.Render()
}

func doctorIcon(status string, cs *ColorScheme) (string, func(string) string) {
	switch status {
	case statusOK:
		return "✓", cs.Green
	case statusWarn:
		return "!", cs.Yellow
	default:
		return "✗", cs.Red
	}
}

func tally(results []checkResult) (ok, warn, fail int) {
	for _, r := range results {
		switch r.Status {
		case statusOK:
			ok++
		case statusWarn:
			warn++
		case statusFail:
			fail++
		}
	}
	return
}

// --- small helpers -----------------------------------------------------------

// plur formats a count with a singular or plural noun phrase, e.g.
// plur(1,"source","sources") -> "1 source", plur(2,...) -> "2 sources".
func plur(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// dedupe returns the unique values of s, preserving first-seen order.
func dedupe(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// orphanFiles returns install-dir files (relative to InstallDir) under our
// managed subtree that are not in the recorded set.
func (a *App) orphanFiles(recorded map[string]bool) []string {
	root := filepath.Join(a.Paths.InstallDir, FileDir)
	var orphans []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(a.Paths.InstallDir, p)
		if e != nil {
			return nil
		}
		if !recorded[rel] {
			orphans = append(orphans, rel)
		}
		return nil
	})
	sort.Strings(orphans)
	return orphans
}

// treesEqual reports whether two directory subtrees contain the same regular
// files with identical contents (used to check the VS Code mirror). A missing
// tree is treated as empty, so two absent trees compare equal.
func treesEqual(src, dst string) bool {
	a, b := fileDigests(src), fileDigests(dst)
	if len(a) != len(b) {
		return false
	}
	for rel, sum := range a {
		if b[rel] != sum {
			return false
		}
	}
	return true
}

func fileDigests(root string) map[string]string {
	m := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return nil
		}
		data, e := os.ReadFile(p)
		if e != nil {
			return nil
		}
		sum := sha256.Sum256(data)
		m[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	return m
}
