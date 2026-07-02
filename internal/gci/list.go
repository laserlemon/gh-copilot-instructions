package gci

import (
	"bytes"
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
// <state>  SLUG  REPOSITORY  REF  SHA  FILES  PULLED
//
// The leading state column is an icon on a TTY (↗ moved / ✓ unchanged / • pending
// / × failed) and the state word when piped. SLUG is cyan; REPOSITORY/REF/SHA/FILES
// use the default foreground (REF and SHA show a muted gray "-" when absent; an
// updated SHA is italic); PULLED is muted gray. FILES is right-aligned. Headers
// are underlined gray.
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
		a.blank()
		a.dim("Add a source: gh copilot-instructions add <owner/repo>")
		return nil
	}
	return a.renderTable(a.Out, staticViews(rows), isTTY, w, cs)
}

// rowView is one row's render input. By default a row renders in full color; the
// flags drive the `add`/`pull` variations:
//   - dim: render the ENTIRE row, including its state icon, in muted gray (the
//     non-target rows during `add`); the icon keeps its glyph, just gray.
//   - loading: animate this row - a yellow spinner in the state cell and an
//     elapsed timer in PULLED; FILES is read live from the Row.
//   - pending: this row is queued for pull (sequential pull) - show the yellow
//     "•" pending icon, keeping its current data.
//   - updated: an existing source's commit moved this pull - show the ↗ icon and
//     render the (settled) SHA italic.
//   - shaResolved: the new SHA has plopped in (a loading row shows the previous
//     SHA in gray until this becomes true, then the new SHA in white).
//
// In-flight rows (loading or pending) render the SHA and FILES gray until the
// row finishes, when they settle to the default foreground.
type rowView struct {
	Row
	dim         bool
	loading     bool
	pending     bool
	spinner     string
	elapsed     string
	updated     bool
	shaResolved bool
}

// staticViews wraps rows as plain full-color views (the `list` table).
func staticViews(rows []Row) []rowView {
	views := make([]rowView, len(rows))
	for i, r := range rows {
		views[i] = rowView{Row: r}
	}
	return views
}

// renderTable writes the list table (one rowView per row) to w following gh
// tableprinter conventions: a TTY gets an aligned, headed, colorized table; a
// pipe gets headerless TSV.
func (a *App) renderTable(w io.Writer, views []rowView, isTTY bool, width int, cs *ColorScheme) error {
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
		tp.AddField("SLUG", tableprinter.WithColor(cs.Header), padRight)
		tp.AddField("REPOSITORY", tableprinter.WithColor(cs.Header), padRight)
		tp.AddField("REF", tableprinter.WithColor(cs.Header), padRight)
		tp.AddField("SHA", tableprinter.WithColor(cs.Header), padRight)
		tp.AddField("FILES", tableprinter.WithColor(cs.Header), padLeft)
		tp.AddField("PULLED", tableprinter.WithColor(cs.Header), padRight)
		tp.EndRow()
	}
	for _, v := range views {
		r := v.Row

		// addCell adds a dimmable cell: color is the normal color func (nil =
		// default foreground); a dim row overrides every cell to gray.
		addCell := func(s string, color func(string) string) {
			if v.dim {
				color = cs.Gray
			}
			if color != nil {
				tp.AddField(s, tableprinter.WithColor(color))
			} else {
				tp.AddField(s)
			}
		}

		// State icon: yellow spinner while loading, the yellow "•" while queued,
		// else the semantic final icon (↗ moved / ✓ unchanged / • pending / ×
		// failed). A dimmed row keeps the same glyph but renders it gray like the
		// rest of the row.
		if isTTY {
			var glyph string
			var color func(string) string
			switch {
			case v.loading:
				glyph, color = v.spinner, cs.Yellow // matches the pending dot
			case v.pending:
				glyph, color = stateIcon(StatePending, cs)
			case r.State == StatePulled && v.updated:
				glyph, color = iconMoved, cs.Green
			default:
				glyph, color = stateIcon(r.State, cs)
			}
			if v.dim {
				color = cs.Gray
			}
			tp.AddField(glyph, tableprinter.WithColor(color))
		} else {
			tp.AddField(r.State)
		}

		addCell(r.ID, cs.Cyan)
		addCell(r.Repo, nil)
		if r.Ref == "" {
			addCell("-", cs.Gray) // default branch
		} else {
			addCell(refDisplay(r.Ref), nil)
		}

		// SHA: while a row is in flight it shows the PREVIOUS SHA in gray until
		// the new one resolves ("plops in"), then the new SHA in white; a settled
		// row is italic when the commit moved, plain otherwise; "-" (gray) when
		// there's no SHA at all.
		inFlight := v.loading || v.pending
		shaText := shaCol(r.SHA)
		if inFlight {
			shaText = reserveWidth(shaText, shaDisplayWidth)
		}
		switch {
		case r.SHA == "":
			addCell(shaText, cs.Gray)
		case inFlight && !v.shaResolved:
			addCell(shaText, cs.Gray) // previous SHA, new one not in yet
		case inFlight:
			addCell(shaText, nil) // new SHA resolved -> white
		case v.updated:
			addCell(shaText, cs.Italic)
		default:
			addCell(shaText, nil)
		}

		// FILES: right-aligned; gray while in flight (or dimmed), white once the
		// row has finished.
		filesText := fmt.Sprintf("%d", r.Files)
		if v.dim || inFlight {
			tp.AddField(filesText, tableprinter.WithColor(cs.Gray), padLeft)
		} else {
			tp.AddField(filesText, padLeft)
		}

		if v.loading {
			addCell(v.elapsed, cs.Gray)
		} else {
			addCell(pulledCol(r.PulledAt, isTTY), cs.Gray)
		}
		tp.EndRow()
	}
	return tp.Render()
}

// renderViews renders views to a slice of lines (one per terminal row), for the
// animated table.
func (a *App) renderViews(views []rowView, width int, cs *ColorScheme) []string {
	var buf bytes.Buffer
	_ = a.renderTable(&buf, views, true, width, cs)
	return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
}

// sourceJSON is one source's JSON representation, shared by every command's
// --json output (field order matches the TTY/TSV columns). State is uppercase
// on every surface (table, TSV, JSON), matching gh: list reports
// PULLED/PENDING/FAILED; pull/add also report UPDATED.
type sourceJSON struct {
	State    string `json:"state"`
	ID       string `json:"slug"`
	Repo     string `json:"repository"`
	Ref      string `json:"ref"`
	SHA      string `json:"sha"`
	Files    int    `json:"fileCount"`
	PulledAt string `json:"pulledAt"`
}

func (a *App) renderListJSON(rows []Row) error {
	return a.renderListJSONUpdated(rows, nil)
}

// renderListJSONUpdated emits the full source list as JSON; any source id in
// `updated` whose state is PULLED is reported as UPDATED (its commit moved this
// run), preserving the "what changed" signal while every command returns the
// current list of sources.
func (a *App) renderListJSONUpdated(rows []Row, updated map[string]bool) error {
	items := make([]sourceJSON, 0, len(rows))
	for _, r := range rows {
		pulled := ""
		if !r.PulledAt.IsZero() {
			pulled = r.PulledAt.Format(time.RFC3339)
		}
		state := r.State
		if updated[r.ID] && state == StatePulled {
			state = "UPDATED"
		}
		items = append(items, sourceJSON{
			State: state, ID: r.ID, Repo: r.Repo, Ref: refJSON(r.Ref),
			SHA: r.SHA, Files: r.Files, PulledAt: pulled,
		})
	}
	return a.writeJSON(items)
}

// renderRaw prints the configured sources in config-file format - one canonical
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

// State-column icons (single cell each).
const (
	iconPulled  = "✓" // pulled, commit unchanged ("we're good")
	iconMoved   = "↗" // pulled, commit advanced to a new SHA
	iconFailed  = "✗" // pull failed or matched no files
	iconPending = "•" // configured but not yet pulled / queued
)

// stateIcon maps a source state to a gh-checks-style icon and its color. The
// PULLED icon here is the "unchanged" ✓; a moved pull is rendered separately
// with iconMoved (see renderTable).
func stateIcon(state string, cs *ColorScheme) (string, func(string) string) {
	switch state {
	case StatePulled:
		return iconPulled, cs.Green
	case StateFailed:
		return iconFailed, cs.Red
	default: // StatePending
		return iconPending, cs.Yellow
	}
}

// refDisplay shows a branch/tag in full, but abbreviates a pinned commit SHA.
func refDisplay(ref string) string {
	if isFullSHA(ref) {
		return short(ref)
	}
	return ref
}

// refJSON renders a source's ref for JSON output: the configured ref, or "-" for
// the default branch. GitHub blob URLs accept "-" in place of a ref and redirect
// to the default branch (e.g. github.com/owner/repo/blob/-/path), so "-" still
// composes into a working URL.
func refJSON(ref string) string {
	if ref == "" {
		return "-"
	}
	return ref
}

// shaDisplayWidth is the display width of an abbreviated SHA (see short()).
const shaDisplayWidth = 8

func shaCol(sha string) string {
	if sha == "" {
		return "~" // null SHA (distinct from the ref column's "-" default-branch marker)
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
		return "~" // never pulled (null), matching the SHA column's "~"
	}
	if isTTY {
		return text.RelativeTimeAgo(time.Now(), t)
	}
	return t.Format(time.RFC3339)
}
