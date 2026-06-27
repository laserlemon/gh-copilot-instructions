package gci

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/tableprinter"
	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/cli/go-gh/v2/pkg/text"
)

// RenderList prints the list rows following gh tableprinter conventions:
// a TTY gets an aligned, headed, colorized table; a pipe gets headerless TSV.
//
// Columns (TTY/TSV/JSON share this order):
//
// <state>  ID  REPOSITORY  REF  SHA  FILES  PULLED
//
// The leading state column is an icon on a TTY (✓ pulled / - pending / ✗ failed)
// and the state word when piped. ID is cyan; REPOSITORY/REF/SHA/FILES use the default
// foreground (REF is muted gray for "(default)"); PULLED is muted gray. FILES is
// right-aligned. Headers are underlined gray.
func (a *App) RenderList(asJSON, raw bool) error {
	if raw {
		return a.renderRaw()
	}
	rows, origin, err := a.ListRows()
	if err != nil {
		return err
	}
	if asJSON {
		return a.renderListJSON(rows)
	}

	t := term.FromEnv()
	isTTY := t.IsTerminalOutput()
	w, _, _ := t.Size()
	if w <= 0 {
		w = 80
	}
	cs := &ColorScheme{enabled: t.IsColorEnabled()}

	if len(rows) == 0 {
		a.dim("No sources configured (%s).", origin)
		a.dim("Add one with: gh copilot-instructions add <owner/repo[:path]>")
		return nil
	}
	return a.renderTable(a.Out, rows, isTTY, w, cs, nil)
}

// rowAnim describes the in-progress row during `add`: its index plus the strings
// to show in the state cell (a spinner frame) and the PULLED cell (an animated
// ellipsis). The SHA and FILES cells are read from the Row itself, so they fill
// in live as the pull reports them.
type rowAnim struct {
	idx        int
	stateCell  string
	pulledCell string
}

// renderTable writes the list table to w. When anim is non-nil, the row at
// anim.idx shows anim.stateCell (a spinner) in its state column and
// anim.pulledCell (an ellipsis) in its PULLED column instead of the usual icon
// and relative time; this is how `add` animates the in-progress row. anim == nil
// renders every row normally.
func (a *App) renderTable(w io.Writer, rows []Row, isTTY bool, width int, cs *ColorScheme, anim *rowAnim) error {
	// Pad headers to full column width so their underline spans the whole
	// column (including the last column); right-align numeric columns.
	padRight := tableprinter.WithPadding(text.PadRight)
	padLeft := tableprinter.WithPadding(func(width int, s string) string {
		if n := width - text.DisplayWidth(s); n > 0 {
			return strings.Repeat(" ", n) + s
		}
		return s
	})

	tp := tableprinter.New(w, isTTY, width)
	if isTTY {
		// Empty state-column header: the tableprinter pads it to the column
		// width (one space) and the color underlines it, giving a single-space
		// underline like `gh pr checks`.
		tp.AddField("", tableprinter.WithColor(cs.Header))
		tp.AddField("ID", tableprinter.WithColor(cs.Header), padRight)
		tp.AddField("REPOSITORY", tableprinter.WithColor(cs.Header), padRight)
		tp.AddField("REF", tableprinter.WithColor(cs.Header), padRight)
		tp.AddField("SHA", tableprinter.WithColor(cs.Header), padRight)
		tp.AddField("FILES", tableprinter.WithColor(cs.Header), padLeft)
		tp.AddField("PULLED", tableprinter.WithColor(cs.Header), padRight)
		tp.EndRow()
	}
	for i, r := range rows {
		animated := anim != nil && i == anim.idx
		if isTTY {
			if animated {
				tp.AddField(anim.stateCell, tableprinter.WithColor(cs.Cyan)) // gh's cyan spinner
			} else {
				icon, color := stateIcon(r.State, cs)
				tp.AddField(icon, tableprinter.WithColor(color))
			}
		} else {
			tp.AddField(r.State)
		}
		tp.AddField(r.ID, tableprinter.WithColor(cs.Cyan))
		tp.AddField(r.Repo)
		if r.Ref == "" {
			tp.AddField("(default)", tableprinter.WithColor(cs.Gray))
		} else {
			tp.AddField(refDisplay(r.Ref))
		}
		// SHA: a gray "-" placeholder until it's known, then the SHA itself in
		// the default foreground once populated. During the `add` animation the
		// cell also reserves the full SHA width so it doesn't shift when it fills.
		shaText := shaCol(r.SHA)
		if animated {
			shaText = reserveWidth(shaText, shaDisplayWidth)
		}
		if r.SHA == "" {
			tp.AddField(shaText, tableprinter.WithColor(cs.Gray))
		} else {
			tp.AddField(shaText)
		}
		tp.AddField(fmt.Sprintf("%d", r.Files), padLeft)
		if animated {
			tp.AddField(anim.pulledCell, tableprinter.WithColor(cs.Gray))
		} else {
			tp.AddField(pulledCol(r.PulledAt, isTTY), tableprinter.WithColor(cs.Gray))
		}
		tp.EndRow()
	}
	return tp.Render()
}

// listJSONItem field order matches the TTY/TSV column order.
type listJSONItem struct {
	State    string `json:"state"`
	ID       string `json:"id"`
	Repo     string `json:"repo"`
	Ref      string `json:"ref"`
	SHA      string `json:"sha"`
	Files    int    `json:"files"`
	PulledAt string `json:"pulledAt"`
}

func (a *App) renderListJSON(rows []Row) error {
	items := make([]listJSONItem, 0, len(rows))
	for _, r := range rows {
		pulled := ""
		if !r.PulledAt.IsZero() {
			pulled = r.PulledAt.Format(time.RFC3339)
		}
		items = append(items, listJSONItem{
			State: r.State, ID: r.ID, Repo: r.Repo, Ref: r.Ref, SHA: r.SHA,
			Files: r.Files, PulledAt: pulled,
		})
	}
	enc := json.NewEncoder(a.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(items)
}

// renderRaw prints the configured sources in config-file format — one canonical
// line per source (`owner/repo[@ref][:path]  [token]`), no header, color, or
// comments. This is the value to paste into a multiline GH_COPILOT_INSTRUCTIONS
// Codespaces secret. Inline tokens are included so private sources work where
// your gh auth isn't available; sources without an inline token are emitted as
// the bare spec (add a token for any private repo before using it in Codespaces).
func (a *App) renderRaw() error {
	srcs, _, err := a.Paths.LoadSources()
	if err != nil {
		a.msg("%v", err)
	}
	for _, s := range srcs {
		fmt.Fprintln(a.Out, s.Line())
	}
	return nil
}

// stateIcon maps a source state to a gh-checks-style icon and its color:
// ✓ (green) pulled, • (yellow) pending, × (red) failed.
func stateIcon(state string, cs *ColorScheme) (string, func(string) string) {
	switch state {
	case StatePulled:
		return "✓", cs.Green
	case StateFailed:
		return "×", cs.Red
	default: // StatePending
		return "•", cs.Yellow
	}
}

// refDisplay shows a branch/tag in full, but abbreviates a pinned commit SHA.
func refDisplay(ref string) string {
	if isFullSHA(ref) {
		return short(ref)
	}
	return ref
}

// shaDisplayWidth is the display width of an abbreviated SHA (see short()).
const shaDisplayWidth = 8

func shaCol(sha string) string {
	if sha == "" {
		return "-"
	}
	return short(sha)
}

// reserveWidth right-pads s with spaces to at least w display columns (left
// alignment, matching the SHA column), so a cell can hold its eventual width
// before its content arrives.
func reserveWidth(s string, w int) string {
	if n := w - text.DisplayWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func pulledCol(t time.Time, isTTY bool) string {
	if t.IsZero() {
		return "-"
	}
	if isTTY {
		return text.RelativeTimeAgo(time.Now(), t)
	}
	return t.Format(time.RFC3339)
}
