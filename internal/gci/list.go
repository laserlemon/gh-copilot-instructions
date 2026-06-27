package gci

import (
	"encoding/json"
	"fmt"
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
// <state>  ID  REPO  REF  SHA  FILES  PULLED
//
// The leading state column is an icon on a TTY (✓ pulled / - pending / ✗ failed)
// and the state word when piped. ID is cyan; REPO/REF/SHA/FILES use the default
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

	// Pad headers to full column width so their underline spans the whole
	// column (including the last column); right-align numeric columns.
	padRight := tableprinter.WithPadding(text.PadRight)
	padLeft := tableprinter.WithPadding(func(width int, s string) string {
		if n := width - text.DisplayWidth(s); n > 0 {
			return strings.Repeat(" ", n) + s
		}
		return s
	})

	tp := tableprinter.New(a.Out, isTTY, w)
	if isTTY {
		// Empty state-column header: the tableprinter pads it to the column
		// width (one space) and the color underlines it, giving a single-space
		// underline like `gh pr checks`.
		tp.AddField("", tableprinter.WithColor(cs.Header))
		tp.AddField("ID", tableprinter.WithColor(cs.Header), padRight)
		tp.AddField("REPO", tableprinter.WithColor(cs.Header), padRight)
		tp.AddField("REF", tableprinter.WithColor(cs.Header), padRight)
		tp.AddField("SHA", tableprinter.WithColor(cs.Header), padRight)
		tp.AddField("FILES", tableprinter.WithColor(cs.Header), padLeft)
		tp.AddField("PULLED", tableprinter.WithColor(cs.Header), padRight)
		tp.EndRow()
	}
	for _, r := range rows {
		if isTTY {
			icon, color := stateIcon(r.State, cs)
			tp.AddField(icon, tableprinter.WithColor(color))
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
		tp.AddField(shaCol(r.SHA))
		tp.AddField(fmt.Sprintf("%d", r.Files), padLeft)
		tp.AddField(pulledCol(r.PulledAt, isTTY), tableprinter.WithColor(cs.Gray))
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

func shaCol(sha string) string {
	if sha == "" {
		return "-"
	}
	return short(sha)
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
