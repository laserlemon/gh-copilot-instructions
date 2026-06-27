package gci

import (
	"strings"
	"testing"

	"github.com/cli/go-gh/v2/pkg/text"
)

// TestAnimatedRowRender verifies the in-progress row shows the spinner glyph,
// the live SHA (filled in), the running file count, and the ellipsis — proving
// the animation surfaces real data (independent of any terminal capture).
func TestAnimatedRowRender(t *testing.T) {
	a := &App{}
	cs := &ColorScheme{enabled: false} // no color codes, easy to assert
	rows := []Row{{
		State: StatePending, ID: "abc12345", Repo: "o/r", Ref: "",
		SHA: "6de16bae1db0345805fe3399b45c1fdfdeb02544", Files: 47,
	}}
	anim := &rowAnim{idx: 0, stateCell: spinnerFrames[2], pulledCell: "3s"}
	lines := a.tableLines(rows, 100, cs, anim)
	last := lines[len(lines)-1]

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

// TestAnimatedSHAPlaceholderColor verifies the SHA cell is gray while it's the
// "-" placeholder and uncolored (default foreground) once the real SHA fills in.
func TestAnimatedSHAPlaceholderColor(t *testing.T) {
	a := &App{}
	cs := &ColorScheme{enabled: true} // colors on, so we can see the wrapping
	anim := &rowAnim{idx: 0, stateCell: spinnerFrames[0], pulledCell: "3s"}

	empty := []Row{{State: StatePending, ID: "abc12345", Repo: "o/r", Files: 0}}
	filled := []Row{{State: StatePending, ID: "abc12345", Repo: "o/r", Files: 9,
		SHA: "6de16bae1db0345805fe3399b45c1fdfdeb02544"}}

	el := a.tableLines(empty, 100, cs, anim)
	fl := a.tableLines(filled, 100, cs, anim)
	le := el[len(el)-1]
	lf := fl[len(fl)-1]

	// Placeholder dash is wrapped in gray.
	if !strings.Contains(le, ansiGray+"-") {
		t.Errorf("empty SHA placeholder not gray: %q", le)
	}
	// The real SHA is not preceded by the gray code.
	if strings.Contains(lf, ansiGray+"6de16bae") {
		t.Errorf("populated SHA should be default-colored, not gray: %q", lf)
	}
	if !strings.Contains(lf, "6de16bae") {
		t.Errorf("populated SHA missing: %q", lf)
	}
}
func TestAnimatedSHAReservesWidthSingleRow(t *testing.T) {
	a := &App{}
	cs := &ColorScheme{enabled: false}
	base := Row{State: StatePending, ID: "abc12345", Repo: "o/r", Files: 5}
	anim := &rowAnim{idx: 0, stateCell: spinnerFrames[0], pulledCell: "3s"}

	empty := base // spinning: SHA not yet resolved
	filled := base
	filled.SHA = "6de16bae1db0345805fe3399b45c1fdfdeb02544"

	le := a.tableLines([]Row{empty}, 100, cs, anim)
	lf := a.tableLines([]Row{filled}, 100, cs, anim)

	// Same overall row width => no column shift when the SHA appears.
	if we, wf := text.DisplayWidth(le[len(le)-1]), text.DisplayWidth(lf[len(lf)-1]); we != wf {
		t.Errorf("row width changed when SHA filled: empty=%d filled=%d\n empty=%q\n filled=%q", we, wf, le[len(le)-1], lf[len(lf)-1])
	}
}
