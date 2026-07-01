package gci

import (
	"bytes"
	"strings"
	"testing"
)

// fakeScheduler is an in-memory scheduler for exercising the App auto-pull flow
// without touching launchd.
type fakeScheduler struct {
	enabled   bool
	cadence   Cadence
	installed bool
	supported bool
	enableErr error
	kind      string
}

func newFakeScheduler() *fakeScheduler { return &fakeScheduler{supported: true} }

func (f *fakeScheduler) Enable(c Cadence) error {
	if f.enableErr != nil {
		return f.enableErr
	}
	f.enabled, f.cadence, f.installed = true, c, true
	return nil
}
func (f *fakeScheduler) Disable() error           { f.enabled, f.installed = false, false; return nil }
func (f *fakeScheduler) Installed() (bool, error) { return f.installed, nil }
func (f *fakeScheduler) Supported() bool          { return f.supported }
func (f *fakeScheduler) Kind() string {
	if f.kind == "" {
		return "fake"
	}
	return f.kind
}

func errBuf(a *App) string { return a.Err.(*bytes.Buffer).String() }
func outBuf(a *App) string { return a.Out.(*bytes.Buffer).String() }

func TestParseCadence(t *testing.T) {
	ok := map[string]Cadence{
		"hour":  {1, "hour"},
		"h":     {1, "hour"},
		"3h":    {3, "hour"},
		"day":   {1, "day"},
		"d":     {1, "day"},
		"2d":    {2, "day"},
		"week":  {1, "week"},
		"w":     {1, "week"},
		"1w":    {1, "week"},
		" 2W ":  {2, "week"},
		"weeks": {1, "week"},
	}
	for in, want := range ok {
		got, err := ParseCadence(in)
		if err != nil {
			t.Fatalf("ParseCadence(%q) errored: %v", in, err)
		}
		if got != want {
			t.Fatalf("ParseCadence(%q) = %+v, want %+v", in, got, want)
		}
	}
	for _, bad := range []string{"", "monthly", "0d", "-1h", "3", "3x", "hourly", "1.5d"} {
		if _, err := ParseCadence(bad); err == nil {
			t.Fatalf("ParseCadence(%q) should error", bad)
		}
	}
}

func TestCadenceDerivations(t *testing.T) {
	cases := []struct {
		c    Cadence
		secs int
		sh   string
		hum  string
	}{
		{Cadence{1, "hour"}, 3600, "1h", "every hour"},
		{Cadence{3, "hour"}, 10800, "3h", "every 3 hours"},
		{Cadence{1, "day"}, 86400, "1d", "every day"},
		{Cadence{2, "day"}, 172800, "2d", "every 2 days"},
		{Cadence{1, "week"}, 604800, "1w", "every week"},
	}
	for _, c := range cases {
		if c.c.Seconds() != c.secs {
			t.Fatalf("%+v seconds=%d want %d", c.c, c.c.Seconds(), c.secs)
		}
		if c.c.Shorthand() != c.sh {
			t.Fatalf("%+v shorthand=%q want %q", c.c, c.c.Shorthand(), c.sh)
		}
		if c.c.Human() != c.hum {
			t.Fatalf("%+v human=%q want %q", c.c, c.c.Human(), c.hum)
		}
	}
}

func TestLaunchdPlist(t *testing.T) {
	p := launchdPlist(launchdLabel, "/opt/homebrew/bin/gh", "/tmp/log", Cadence{3, "hour"})
	for _, want := range []string{
		"<string>" + launchdLabel + "</string>",
		"<string>/opt/homebrew/bin/gh</string>",
		"<string>copilot-instructions</string>",
		"<string>pull</string>",
		"<integer>10800</integer>",
		"<string>/tmp/log</string>",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("plist missing %q\n%s", want, p)
		}
	}
	// XML special characters in a path must be escaped.
	esc := launchdPlist(launchdLabel, "/a & b/gh", "/l", Cadence{1, "day"})
	if strings.Contains(esc, "/a & b/gh") || !strings.Contains(esc, "/a &amp; b/gh") {
		t.Fatalf("path not XML-escaped:\n%s", esc)
	}
}

func TestAutoPullEnableDisableStatus(t *testing.T) {
	a := newTestApp(t, &fakeFetcher{})
	fs := newFakeScheduler()
	a.Sched = fs

	if err := a.AutoPullEnable(Cadence{3, "hour"}, false); err != nil {
		t.Fatal(err)
	}
	if !fs.enabled || fs.cadence != (Cadence{3, "hour"}) {
		t.Fatalf("scheduler not enabled every 3h: %+v", fs)
	}
	st, _ := a.Paths.LoadState()
	if st.AutoPull == nil || !st.AutoPull.Enabled || st.AutoPull.Cadence != "3h" {
		t.Fatalf("state not recorded: %+v", st.AutoPull)
	}
	if !strings.Contains(errBuf(a), "is on") || !strings.Contains(errBuf(a), "every 3 hours") {
		t.Fatalf("enable message: %q", errBuf(a))
	}

	// Status while enabled and installed: reports on, no divergence warning.
	a.Err = &bytes.Buffer{}
	if err := a.AutoPullStatus(false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errBuf(a), "on") || strings.Contains(errBuf(a), "missing") {
		t.Fatalf("status output: %q", errBuf(a))
	}

	// Simulate the OS job vanishing: status should warn about the divergence.
	fs.installed = false
	a.Err = &bytes.Buffer{}
	_ = a.AutoPullStatus(false)
	if !strings.Contains(errBuf(a), "missing") {
		t.Fatalf("expected divergence warning: %q", errBuf(a))
	}

	// Disable clears both the scheduler and the recorded state.
	fs.installed = true
	if err := a.AutoPullDisable(false); err != nil {
		t.Fatal(err)
	}
	if fs.enabled || fs.installed {
		t.Fatalf("scheduler still enabled: %+v", fs)
	}
	if !strings.Contains(errBuf(a), "is off") {
		t.Fatalf("disable message: %q", errBuf(a))
	}
	st, _ = a.Paths.LoadState()
	if st.AutoPull == nil || st.AutoPull.Enabled {
		t.Fatalf("state not disabled: %+v", st.AutoPull)
	}
}

func TestAutoPullStatusJSON(t *testing.T) {
	a := newTestApp(t, &fakeFetcher{})
	a.Sched = newFakeScheduler()
	if err := a.AutoPullEnable(Cadence{1, "day"}, true); err != nil {
		t.Fatal(err)
	}
	out := outBuf(a)
	for _, want := range []string{`"enabled":true`, `"cadence":"1d"`, `"intervalSeconds":86400`, `"installed":true`, `"supported":true`} {
		if !strings.Contains(out, want) {
			t.Fatalf("json missing %q: %s", want, out)
		}
	}
}

func TestAutoPullUnsupported(t *testing.T) {
	a := newTestApp(t, &fakeFetcher{})
	fs := newFakeScheduler()
	fs.supported = false
	fs.kind = "linux"
	a.Sched = fs

	// enable surfaces the scheduler's error (here, our fake succeeds, so assert
	// the message path via status instead).
	if err := a.AutoPullStatus(false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errBuf(a), "requires macOS") {
		t.Fatalf("expected unsupported message: %q", errBuf(a))
	}
}
