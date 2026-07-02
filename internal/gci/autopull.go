package gci

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Cadence is how often the scheduled background pull runs: a positive count of a
// base unit (hour, day, or week). The clock starts when `auto-pull enable` runs.
type Cadence struct {
	N    int    // >= 1
	Unit string // "hour" | "day" | "week"
}

// DefaultEvery is the --every value used when the flag is omitted.
const DefaultEvery = "day"

// launchdLabel is the reverse-DNS LaunchAgent label (and plist basename) on macOS.
const launchdLabel = "com.github.laserlemon.gh-copilot-instructions"

// unitSeconds maps a base unit to its length in seconds.
var unitSeconds = map[string]int{"hour": 3600, "day": 86400, "week": 604800}

// ParseCadence parses an --every value: a base unit with an optional leading
// count. Accepts "hour"/"day"/"week", the shorthands "h"/"d"/"w", and
// integer-prefixed forms like "3h", "2d", "1w" (a bare unit means a count of 1).
func ParseCadence(s string) (Cadence, error) {
	orig := s
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return Cadence{}, fmt.Errorf("empty cadence (want e.g. hour, day, week, 3h, 2d, 1w)")
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n := 1
	if i > 0 {
		v, err := strconv.Atoi(s[:i])
		if err != nil || v < 1 {
			return Cadence{}, fmt.Errorf("invalid cadence count in %q (must be a positive integer)", orig)
		}
		n = v
	}
	unit, ok := normalizeUnit(strings.TrimSpace(s[i:]))
	if !ok {
		return Cadence{}, fmt.Errorf("invalid cadence unit in %q (want hour/day/week or h/d/w, optionally with a count like 3h)", orig)
	}
	return Cadence{N: n, Unit: unit}, nil
}

func normalizeUnit(u string) (string, bool) {
	switch u {
	case "h", "hr", "hrs", "hour", "hours":
		return "hour", true
	case "d", "day", "days":
		return "day", true
	case "w", "wk", "week", "weeks":
		return "week", true
	default:
		return "", false
	}
}

// Seconds is the interval a cadence maps to (launchd StartInterval).
func (c Cadence) Seconds() int { return c.N * unitSeconds[c.Unit] }

// Shorthand is the compact canonical form ("1d", "3h", "2w"), used for storage
// and JSON so machine consumers see a stable value.
func (c Cadence) Shorthand() string { return fmt.Sprintf("%d%c", c.N, c.Unit[0]) }

// Human renders the cadence for status output ("every day", "every 3 hours").
func (c Cadence) Human() string {
	if c.N == 1 {
		return "every " + c.Unit
	}
	return fmt.Sprintf("every %d %ss", c.N, c.Unit)
}

// scheduler installs, removes, and inspects the OS-level recurring job that runs
// `gh copilot-instructions pull`. Only macOS (launchd) is supported today; other
// platforms return a friendly message from Enable/Disable and report
// Supported() == false.
type scheduler interface {
	Enable(c Cadence) error
	Disable() error
	Installed() (bool, error)
	Supported() bool
	Kind() string // human label for status, e.g. "launchd" or the GOOS name
}

// newScheduler returns the scheduler for the current OS.
func newScheduler(p Paths) scheduler {
	if runtime.GOOS == "darwin" {
		return &launchdScheduler{
			plist:   filepath.Join(homeDir(), "Library", "LaunchAgents", launchdLabel+".plist"),
			logPath: filepath.Join(p.StateDir, "auto-pull.log"),
		}
	}
	return unsupportedScheduler{}
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// ghPath resolves the absolute path to the gh executable, which the scheduled
// job invokes directly so it does not depend on the daemon's PATH.
func ghPath() (string, error) {
	p, err := exec.LookPath("gh")
	if err != nil {
		return "", fmt.Errorf("could not find the gh executable on PATH: %w", err)
	}
	return filepath.Abs(p)
}

// ---- macOS: launchd -------------------------------------------------------

type launchdScheduler struct {
	plist   string
	logPath string
}

func (launchdScheduler) Kind() string    { return "launchd" }
func (launchdScheduler) Supported() bool { return true }

func (l launchdScheduler) Enable(c Cadence) error {
	gh, err := ghPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.plist), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.logPath), 0o755); err != nil {
		return err
	}
	body := launchdPlist(launchdLabel, gh, l.logPath, c)
	if err := os.WriteFile(l.plist, []byte(body), 0o644); err != nil {
		return err
	}
	// Reload so a cadence change takes effect: bootout the old instance (ignore
	// "not loaded" errors), then bootstrap the new one. Fall back to the older
	// load -w for launchctl builds without bootstrap.
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain+"/"+launchdLabel).Run()
	if err := exec.Command("launchctl", "bootstrap", domain, l.plist).Run(); err != nil {
		_ = exec.Command("launchctl", "unload", l.plist).Run()
		if err2 := exec.Command("launchctl", "load", "-w", l.plist).Run(); err2 != nil {
			return fmt.Errorf("launchctl could not load the agent: %v", err)
		}
	}
	return nil
}

func (l launchdScheduler) Disable() error {
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain+"/"+launchdLabel).Run()
	_ = exec.Command("launchctl", "unload", l.plist).Run()
	if err := os.Remove(l.plist); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (l launchdScheduler) Installed() (bool, error) {
	_, err := os.Stat(l.plist)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// launchdPlist renders the LaunchAgent plist for a cadence. It is a pure
// function so it can be unit-tested; xml.EscapeText guards the interpolated
// paths. The job redirects stdout/stderr to logPath for after-the-fact
// debugging of an unattended run.
func launchdPlist(label, gh, logPath string, c Cadence) string {
	esc := func(s string) string {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + esc(label) + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + esc(gh) + `</string>
		<string>copilot-instructions</string>
		<string>pull</string>
	</array>
	<key>StartInterval</key>
	<integer>` + strconv.Itoa(c.Seconds()) + `</integer>
	<key>StandardOutPath</key>
	<string>` + esc(logPath) + `</string>
	<key>StandardErrorPath</key>
	<string>` + esc(logPath) + `</string>
	<key>ProcessType</key>
	<string>Background</string>
</dict>
</plist>
`
}

// ---- Unsupported platforms (Linux, Windows, ...) --------------------------

type unsupportedScheduler struct{}

func (unsupportedScheduler) Kind() string             { return runtime.GOOS }
func (unsupportedScheduler) Supported() bool          { return false }
func (unsupportedScheduler) Installed() (bool, error) { return false, nil }

func (unsupportedScheduler) unsupported() error {
	return fmt.Errorf("auto-pull is macOS-only for now. On %s, schedule gh copilot-instructions pull yourself with cron or Task Scheduler", runtime.GOOS)
}

func (u unsupportedScheduler) Enable(Cadence) error { return u.unsupported() }
func (u unsupportedScheduler) Disable() error       { return u.unsupported() }

// ---- App wiring -----------------------------------------------------------

// sched returns the App's scheduler, defaulting to the platform one when unset
// (New wires it; tests may inject a fake).
func (a *App) sched() scheduler {
	if a.Sched == nil {
		a.Sched = newScheduler(a.Paths)
	}
	return a.Sched
}

// autoPullJSON is the shape emitted by `auto-pull ... --json`.
type autoPullJSON struct {
	Enabled         bool   `json:"enabled"`
	Cadence         string `json:"cadence"`
	IntervalSeconds int    `json:"intervalSeconds"`
	Scheduler       string `json:"scheduler"`
	Supported       bool   `json:"supported"`
	Installed       bool   `json:"installed"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
}

// AutoPullEnable turns on (or reconfigures) the scheduled background pull, then
// prints the shared status block.
func (a *App) AutoPullEnable(cadence Cadence, asJSON bool) error {
	if err := a.sched().Enable(cadence); err != nil {
		return err
	}
	st, err := a.Paths.LoadState()
	if err != nil {
		return err
	}
	st.AutoPull = &AutoPullState{Enabled: true, Cadence: cadence.Shorthand(), UpdatedAt: time.Now().UTC()}
	if err := a.Paths.Save(st); err != nil {
		return err
	}
	if asJSON {
		return a.renderAutoPullJSON(st)
	}
	a.printAutoPull(st)
	return nil
}

// AutoPullDisable turns off the scheduled background pull, then prints the shared
// status block.
func (a *App) AutoPullDisable(asJSON bool) error {
	if err := a.sched().Disable(); err != nil {
		return err
	}
	st, err := a.Paths.LoadState()
	if err != nil {
		return err
	}
	st.AutoPull = &AutoPullState{Enabled: false, Cadence: cadenceFromState(st).Shorthand(), UpdatedAt: time.Now().UTC()}
	if err := a.Paths.Save(st); err != nil {
		return err
	}
	if asJSON {
		return a.renderAutoPullJSON(st)
	}
	a.printAutoPull(st)
	return nil
}

// AutoPullStatus prints the shared status block for the current state.
func (a *App) AutoPullStatus(asJSON bool) error {
	st, err := a.Paths.LoadState()
	if err != nil {
		return err
	}
	if asJSON {
		return a.renderAutoPullJSON(st)
	}
	a.printAutoPull(st)
	return nil
}

// printAutoPull renders the one canonical status block that enable, disable, and
// status all share, following the tool's primary/secondary model: a single
// primary headline (status icon + primary text), a blank separator line, then a
// gray secondary block (one topic per line). Headline icons match the `list`
// command: green ✓ (enabled), red ✗ (disabled), yellow ! (unsupported). Notes in
// the secondary block use the yellow ! status icon with gray text; the enable
// command stays the primary color so it stands out as the thing to run.
//
//	✓ Auto-pull is enabled to pull every 3 hours.
//
//	Runs: gh copilot-instructions pull
//	Log:  ~/.local/state/gh-copilot-instructions/auto-pull.log
//
//	✗ Auto-pull is disabled.
//
//	Enable it with: gh copilot-instructions auto-pull enable
func (a *App) printAutoPull(st *State) {
	cs := a.cs()
	sc := a.sched()
	if !sc.Supported() {
		a.warn("Auto-pull isn't supported on %s yet (macOS only for now).", sc.Kind())
		a.blank()
		a.dim("Schedule it yourself with cron or Task Scheduler: gh copilot-instructions pull")
		return
	}
	enabled := st.AutoPull != nil && st.AutoPull.Enabled
	installed, ierr := sc.Installed()
	if enabled {
		a.msg("%s Auto-pull is enabled to pull %s.", cs.Green("✓"), cadenceFromState(st).Human())
		a.blank()
		if ierr == nil && !installed {
			a.note("The scheduled job is missing. Reinstall it: gh copilot-instructions auto-pull enable")
		}
		if srcs, origin, _ := a.Paths.LoadSources(); origin == OriginNone || len(srcs) == 0 {
			a.note("No sources are configured yet. Add a source: gh copilot-instructions add <owner/repo>")
		}
		a.dim("Runs: gh copilot-instructions pull")
		a.dim("Log:  %s", filepath.Join(a.Paths.StateDir, "auto-pull.log"))
		return
	}
	a.msg("%s Auto-pull is disabled.", cs.Red("✗"))
	a.blank()
	if ierr == nil && installed {
		a.note("A scheduled job is still installed. Remove it: gh copilot-instructions auto-pull disable")
	}
	// The enable command stays the primary (default) foreground so it stands out
	// as the thing to run; only its "Enable it with:" label is muted.
	a.msg("%s%s", cs.Gray("Enable it with: "), "gh copilot-instructions auto-pull enable")
}

func (a *App) renderAutoPullJSON(st *State) error {
	installed, _ := a.sched().Installed()
	c := cadenceFromState(st)
	out := autoPullJSON{
		Cadence:         c.Shorthand(),
		IntervalSeconds: c.Seconds(),
		Scheduler:       a.sched().Kind(),
		Supported:       a.sched().Supported(),
		Installed:       installed,
	}
	if st.AutoPull != nil {
		out.Enabled = st.AutoPull.Enabled
		if !st.AutoPull.UpdatedAt.IsZero() {
			out.UpdatedAt = st.AutoPull.UpdatedAt.Format(time.RFC3339)
		}
	}
	return a.writeJSON(out)
}

// cadenceFromState returns the recorded cadence, or the default when absent or
// unparseable.
func cadenceFromState(st *State) Cadence {
	if st.AutoPull != nil && st.AutoPull.Cadence != "" {
		if c, err := ParseCadence(st.AutoPull.Cadence); err == nil {
			return c
		}
	}
	c, _ := ParseCadence(DefaultEvery)
	return c
}
