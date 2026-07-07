package gci

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cli/go-gh/v2/pkg/tableprinter"
	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/cli/go-gh/v2/pkg/text"
)

// FileRow is one line of `file list` output: an installed instruction file, the
// source it came from, and where it lives locally and remotely.
type FileRow struct {
	Source string // the origin source's slug
	Repo   string // the origin source's repository
	Ref    string // the origin source's configured ref ("" => default branch)
	SHA    string // the commit the file was pulled from
	Remote string // the repo-relative path the file was pulled from
	Local  string // the absolute local install path
	URL    string // the file's GitHub blob URL (empty when the remote path is unknown)
}

// FileRows returns every instruction file installed from the configured sources,
// joined with the source it came from, sorted by (repository, source, remote).
// Files come from each source's recorded pull manifest (state), scoped to the
// sources currently configured.
func (a *App) FileRows() ([]FileRow, ConfigOrigin, error) {
	srcs, origin, err := a.Paths.LoadSources()
	if err != nil {
		a.msg("%v", err)
	}
	st, sErr := a.Paths.LoadState()
	if sErr != nil {
		return nil, origin, sErr
	}
	var rows []FileRow
	for _, s := range srcs {
		ss, ok := st.Sources[s.ID()]
		if !ok {
			continue
		}
		prefix := FileDir + "/" + s.ID() + "/"
		for _, f := range ss.Files {
			remote := ss.Remote[f]
			row := FileRow{
				Source: s.ID(),
				Repo:   s.Repo,
				Ref:    s.Ref,
				SHA:    ss.SHA,
				Remote: remote,
				Local:  filepath.Join(a.Paths.InstallDir, filepath.FromSlash(f)),
			}
			if remote == "" {
				// Pre-remote state: best-effort fall back to the installed name.
				row.Remote = strings.TrimPrefix(f, prefix)
			} else {
				row.URL = blobURL(s.Repo, blobRef(ss.SHA, s.Ref), remote)
			}
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Repo != rows[j].Repo {
			return rows[i].Repo < rows[j].Repo
		}
		if rows[i].Source != rows[j].Source {
			return rows[i].Source < rows[j].Source
		}
		return rows[i].Remote < rows[j].Remote
	})
	return rows, origin, nil
}

// blobRef picks the ref for a blob URL: the exact pulled commit SHA (a permalink)
// when known, else the configured ref, else the default-branch marker HEAD.
func blobRef(sha, ref string) string {
	switch {
	case sha != "":
		return sha
	case ref != "":
		return ref
	default:
		return "HEAD"
	}
}

// blobURL builds a github.com blob URL for a repo-relative path at a ref, with
// each path segment escaped.
func blobURL(repo, ref, remote string) string {
	segs := strings.Split(remote, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return fmt.Sprintf("https://github.com/%s/blob/%s/%s", repo, url.PathEscape(ref), strings.Join(segs, "/"))
}

// fileSourceJSON is the nested `source` object in file JSON: a small subset of
// the source list's fields (identity plus version) so each file carries its
// origin.
type fileSourceJSON struct {
	Slug string `json:"slug"`
	Repo string `json:"repository"`
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
}

// fileJSON is one file's JSON representation: its source (as a nested object),
// the repo-relative remote path, the absolute local path, and the blob URL.
type fileJSON struct {
	Source fileSourceJSON `json:"source"`
	Remote string         `json:"remotePath"`
	Local  string         `json:"localPath"`
	URL    string         `json:"url"`
}

// RenderFileList prints the installed instruction files following the same
// conventions as the source list: a TTY gets an aligned, headed, colorized
// table; a pipe gets headerless TSV; --json emits a (compact when piped) array.
func (a *App) RenderFileList(asJSON bool) error {
	rows, origin, err := a.FileRows()
	if err != nil {
		return err
	}
	if asJSON {
		items := make([]fileJSON, 0, len(rows))
		for _, r := range rows {
			items = append(items, fileJSON{
				Source: fileSourceJSON{Slug: r.Source, Repo: r.Repo, Ref: refJSON(r.Ref), SHA: r.SHA},
				Remote: r.Remote,
				Local:  r.Local,
				URL:    r.URL,
			})
		}
		return a.writeJSON(items)
	}

	t := term.FromEnv()
	isTTY := t.IsTerminalOutput()
	linkify := isTTY && t.IsColorEnabled()
	w, _, _ := t.Size()
	if w <= 0 {
		w = 80
	}
	cs := &ColorScheme{enabled: t.IsColorEnabled()}

	if len(rows) == 0 {
		srcs, _, _ := a.Paths.LoadSources()
		if origin == OriginNone || len(srcs) == 0 {
			a.note("No Copilot instructions sources added.")
			a.blank()
			a.dim("Add a source: gh copilot-instructions add <owner/repo>")
		} else {
			a.note("No instruction files pulled yet.")
			a.blank()
			a.dim("Pull your sources: gh copilot-instructions pull")
		}
		if isTTY {
			a.dim("See all commands: gh copilot-instructions --help")
		}
		return nil
	}

	a.renderFileTable(a.Out, rows, isTTY, linkify, w, cs)
	if isTTY {
		a.blank()
		a.dim("See all commands: gh copilot-instructions --help")
	}
	return nil
}

// renderFileTable writes the file table to w: a TTY gets aligned, underlined
// headers with a cyan SOURCE column (matching the source table) and a REMOTE
// PATH linked to its GitHub blob page; a pipe gets headerless tab-separated
// values (no color, no hyperlinks) with absolute local paths.
func (a *App) renderFileTable(w io.Writer, rows []FileRow, isTTY, linkify bool, width int, cs *ColorScheme) error {
	padRight := tableprinter.WithPadding(text.PadRight)
	tp := tableprinter.New(w, isTTY, width)
	if isTTY {
		tp.AddField("SOURCE", tableprinter.WithColor(cs.Header), padRight)
		tp.AddField("REMOTE PATH", tableprinter.WithColor(cs.Header), padRight)
		tp.AddField("LOCAL PATH", tableprinter.WithColor(cs.Header), padRight)
		tp.EndRow()
	}
	for _, r := range rows {
		if isTTY {
			tp.AddField(r.Source, tableprinter.WithColor(cs.Cyan))
		} else {
			tp.AddField(r.Source)
		}

		// REMOTE PATH: on a capable terminal, wrap the cell in an OSC 8 hyperlink
		// to the file's GitHub blob page. The link function runs after padding
		// and only in TTY mode, so alignment is preserved and piped output stays
		// a clean path.
		if linkify && r.URL != "" {
			link := r.URL
			tp.AddField(r.Remote, tableprinter.WithColor(func(s string) string { return linkPadded(link, s) }))
		} else {
			tp.AddField(r.Remote)
		}

		// LOCAL PATH: link to the file itself via a file:// URL so the link is
		// truncation-proof (the target is the full path even when the displayed
		// label is shortened), rather than relying on the terminal to auto-detect
		// the path text. The home directory is abbreviated to ~ on a terminal;
		// piped output keeps the absolute path and no link.
		if linkify {
			target := fileURL(r.Local)
			label := abbrevHome(r.Local)
			tp.AddField(label, tableprinter.WithColor(func(s string) string { return linkPadded(target, s) }))
		} else {
			tp.AddField(r.Local)
		}
		tp.EndRow()
	}
	return tp.Render()
}

// fileURL builds a file:// URL for an absolute local path, escaping each segment.
func fileURL(abs string) string {
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // Windows drive paths (C:/...) become file:///C:/...
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "file://" + strings.Join(segs, "/")
}

// hyperlink wraps label in an OSC 8 terminal hyperlink pointing at link.
func hyperlink(link, label string) string {
	return "\x1b]8;;" + link + "\x1b\\" + label + "\x1b]8;;\x1b\\"
}

// linkPadded hyperlinks a padded table cell, linking only its visible text and
// leaving any trailing space padding outside the link, so the padding isn't
// underlined/styled as part of the link.
func linkPadded(link, padded string) string {
	trimmed := strings.TrimRight(padded, " ")
	return hyperlink(link, trimmed) + padded[len(trimmed):]
}

// abbrevHome replaces a leading home directory with "~".
func abbrevHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}
