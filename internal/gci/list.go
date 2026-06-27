package gci

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cli/go-gh/v2/pkg/tableprinter"
	"github.com/cli/go-gh/v2/pkg/term"
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

	if len(rows) == 0 {
		a.msg("No sources configured (%s).", origin)
		a.msg("Add one with: gh copilot-instructions add <owner/repo[:path]>")
		return nil
	}

	tp := tableprinter.New(a.Out, isTTY, w)
	if isTTY {
		for _, h := range []string{"ID", "REPO", "REF", "SHA", "PULLED", "TOKEN", "FILES"} {
			tp.AddField(h)
		}
		tp.EndRow()
	}
	for _, r := range rows {
		tp.AddField(r.ID)
		tp.AddField(r.Repo)
		tp.AddField(refOrDefault(r.Ref))
		tp.AddField(shaCol(r.SHA))
		tp.AddField(pulledCol(r.PulledAt, isTTY))
		tp.AddField(tokenCol(r.HasToken))
		tp.AddField(fmt.Sprintf("%d", r.Files))
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
	HasToken bool   `json:"hasToken"`
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
			PulledAt: pulled, HasToken: r.HasToken, Files: r.Files,
		})
	}
	enc := json.NewEncoder(a.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(items)
}

func refOrDefault(ref string) string {
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

func tokenCol(has bool) string {
	if has {
		return "yes"
	}
	return "-"
}

func pulledCol(t time.Time, isTTY bool) string {
	if t.IsZero() {
		return "-"
	}
	if isTTY {
		return humanizeSince(time.Since(t))
	}
	return t.Format(time.RFC3339)
}

func humanizeSince(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
