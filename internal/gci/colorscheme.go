package gci

import (
	"os"

	"github.com/cli/go-gh/v2/pkg/term"
)

// ColorScheme is a small gh-style semantic palette. When disabled (piped output,
// NO_COLOR, etc.) every method returns its input unchanged, so non-terminal and
// test output stays plain.
type ColorScheme struct {
	enabled bool
	has256  bool
}

const (
	ansiReset   = "\x1b[0m"
	ansiBold    = "\x1b[1m"
	ansiRed     = "\x1b[31m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiBlue    = "\x1b[34m"
	ansiMagenta = "\x1b[35m"
	ansiCyan    = "\x1b[36m"
	ansiGray256 = "\x1b[38;5;242m"
	ansiGray16  = "\x1b[90m"
)

func (c *ColorScheme) wrap(code, s string) string {
	if !c.enabled || s == "" {
		return s
	}
	return code + s + ansiReset
}

func (c *ColorScheme) Bold(s string) string    { return c.wrap(ansiBold, s) }
func (c *ColorScheme) Red(s string) string     { return c.wrap(ansiRed, s) }
func (c *ColorScheme) Green(s string) string   { return c.wrap(ansiGreen, s) }
func (c *ColorScheme) Yellow(s string) string  { return c.wrap(ansiYellow, s) }
func (c *ColorScheme) Blue(s string) string    { return c.wrap(ansiBlue, s) }
func (c *ColorScheme) Magenta(s string) string { return c.wrap(ansiMagenta, s) }
func (c *ColorScheme) Cyan(s string) string    { return c.wrap(ansiCyan, s) }

// Gray renders secondary/muted text, preferring a 256-color gray.
func (c *ColorScheme) Gray(s string) string {
	if c.has256 {
		return c.wrap(ansiGray256, s)
	}
	return c.wrap(ansiGray16, s)
}

// GreenBold is used for success headers.
func (c *ColorScheme) GreenBold(s string) string {
	if !c.enabled || s == "" {
		return s
	}
	return ansiBold + ansiGreen + s + ansiReset
}

func (c *ColorScheme) SuccessIcon() string { return c.Green("✓") }
func (c *ColorScheme) WarningIcon() string { return c.Yellow("!") }
func (c *ColorScheme) FailureIcon() string { return c.Red("✗") }

// newSchemes builds color schemes for stdout and stderr, each gated on whether
// that stream is a color-capable terminal (honoring NO_COLOR/CLICOLOR/
// GH_FORCE_TTY via go-gh's term package).
func newSchemes() (out, err *ColorScheme) {
	t := term.FromEnv()
	has256 := t.Is256ColorSupported()
	outOn := t.IsColorEnabled()
	errOn := colorEnabledFor(os.Stderr)
	return &ColorScheme{enabled: outOn, has256: has256}, &ColorScheme{enabled: errOn, has256: has256}
}

// colorEnabledFor decides whether to emit color to a specific file (used for
// stderr, which go-gh's Term only evaluates for stdout).
func colorEnabledFor(f *os.File) bool {
	if term.IsColorDisabled() {
		return false
	}
	if term.IsColorForced() || os.Getenv("GH_FORCE_TTY") != "" {
		return true
	}
	return term.IsTerminal(f)
}
