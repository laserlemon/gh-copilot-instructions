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
	"strconv"
	"strings"

	"github.com/cli/go-gh/v2/pkg/tableprinter"
	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/cli/go-gh/v2/pkg/text"
)

// checkResult is one doctor check. Check is a fixed label (the same text every
// run); Note carries the dynamic reading - a healthy detail, a problem plus its
// fix, or (for a not-applicable check) why it doesn't apply here.
type checkResult struct {
	Status string `json:"status"` // statusOK | statusWarn | statusFail | statusNA
	Check  string `json:"check"`  // fixed label / title
	Note   string `json:"note,omitempty"`
}

const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
	statusNA   = "na" // doesn't apply to this machine/config (rendered dimmed)
)

// accountProbe answers "who am I" and rate-limit questions against the GitHub
// API. It is split out from the content fetcher so doctor's auth and rate-limit
// checks can be tested without a network.
type accountProbe interface {
	Whoami(token string) (login string, err error)
	RateLimit(token string) (remaining, limit int, err error)
	LatestRelease(token string) (tag string, err error)
}

// extensionRepo is this extension's own repository, queried for the latest
// release when checking whether an upgrade is available.
const extensionRepo = "laserlemon/gh-copilot-instructions"

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

func (apiProbe) LatestRelease(token string) (string, error) {
	client, err := newClient(token)
	if err != nil {
		return "", err
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := client.Get("repos/"+extensionRepo+"/releases/latest", &rel); err != nil {
		return "", err
	}
	return rel.TagName, nil
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

// diagnose runs the full fixed set of checks, always in the same order and
// always producing a row for each (a check that doesn't apply to this machine
// reports statusNA rather than being dropped).
func (a *App) diagnose() []checkResult {
	token := ambientToken()
	srcs, origin, serr := a.Paths.LoadSources()
	st, sterr := a.Paths.LoadState()

	// Resolve every source once (network); reused by reachability and updates.
	shas := map[string]string{}
	reach := a.checkReachable(srcs, shas)

	return []checkResult{
		a.checkUpgrade(token),
		a.checkAuth(token),
		a.checkRateLimit(token),
		a.checkSources(srcs, origin, serr),
		a.checkEnvOverride(),
		a.checkSourcesPerms(),
		reach,
		a.checkInstallDir(),
		a.checkUpdates(srcs, st, shas, sterr),
		a.checkFailedSources(srcs, st, sterr),
		a.checkInstalledFiles(st, sterr),
		a.checkFrontmatter(st, sterr),
		a.checkLeftoverState(srcs, st, sterr),
		a.checkVSCode(),
		a.checkCodespaces(),
		a.checkAutoPull(st, sterr),
	}
}

// --- individual checks -------------------------------------------------------
//
// Each returns a checkResult with a fixed Check label; the Note holds the
// dynamic detail (and, when there's a problem, the command to fix it).

func (a *App) checkUpgrade(token string) checkResult {
	const label = "Extension version"
	cur := a.Version
	if cur == "" || cur == "dev" {
		return checkResult{statusNA, label, "Built from source (no released version to compare)"}
	}
	latest, err := a.probe().LatestRelease(token)
	if err != nil || latest == "" {
		return checkResult{statusNA, label, "Couldn't check for a newer release"}
	}
	if semverLess(cur, latest) {
		return checkResult{statusWarn, label, fmt.Sprintf("%s is available (you have %s). Run gh extension upgrade gh-copilot-instructions", latest, cur)}
	}
	return checkResult{statusOK, label, fmt.Sprintf("Up to date (%s)", cur)}
}

func (a *App) checkAuth(token string) checkResult {
	const label = "GitHub authentication"
	if token == "" {
		return checkResult{statusWarn, label, "Anonymous: public repos only, low rate limit. Run gh auth login"}
	}
	login, err := a.probe().Whoami(token)
	if err != nil {
		return checkResult{statusFail, label, "Token is invalid or the API is unreachable. Run gh auth login"}
	}
	return checkResult{statusOK, label, fmt.Sprintf("Authenticated as %s", login)}
}

func (a *App) checkRateLimit(token string) checkResult {
	const label = "GitHub API rate limit"
	rem, lim, err := a.probe().RateLimit(token)
	if err != nil || lim == 0 {
		return checkResult{statusNA, label, "Unavailable (couldn't reach the GitHub API)"}
	}
	note := fmt.Sprintf("%d/%d requests remaining", rem, lim)
	switch {
	case rem == 0:
		return checkResult{statusFail, label, "Exhausted. Wait for it to reset, or authenticate for a higher limit"}
	case rem*10 < lim: // under 10% left
		return checkResult{statusWarn, label, "Running low: " + note + ". Authenticate for a higher limit"}
	}
	return checkResult{statusOK, label, note}
}

func (a *App) checkSources(srcs []Source, origin ConfigOrigin, err error) checkResult {
	const label = "Sources configured"
	if err != nil {
		return checkResult{statusFail, label, fmt.Sprintf("Couldn't read your sources (%v)", err)}
	}
	if origin == OriginNone || len(srcs) == 0 {
		return checkResult{statusWarn, label, "None. Add one: gh copilot-instructions source add <owner/repo>"}
	}
	where := "configuration file"
	if origin == OriginEnv {
		where = "GH_COPILOT_INSTRUCTIONS"
	}
	return checkResult{statusOK, label, fmt.Sprintf("%s (from your %s)", plur(len(srcs), "source", "sources"), where)}
}

func (a *App) checkEnvOverride() checkResult {
	const label = "Configuration override"
	if !EnvSet() {
		return checkResult{statusNA, label, "GH_COPILOT_INSTRUCTIONS is not set"}
	}
	if _, err := os.Stat(a.Paths.SourcesFile); err == nil {
		return checkResult{statusWarn, label, "GH_COPILOT_INSTRUCTIONS is overriding your sources file. Unset it to use the file"}
	}
	return checkResult{statusOK, label, "GH_COPILOT_INSTRUCTIONS is active (no sources file to shadow)"}
}

func (a *App) checkSourcesPerms() checkResult {
	const label = "Configuration file permissions"
	fi, err := os.Stat(a.Paths.SourcesFile)
	if err != nil {
		return checkResult{statusNA, label, "No sources file on disk"}
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return checkResult{statusWarn, label, fmt.Sprintf("Readable by other users (it can hold tokens). Run chmod 600 %s", abbrevHome(a.Paths.SourcesFile))}
	}
	return checkResult{statusOK, label, "Correct (600)"}
}

func (a *App) checkReachable(srcs []Source, shas map[string]string) checkResult {
	const label = "Source reachability"
	if len(srcs) == 0 {
		return checkResult{statusNA, label, "No sources configured"}
	}
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
		return checkResult{statusOK, label, "Every source is reachable on GitHub"}
	}
	return checkResult{statusFail, label, fmt.Sprintf("%d of %d unreachable: %s. Check the repo, ref, and your access", len(unreachable), len(srcs), strings.Join(dedupe(unreachable), ", "))}
}

func (a *App) checkUpdates(srcs []Source, st *State, shas map[string]string, sterr error) checkResult {
	const label = "Available source updates"
	if sterr != nil {
		return checkResult{statusNA, label, "State is unreadable"}
	}
	pulled, updates := 0, 0
	for _, s := range srcs {
		ss, ok := st.Sources[s.ID()]
		if !ok || ss.SHA == "" {
			continue
		}
		pulled++
		if cur, ok := shas[s.ID()]; ok && cur != ss.SHA {
			updates++
		}
	}
	if pulled == 0 {
		return checkResult{statusNA, label, "Nothing pulled yet"}
	}
	if updates > 0 {
		return checkResult{statusWarn, label, fmt.Sprintf("%d of %d pulled sources have updates. Run source pull", updates, pulled)}
	}
	return checkResult{statusOK, label, "All sources are up to date"}
}

func (a *App) checkFailedSources(srcs []Source, st *State, sterr error) checkResult {
	const label = "Failed sources"
	if sterr != nil {
		return checkResult{statusNA, label, "State is unreadable"}
	}
	if len(srcs) == 0 {
		return checkResult{statusNA, label, "No sources configured"}
	}
	// A source is "failed" here if it produced no installed files - either it was
	// never pulled (no state) or its pull matched nothing (empty file list). A
	// source whose files were installed and later went missing is a different
	// problem, reported by the "Pulled files" check.
	bad := 0
	for _, s := range srcs {
		ss, ok := st.Sources[s.ID()]
		if !ok || len(ss.Files) == 0 {
			bad++
		}
	}
	if bad > 0 {
		return checkResult{statusWarn, label, fmt.Sprintf("%d of %d produced no files. Run source pull (check the --path if it persists)", bad, len(srcs))}
	}
	return checkResult{statusOK, label, "Every source produced instructions files"}
}

func (a *App) checkInstallDir() checkResult {
	const label = "Instructions file directory"
	managed := filepath.Join(a.Paths.InstallDir, FileDir)
	disp := abbrevHome(managed)
	fi, err := os.Stat(managed)
	if os.IsNotExist(err) {
		return checkResult{statusNA, label, disp + " doesn't exist yet (nothing pulled)"}
	}
	if err != nil || !fi.IsDir() {
		return checkResult{statusFail, label, disp + " is not usable. Check permissions on ~/.copilot"}
	}
	if !dirWritable(managed) {
		return checkResult{statusFail, label, disp + " is not writable. Check its permissions"}
	}
	return checkResult{statusOK, label, disp + " is writable"}
}

// dirWritable reports whether a regular file can be created in dir.
func dirWritable(dir string) bool {
	probe := filepath.Join(dir, ".gci-doctor-write-test")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(probe)
	return true
}

func (a *App) checkInstalledFiles(st *State, sterr error) checkResult {
	const label = "Pulled files"
	if sterr != nil {
		return checkResult{statusFail, label, fmt.Sprintf("state.json is unreadable (%v). Run source pull", sterr)}
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
		return checkResult{statusNA, label, "Nothing pulled yet"}
	}
	if missing > 0 {
		return checkResult{statusFail, label, fmt.Sprintf("%d of %d files missing. Run source pull", missing, total)}
	}
	if orphans := a.orphanFiles(recorded); len(orphans) > 0 {
		return checkResult{statusWarn, label, fmt.Sprintf("%s not owned by any source. Run source pull, or delete them", plur(len(orphans), "file is", "files are"))}
	}
	return checkResult{statusOK, label, fmt.Sprintf("Every instructions file is present (%s)", plur(total, "instructions file", "instructions files"))}
}

func (a *App) checkFrontmatter(st *State, sterr error) checkResult {
	const label = "applyTo frontmatter"
	if sterr != nil {
		return checkResult{statusNA, label, "State is unreadable"}
	}
	checked, noApply := 0, 0
	for _, ss := range st.Sources {
		for _, rel := range ss.Files {
			data, err := os.ReadFile(filepath.Join(a.Paths.InstallDir, rel))
			if err != nil {
				continue // missing files are reported by checkInstalledFiles
			}
			checked++
			if !hasApplyTo(data) {
				noApply++
			}
		}
	}
	if checked == 0 {
		return checkResult{statusNA, label, "No files installed"}
	}
	if noApply > 0 {
		return checkResult{statusWarn, label, fmt.Sprintf("%d of %d files have no applyTo (VS Code won't auto-apply them)", noApply, checked)}
	}
	return checkResult{statusOK, label, fmt.Sprintf("Every instructions file declares applyTo (%s)", plur(checked, "instructions file", "instructions files"))}
}

func (a *App) checkLeftoverState(srcs []Source, st *State, sterr error) checkResult {
	const label = "Orphaned files"
	if sterr != nil {
		return checkResult{statusNA, label, "State is unreadable"}
	}
	if len(st.Sources) == 0 {
		return checkResult{statusNA, label, "No sources have been pulled"}
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
	if stale > 0 {
		return checkResult{statusWarn, label, fmt.Sprintf("Instructions files from %s remain. Run source remove <slug>", plur(stale, "deleted source", "deleted sources"))}
	}
	return checkResult{statusOK, label, "No instructions files from deleted sources"}
}

func (a *App) checkVSCode() checkResult {
	const label = "VS Code instructions"
	dirs := vscodePromptDirs()
	if len(dirs) == 0 {
		return checkResult{statusNA, label, "VS Code isn't installed"}
	}
	src := filepath.Join(a.Paths.InstallDir, FileDir)
	files := len(fileDigests(src))
	outOfSync := 0
	for _, d := range dirs {
		if !treesEqual(src, filepath.Join(d, FileDir)) {
			outOfSync++
		}
	}
	if outOfSync > 0 {
		return checkResult{statusWarn, label, "Out of date. Run source pull to re-sync them"}
	}
	note := fmt.Sprintf("Synchronized (%s)", plur(files, "file", "files"))
	if len(dirs) > 1 {
		note = fmt.Sprintf("Synchronized (%s across %s)", plur(files, "file", "files"), plur(len(dirs), "VS Code install", "VS Code installs"))
	}
	return checkResult{statusOK, label, note}
}

func (a *App) checkCodespaces() checkResult {
	const label = "Codespaces secret"
	if os.Getenv("CODESPACES") != "true" {
		return checkResult{statusNA, label, "Not running in a Codespace"}
	}
	if EnvSet() {
		return checkResult{statusOK, label, "GH_COPILOT_INSTRUCTIONS is set"}
	}
	return checkResult{statusWarn, label, "GH_COPILOT_INSTRUCTIONS isn't set. Add it as a Codespaces secret (see the README)"}
}

func (a *App) checkAutoPull(st *State, sterr error) checkResult {
	const label = "Auto-pull"
	sc := a.sched()
	if !sc.Supported() {
		return checkResult{statusNA, label, fmt.Sprintf("Not supported on %s yet", sc.Kind())}
	}
	if sterr != nil {
		return checkResult{statusNA, label, "State is unreadable"}
	}
	installed, err := sc.Installed()
	if err != nil {
		return checkResult{statusNA, label, "Couldn't read the scheduler"}
	}
	enabled := st.AutoPull != nil && st.AutoPull.Enabled
	switch {
	case enabled && !installed:
		return checkResult{statusFail, label, "Enabled but its scheduled job is missing. Run auto-pull enable"}
	case !enabled && installed:
		return checkResult{statusWarn, label, "A scheduled job is installed but auto-pull is disabled. Run auto-pull disable"}
	case enabled && installed:
		return checkResult{statusOK, label, "Enabled (scheduled job installed)"}
	}
	return checkResult{statusNA, label, "Disabled"}
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
		tp.AddField("NOTE", tableprinter.WithColor(cs.Header), padRight)
		tp.EndRow()
	}
	for _, r := range results {
		if !isTTY {
			tp.AddField(r.Status)
			tp.AddField(r.Check)
			tp.AddField(r.Note)
			tp.EndRow()
			continue
		}
		glyph, color := doctorIcon(r.Status, cs)
		tp.AddField(glyph, tableprinter.WithColor(color))
		// A not-applicable row is dimmed whole (label and note gray); every
		// other row keeps its label and note in the default foreground.
		if r.Status == statusNA {
			tp.AddField(r.Check, tableprinter.WithColor(cs.Gray))
			tp.AddField(r.Note, tableprinter.WithColor(cs.Gray))
		} else {
			tp.AddField(r.Check)
			tp.AddField(r.Note)
		}
		tp.EndRow()
	}
	_ = tp.Render()
}

// doctorIcon maps a status to its glyph and color: ✓ green, ! yellow, ✗ red, and
// a muted "-" for a check that doesn't apply.
func doctorIcon(status string, cs *ColorScheme) (string, func(string) string) {
	switch status {
	case statusOK:
		return "✓", cs.Green
	case statusWarn:
		return "!", cs.Yellow
	case statusFail:
		return "✗", cs.Red
	default: // statusNA
		return "-", cs.Gray
	}
}

// tally counts the real outcomes (not-applicable checks are excluded).
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

// semverLess reports whether version a is older than b. Each may carry a leading
// "v" and an optional "-prerelease" suffix; the major.minor.patch core is
// compared numerically, and a release outranks a same-core prerelease.
func semverLess(a, b string) bool {
	ac, ap := splitVersion(a)
	bc, bp := splitVersion(b)
	for i := 0; i < 3; i++ {
		if ac[i] != bc[i] {
			return ac[i] < bc[i]
		}
	}
	switch {
	case ap == "" && bp != "": // a is the release, b a prerelease -> a is newer
		return false
	case ap != "" && bp == "":
		return true
	default:
		return ap < bp
	}
}

// splitVersion parses "v1.2.3-rc.1" into its numeric core [1,2,3] and prerelease
// ("rc.1"). Missing or non-numeric parts read as 0.
func splitVersion(v string) (core [3]int, pre string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v, pre = v[:i], v[i+1:]
	}
	for i, p := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		core[i], _ = strconv.Atoi(strings.TrimSpace(p))
	}
	return core, pre
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
