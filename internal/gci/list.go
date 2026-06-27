package gci

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cli/go-gh/v2/pkg/tableprinter"
	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/cli/go-gh/v2/pkg/text"
)

// RenderList prints the list rows following gh tableprinter conventions:
// a TTY gets an aligned, headed, colorized table; a pipe gets headerless TSV.
//
// Columns (TTY and TSV share this order; JSON matches it too):
//
// # ID  REPO  REF  SHA  FILES  PULLED
//
// ID is accented (cyan); REPO/REF/SHA/FILES use the terminal's default
// foreground; PULLED is muted gray (matching the underlined gray headers).
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

	tp := tableprinter.New(a.Out, isTTY, w)
	if isTTY {
		for _, h := range []string{"ID", "REPO", "REF", "SHA", "FILES", "PULLED"} {
			tp.AddField(h, tableprinter.WithColor(cs.Header))
		}
		tp.EndRow()
	}
	for _, r := range rows {
		tp.AddField(r.ID, tableprinter.WithColor(cs.Cyan))
		tp.AddField(r.Repo)
		tp.AddField(refCol(r.Ref))
		tp.AddField(shaCol(r.SHA))
		tp.AddField(fmt.Sprintf("%d", r.Files))
		tp.AddField(pulledCol(r.PulledAt, isTTY), tableprinter.WithColor(cs.Gray))
		tp.EndRow()
	}
	return tp.Render()
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

// listJSONItem field order matches the TTY/TSV column order.
type listJSONItem struct {
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
			ID: r.ID, Repo: r.Repo, Ref: r.Ref, SHA: r.SHA,
			Files: r.Files, PulledAt: pulled,
		})
	}
	enc := json.NewEncoder(a.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(items)
}

// refCol shows "(default)" when no ref is pinned. A pull with no ref always
// follows the repo's current default branch, even if that branch changes.
func refCol(ref string) string {
	if ref == "" {
		return "(default)"
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
