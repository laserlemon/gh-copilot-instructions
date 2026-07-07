# gh-copilot-instructions

A [`gh` CLI](https://cli.github.com) extension that **pulls your Copilot custom instructions** from
one or more repos into the user-level locations every Copilot surface reads — so your instructions
apply **everywhere, with no per-repo setup**:

- **Copilot CLI** (local and in Codespaces) and the **GitHub Copilot desktop app** — both read
  `~/.copilot/instructions/`.
- **VS Code** — local, and VS Code Desktop connected to a Codespace. VS Code reads its own
  user-level `prompts` directory rather than `~/.copilot`, so the tool **also installs a copy there**
  when it detects VS Code (see [VS Code](#vs-code)).

It fetches over the GitHub API using your existing `gh` authentication — **no `git`, no credential
helper, nothing touches your keychain.**

## Install

```bash
gh extension install laserlemon/gh-copilot-instructions
gh copilot-instructions add acme/team-instructions
```

That's it — your instruction files are now installed and applied across every surface (reload VS Code
or restart the desktop app to pick up changes).

## Commands

```
gh copilot-instructions                                                    # list sources (or show help on a fresh install)
gh copilot-instructions source add [<owner/repo> | <blob-url>] [--ref REF] [--path P] [--token T]
gh copilot-instructions source pull [<slug> | <owner/repo>]                 # pull all configured sources, or just one
gh copilot-instructions source list [--raw]                                 # show sources and their pulled state
gh copilot-instructions source remove [<owner/repo> | <blob-url> | <slug>]  # remove one source and prune its files
gh copilot-instructions source remove --all                                 # remove every source, all installed files, and config
gh copilot-instructions auto-pull [status]                                  # show whether scheduled pulling is enabled
gh copilot-instructions auto-pull enable [--every hour|day|week|Nh|Nd|Nw]   # schedule background pulls (default: day)
gh copilot-instructions auto-pull disable                                   # disable scheduled background pulls
```

The source-management commands live under `source` (`source list`, `source add`, `source pull`,
`source remove`) and the installed files under `file` (`file list`). For convenience several bare
names work as top-level aliases: `add` → `source add`, `pull` → `source pull`, `sources` →
`source list`, and `files` → `file list`. Running `gh copilot-instructions` with no command lists
your sources once any are configured, and shows help on a fresh install.

Every command accepts `--json` for machine-readable output. On a terminal the JSON is pretty-printed
and syntax-highlighted; piped, it stays compact (one line) so it pipes cleanly into `jq`.

- **`source add`** takes an `owner/repo` with optional `--ref`/`--path`, or a **GitHub blob URL**
  (which carries its own ref and path, so `--ref`/`--path` are ignored). A glob `path` must be quoted
  and is repo-root-relative (a leading `/` is fine).
- **`source pull`** is incremental: it resolves each source's current commit SHA and **skips** any
  source that's already up to date (and self-heals if installed files go missing).
- **`source list`** prints an aligned table on a terminal and tab-separated values when piped. The
  first column shows each source's state — `✓ PULLED`, `• PENDING`, or `× FAILED` (failed means the
  last pull matched no files or its installed files are missing). Use `--json` for structured output,
  or `--raw` to print the sources in config-file format (one per line, with any inline tokens) — ready
  to paste into the multiline `GH_COPILOT_INSTRUCTIONS` Codespaces secret.
- **`source remove`** identifies a source the same way `source add` does — an `owner/repo` (optionally
  with `--ref`/`--path`) or a GitHub blob URL — or by its **slug** (the `SLUG` column
  of `source list`). `source remove` / `source remove --all` only ever delete files this tool installed
  (they live under the `~/.copilot/instructions/gh-copilot-instructions/` directory) — your own
  hand-written instruction files are never touched.
- **`auto-pull`** enables or disables scheduled background pulling (`enable` / `disable` / `status`).
  See [Keep it fresh with auto-pull](#keep-it-fresh-with-auto-pull).


## Sources & configuration

A **source** is one line: `owner/repo[@ref][:path]` with an optional trailing token.

- `@ref` — branch, tag, or commit SHA (default: the repo's default branch). A full commit SHA is
  treated as immutable, so it never re-fetches.
- `:path` — a recursive glob, a file, or a directory (default: `**/*.instructions.md`, anywhere in
  the repo). Matched files are copied **verbatim**.
- The trailing token (last whitespace-separated field) is only needed for a private source when your
  `gh` auth can't read it (e.g. in Codespaces).

That compact `owner/repo[@ref][:path]` form is the **config-file / secret line format**. On the command
line, `source add` instead takes `owner/repo` with `--ref`/`--path` flags (or a GitHub blob URL) — see
[Commands](#commands).

You can also `add` a **GitHub blob URL** and it's normalized to the same source — handy for copy-paste
from the browser:

```
gh copilot-instructions add https://github.com/owner/repo/blob/main/path/to/file.md   # a file
gh copilot-instructions add https://github.com/owner/repo/blob/-/instructions/x.md    # - = default branch
```

Configuration lives in **one of two places, same format**:

- **Local file** `~/.config/gh-copilot-instructions/sources` (mode `600`), managed by `add`/`remove`.
- **Environment variable** `GH_COPILOT_INSTRUCTIONS` (multiline) — when set, it **overrides** the
  file. Ideal for Codespaces secrets.

```
# example — one source per line:  owner/repo[@ref][:path]  [token]
acme/team-instructions
acme/standards@main:**/*.instructions.md   github_pat_xxx
```

Token resolution per source: inline token → `GH_COPILOT_INSTRUCTIONS_TOKEN` → your `gh` auth →
`GH_TOKEN`/`GITHUB_TOKEN` → anonymous (public repos).

Other variables: `GH_COPILOT_INSTRUCTIONS_TOKEN` (fallback token), `GH_COPILOT_INSTRUCTIONS_REF`
(default ref for lines that omit `@ref`).

## Use it everywhere

- **Local machine:** run the two install commands above. Re-run `gh copilot-instructions source pull` to
  refresh.
- **New Codespaces (zero-touch):** add a multiline **Codespaces secret** named
  `GH_COPILOT_INSTRUCTIONS` (one source per line, with an inline token for any private source). The
  quickest way to produce that value is to run `gh copilot-instructions source list --raw` on your machine
  and paste the output (add tokens for any private repos). Then put these two lines in your dotfiles
  install script:
  ```bash
  gh extension install laserlemon/gh-copilot-instructions
  gh copilot-instructions source pull
  ```
  Every new codespace then self-installs your instructions with no prompts.
- **VS Code / desktop app:** nothing to configure. The desktop app reads `~/.copilot/instructions/`;
  VS Code reads its own prompts directory, which the tool populates automatically when it's installed
  (see [VS Code](#vs-code)). Reload the window / restart the app to pick up changes.

## Keep it fresh with auto-pull

Instead of re-running `pull` by hand, let your machine do it on a schedule:

```bash
gh copilot-instructions auto-pull enable           # daily (the default)
gh copilot-instructions auto-pull enable --every 3h   # every 3 hours
gh copilot-instructions auto-pull enable --every 1w   # weekly
gh copilot-instructions auto-pull                  # status
gh copilot-instructions auto-pull disable          # disable
```

`--every` takes a base unit — `hour`, `day`, or `week` (shorthands `h`, `d`, `w`) — with an optional
count, so `h`, `3h`, `day`, `2d`, and `1w` are all valid. The default is `day`, and the clock starts
when you run `enable`.

When enabled, a recurring job runs `gh copilot-instructions source pull` at that cadence, using the absolute
path to `gh` so it works regardless of the scheduler's `PATH`. Output is logged to
`~/.local/state/gh-copilot-instructions/auto-pull.log`.

- **macOS** is supported today, via a **launchd** LaunchAgent
  (`~/Library/LaunchAgents/com.github.laserlemon.gh-copilot-instructions.plist`).
- **Linux / Windows** aren't wired up yet — the command tells you so and points you at scheduling
  `gh copilot-instructions source pull` yourself (cron, Task Scheduler).

`auto-pull status` reconciles the recorded setting against the actual launchd job and warns if they've
drifted apart (for example, if the agent was removed by hand). Pulls run non-interactively, so the
scheduled job needs your `gh` auth to be readable without a prompt — check the log if a source stops
updating.

## VS Code

VS Code reads user-level instructions from its **own** profile directory — `User/prompts/` — not from
`~/.copilot/instructions/`. So when the tool detects VS Code, `add`/`pull` install a second copy of
your instructions into its prompts directory (and `remove` prunes it):

```
<VS Code User dir>/prompts/gh-copilot-instructions/<slug>/<file>.instructions.md
```

- **Detected editors:** Stable (`Code`), Insiders (`Code - Insiders`), and VSCodium, on macOS, Linux,
  and Windows. A copy is written only for an editor whose profile directory already exists — the tool
  never creates one, and simply does nothing on a machine without VS Code.
- **This also covers Codespaces opened in VS Code Desktop.** When you connect Desktop to a Codespace,
  VS Code applies your **local** machine's instructions (client-side), so no Codespaces setup is
  needed for that workflow. (A Codespace's **integrated terminal** — where the Copilot CLI runs — is a
  separate surface; getting instructions there is the job of the Codespaces setup, still in progress.)
- The copies are kept in sync on every `pull`: files are updated, and instructions for removed sources
  are pruned. The tool only ever manages its own `gh-copilot-instructions/` subdirectory, so your
  hand-written `*.instructions.md` prompt files are never touched.

> **Not covered:** Codespaces opened **in the browser** (`*.github.dev`). VS Code for the Web doesn't
> reliably apply user-level instruction files today, and no file placement changes that — so the tool
> can't target it. If that changes upstream, the browser will pick these up automatically via Settings
> Sync, since the tool already populates the directory Sync carries.

## A note on VS Code and `applyTo`

This tool copies your files **verbatim** — it never reads or edits `applyTo` or any other
frontmatter. VS Code only auto-applies a user-level `*.instructions.md` file when the file itself
declares an `applyTo` key, so include one in your source files (for example `applyTo: '**'`).

As a safety net, `add` and `pull` **warn** when a pulled source installs files with no `applyTo`
value, telling you exactly how many and how to fix them — so a file that would silently never apply
in VS Code doesn't slip by unnoticed. The files are still installed unchanged; only the warning is
new.

## How files are installed

Matched files are written under a single namespace directory, mirroring each source's repo layout:

```
~/.copilot/instructions/gh-copilot-instructions/<slug>/<repo-relative-path>
```

- `<slug>` is the source's deterministic slug (`sha256(owner/repo + ref + path)`, first 8 base36 chars), so
  every source gets its own subtree for clean pruning and removal.
- The repo-relative directory structure is preserved, and content is copied **verbatim**. Each file
  name is normalized to a clean `*.instructions.md` (drop a trailing `.md`, then a trailing
  `.instructions`, then append `.instructions.md`) — `ruby.md` and `ruby.instructions.md` both become
  `ruby.instructions.md`. Copilot only auto-loads files with that suffix, and it searches this
  directory recursively, so nested files are picked up automatically.
- If two files in a source normalize to the same name, the one that already ended in
  `.instructions.md` is kept, then `.md`, then anything else (ties break on the lowest repo path); the
  rest are skipped with a warning. This is deterministic and rare.

Because everything we install lives under `gh-copilot-instructions/`, `remove` and `remove --all`
never touch your own hand-written `~/.copilot/instructions/*.instructions.md` files.

## Development

Iterate on the extension locally without cutting a release. A **dev (symlink) install**
points `gh` at your working copy, so a rebuilt binary takes effect immediately:

```bash
make dev     # build + symlink-install: `gh copilot-instructions ...` now runs your local build
make         # rebuild after each change (the symlink picks it up; no reinstall, no tag)
make test    # go test ./...
make release # switch back to the published release build
```

Under the hood `make dev` runs `gh extension install .`, which symlinks
`~/.local/share/gh/extensions/gh-copilot-instructions` to this repo and runs the
`gh-copilot-instructions` binary you build here. Because our state lives in its own
`~/.local/state/gh-copilot-instructions/` namespace (not under gh's
`~/.local/state/gh/extensions/`, which gh wipes on install/remove), switching between dev
and release builds never disturbs your pulled state.

Releases are built by the `cli/gh-extension-precompile` workflow on pushing a `v*` tag, which
attaches per-platform binaries so `gh extension install` works everywhere.
