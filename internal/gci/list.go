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
func (a *App) RenderList(asJSON bool) error {
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

	// Right-align numeric/time columns: pad on the left to the column width,
	// measuring display width so it is correct before color is applied.
	rightAlign := tableprinter.WithPadding(func(width int, s string) string {
		if pad := width - text.DisplayWidth(s); pad > 0 {
			return strings.Repeat(" ", pad) + s
		}
		return s
	})

	tp := tableprinter.New(a.Out, isTTY, w)
	if isTTY {
		header := func(s string) string { return cs.Header(s) }
		tp.AddField("ID", tableprinter.WithColor(header))
		tp.AddField("REPO", tableprinter.WithColor(header))
		tp.AddField("REF", tableprinter.WithColor(header))
		tp.AddField("SHA", tableprinter.WithColor(header))
		tp.AddField("PULLED", tableprinter.WithColor(header), rightAlign)
		tp.AddField("FILES", tableprinter.WithColor(header), rightAlign)
		tp.EndRow()
	}
	for _, r := range rows {
		tp.AddField(r.ID, tableprinter.WithColor(cs.Cyan))
		tp.AddField(r.Repo)
		tp.AddField(refCol(r.Ref), tableprinter.WithColor(cs.Gray))
		tp.AddField(shaCol(r.SHA), tableprinter.WithColor(cs.Gray))
		tp.AddField(pulledCol(r.PulledAt, isTTY), tableprinter.WithColor(cs.Gray), rightAlign)
		tp.AddField(fmt.Sprintf("%d", r.Files), tableprinter.WithColor(cs.Gray), rightAlign)
		tp.EndRow()
	}
	return tp.Render()
}

type listJSONItem struct {
	ID       string `json:"id"`
	Repo     string `json:"repo"`
	Ref      string `json:"ref"`
	SHA      string `json:"sha"`
	PulledAt string `json:"pulledAt"`
	Files    int    `json:"files"`
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
			PulledAt: pulled, Files: r.Files,
		})
	}
	enc := json.NewEncoder(a.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(items)
}

func refCol(ref string) string {
	if ref == "" {
		return "-"
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
