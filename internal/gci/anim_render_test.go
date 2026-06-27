package gci

import (
	"strings"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/text"
)

// renderOne renders a single rowView and returns its (last) data line.
func renderOne(a *App, cs *ColorScheme, v rowView) string {
	lines := a.renderViews([]rowView{v}, 100, cs)
	return lines[len(lines)-1]
}

// TestAnimatedRowRender verifies a loading row shows the spinner glyph, the live
// SHA (abbreviated), the running file count, and the elapsed timer.
func TestAnimatedRowRender(t *testing.T) {
	a := &App{}
	cs := &ColorScheme{enabled: false} // no color codes, easy to assert
	v := rowView{
		Row:     Row{State: StatePending, ID: "abc12345", Repo: "o/r", SHA: "6de16bae1db0345805fe3399b45c1fdfdeb02544", Files: 47},
		loading: true, spinner: spinnerFrames[2], elapsed: "3s",
	}
	last := renderOne(a, cs, v)

	for _, want := range []string{spinnerFrames[2], "abc12345", "o/r", "6de16bae", "47", "3s"} {
		if !strings.Contains(last, want) {
			t.Errorf("animated row %q missing %q", last, want)
		}
	}
	// SHA must be the 8-char abbreviation, not the full 40.
	if strings.Contains(last, "6de16bae1db0") {
		t.Errorf("SHA not abbreviated in %q", last)
	}
}

// TestSHAColor verifies the SHA cell colors: gray "-" placeholder, green when
// changed, default (no wrap) when unchanged.
func TestSHAColor(t *testing.T) {
	a := &App{}
	cs := &ColorScheme{enabled: true}
	base := Row{State: StatePending, ID: "abc12345", Repo: "o/r", Files: 1}
	full := "6de16bae1db0345805fe3399b45c1fdfdeb02544"

	empty := renderOne(a, cs, rowView{Row: base, loading: true, spinner: spinnerFrames[0], elapsed: "0s"})
	if !strings.Contains(empty, ansiGray+"-") {
		t.Errorf("empty SHA placeholder not gray: %q", empty)
	}

	r := base
	r.SHA = full
	changed := renderOne(a, cs, rowView{Row: r, loading: true, spinner: spinnerFrames[0], elapsed: "0s", shaChanged: true})
	if !strings.Contains(changed, ansiGreen+"6de16bae") {
		t.Errorf("changed SHA should be green: %q", changed)
	}

	unchanged := renderOne(a, cs, rowView{Row: r, loading: true, spinner: spinnerFrames[0], elapsed: "0s", shaChanged: false})
	if strings.Contains(unchanged, ansiGreen+"6de16bae") || strings.Contains(unchanged, ansiGray+"6de16bae") {
		t.Errorf("unchanged SHA should be default-colored: %q", unchanged)
	}
	if !strings.Contains(unchanged, "6de16bae") {
		t.Errorf("SHA missing: %q", unchanged)
	}
}

// TestDimRow verifies a dimmed row renders every cell gray EXCEPT the state icon,
// which keeps its semantic color.
func TestDimRow(t *testing.T) {
	a := &App{}
	cs := &ColorScheme{enabled: true}
	v := rowView{Row: Row{State: StatePulled, ID: "abc12345", Repo: "o/r",
		SHA: "6de16bae1db0345805fe3399b45c1fdfdeb02544", Files: 3, PulledAt: time.Now()}, dim: true}
	last := renderOne(a, cs, v)

	// ID is gray, not cyan.
	if !strings.Contains(last, ansiGray+"abc12345") {
		t.Errorf("dim ID not gray: %q", last)
	}
	if strings.Contains(last, ansiCyan+"abc12345") {
		t.Errorf("dim ID should not be cyan: %q", last)
	}
	// The state icon keeps its color (PULLED => green ✓).
	if !strings.Contains(last, ansiGreen+"✓") {
		t.Errorf("dim row should keep its colored state icon: %q", last)
	}
}

// TestAnimatedSHAReservesWidthSingleRow verifies that, when the in-progress row
// is the only row, the SHA column is the same width before and after the SHA
// fills in — so populating it doesn't shift the FILES/PULLED columns.
func TestAnimatedSHAReservesWidthSingleRow(t *testing.T) {
	a := &App{}
	cs := &ColorScheme{enabled: false}
	base := Row{State: StatePending, ID: "abc12345", Repo: "o/r", Files: 5}

	empty := renderOne(a, cs, rowView{Row: base, loading: true, spinner: spinnerFrames[0], elapsed: "3s"})
	filled := base
	filled.SHA = "6de16bae1db0345805fe3399b45c1fdfdeb02544"
	full := renderOne(a, cs, rowView{Row: filled, loading: true, spinner: spinnerFrames[0], elapsed: "3s"})

	if we, wf := text.DisplayWidth(empty), text.DisplayWidth(full); we != wf {
		t.Errorf("row width changed when SHA filled: empty=%d filled=%d\n empty=%q\n filled=%q", we, wf, empty, full)
	}
}
