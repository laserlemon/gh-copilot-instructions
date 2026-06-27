package gci

import (
	"strings"
	"testing"
)

func TestColorSchemeDisabledIsPlain(t *testing.T) {
	cs := &ColorScheme{enabled: false}
	for _, got := range []string{
		cs.Bold("x"), cs.Red("x"), cs.Green("x"), cs.Yellow("x"),
		cs.Cyan("x"), cs.Magenta("x"), cs.Gray("x"), cs.Header("x"),
		cs.SuccessIcon(), cs.WarningIcon(), cs.FailureIcon(),
	} {
		if strings.Contains(got, "\x1b") {
			t.Errorf("disabled scheme emitted ANSI: %q", got)
		}
	}
}

func TestColorSchemeUsesBasic16Palette(t *testing.T) {
	cs := &ColorScheme{enabled: true}
	// Only basic 16-color codes (3x/9x); never 256-color (38;5;) or truecolor (38;2;).
	for name, got := range map[string]string{
		"green":  cs.Green("ok"),
		"gray":   cs.Gray("g"),
		"cyan":   cs.Cyan("c"),
		"header": cs.Header("H"),
	} {
		if strings.Contains(got, "38;5;") || strings.Contains(got, "38;2;") {
			t.Errorf("%s used an extended-palette code: %q", name, got)
		}
		if !strings.HasSuffix(got, "\x1b[0m") {
			t.Errorf("%s not reset-terminated: %q", name, got)
		}
	}
	if got := cs.Gray("g"); !strings.HasPrefix(got, "\x1b[90m") {
		t.Errorf("gray should use bright-black (90): %q", got)
	}
	if got := cs.Header("H"); !strings.HasPrefix(got, "\x1b[4m") {
		t.Errorf("header should be underlined (4): %q", got)
	}
	// Empty strings are never wrapped (avoids stray escapes in padding).
	if got := cs.Green(""); got != "" {
		t.Errorf("empty string should not be wrapped: %q", got)
	}
}
