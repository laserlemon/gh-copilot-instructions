# gh-copilot-instructions

A [`gh` CLI](https://cli.github.com) extension that **pulls your Copilot custom instructions** from
one or more repos into `~/.copilot/instructions/` — the single user-level location that every
Copilot surface reads — so your instructions apply **everywhere, with no per-repo setup**:

- **Copilot CLI** (local and in Codespaces)
- **VS Code** — local, Remote-SSH, Codespaces (web), and VS Code connected to a Codespace
- the **GitHub Copilot desktop app** (it inherits `~/.copilot`)

It fetches over the GitHub API using your existing `gh` authentication — **no `git`, no credential
helper, nothing touches your keychain.**

## Install

```bash
gh extension install laserlemon/gh-copilot-instructions
gh copilot-instructions add laserlemon/my-instructions
```

That's it — your instruction files are now installed and applied across every surface (reload VS Code
or restart the desktop app to pick up changes).

## Commands

```
gh copilot-instructions add <owner/repo[@ref][:path]> [--token T]   # add a source, then pull
gh copilot-instructions add --repo R [--ref REF] [--path P] [--token T]
gh copilot-instructions pull [<id | owner/repo>]                    # pull all configured sources, or just one
gh copilot-instructions list [--json]                               # show sources and their pulled state
gh copilot-instructions remove <id | owner/repo>                    # remove one source and prune its files
gh copilot-instructions remove --all                                # remove every source, all installed files, and config
```

- **`add`** takes a positional spec, the equivalent flags, or a mix (a flag overrides the matching
  part of the spec). A glob `path` must be quoted. Paths are repo-root-relative (a leading `/` is fine).
- **`pull`** is incremental: it resolves each source's current commit SHA and **skips** any source
  that's already up to date (and self-heals if installed files go missing).
- **`list`** prints an aligned table on a terminal and tab-separated values when piped (use `--json`
  for structured output).
- **`remove`** / **`remove --all`** only ever delete files this tool installed (they carry a
  `gh-copilot-instructions.` prefix) — your own hand-written instruction files are never touched.

## Sources & configuration

A **source** is one line: `owner/repo[@ref][:path]` with an optional trailing token.

- `@ref` — branch, tag, or commit SHA (default: the repo's default branch). A full commit SHA is
  treated as immutable, so it never re-fetches.
- `:path` — a recursive glob, a file, or a directory (default: `**/*.instructions.md`, anywhere in
  the repo). Matched files are copied **verbatim**.
- The trailing token (last whitespace-separated field) is only needed for a private source when your
  `gh` auth can't read it (e.g. in Codespaces).

Configuration lives in **one of two places, same format**:

- **Local file** `~/.config/gh-copilot-instructions/sources` (mode `600`), managed by `add`/`remove`.
- **Environment variable** `GH_COPILOT_INSTRUCTIONS` (multiline) — when set, it **overrides** the
  file. Ideal for Codespaces secrets.

```
# example — one source per line:  owner/repo[@ref][:path]  [token]
laserlemon/my-instructions
acme/standards@main
partner/secure-rules:**/*.instructions.md   github_pat_xxx
```

Token resolution per source: inline token → `GH_COPILOT_INSTRUCTIONS_TOKEN` → your `gh` auth →
`GH_TOKEN`/`GITHUB_TOKEN` → anonymous (public repos).

Other variables: `GH_COPILOT_INSTRUCTIONS_TOKEN` (fallback token), `GH_COPILOT_INSTRUCTIONS_REF`
(default ref for lines that omit `@ref`).

## Use it everywhere

- **Local machine:** run the two install commands above. Re-run `gh copilot-instructions pull` to
  refresh.
- **New Codespaces (zero-touch):** add a multiline **Codespaces secret** named
  `GH_COPILOT_INSTRUCTIONS` (one source per line, with an inline token for any private source), and
  put these two lines in your dotfiles install script:
  ```bash
  gh extension install laserlemon/gh-copilot-instructions
  gh copilot-instructions pull
  ```
  Every new codespace then self-installs your instructions with no prompts.
- **VS Code / desktop app:** nothing to configure — they read `~/.copilot/instructions/`
  automatically. Reload the window / restart the app to pick up changes.

## A note on VS Code and `applyTo`

This tool copies your files **verbatim** — it never reads or edits `applyTo` or any other
frontmatter. VS Code only auto-applies a user-level `*.instructions.md` file when the file itself
declares an `applyTo` key, so include one in your source files (for example `applyTo: '**'`).

## Development

```bash
go build -o gh-copilot-instructions .
gh extension install .          # install your local build
go test ./...
```

Releases are built by the `cli/gh-extension-precompile` workflow on pushing a `v*` tag, which
attaches per-platform binaries so `gh extension install` works everywhere.
