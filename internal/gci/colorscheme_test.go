package gci

import (
	"strings"
	"testing"
)

func TestColorSchemeDisabledIsPlain(t *testing.T) {
	cs := &ColorScheme{enabled: false}
	for _, got := range []string{
		cs.Bold("x"), cs.Red("x"), cs.Green("x"), cs.Yellow("x"),
		cs.Cyan("x"), cs.Magenta("x"), cs.Gray("x"), cs.GreenBold("x"),
		cs.SuccessIcon(), cs.WarningIcon(), cs.FailureIcon(),
	} {
		if strings.Contains(got, "\x1b") {
			t.Errorf("disabled scheme emitted ANSI: %q", got)
		}
	}
}

func TestColorSchemeEnabledWraps(t *testing.T) {
	cs := &ColorScheme{enabled: true, has256: true}
	if got := cs.Green("ok"); !strings.HasPrefix(got, "\x1b[32m") || !strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("Green wrap wrong: %q", got)
	}
	if got := cs.Gray("g"); !strings.Contains(got, "38;5;242") {
		t.Errorf("256-color gray expected: %q", got)
	}
	if got := (&ColorScheme{enabled: true, has256: false}).Gray("g"); !strings.Contains(got, "\x1b[90m") {
		t.Errorf("16-color gray fallback expected: %q", got)
	}
	// Empty strings are never wrapped (avoids stray escapes in padding).
	if got := cs.Green(""); got != "" {
		t.Errorf("empty string should not be wrapped: %q", got)
	}
}
