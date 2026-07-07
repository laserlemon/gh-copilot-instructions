// Command gh-copilot-instructions is a gh CLI extension that pulls your Copilot
// custom instructions from one or more repos into ~/.copilot/instructions/, so
// they apply automatically across Copilot CLI, VS Code, and the desktop app.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/laserlemon/gh-copilot-instructions/internal/gci"
	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		// Failures the App already presented (a styled ✗ line plus any hint) are
		// marked ErrReported, so we set the exit status without printing a
		// duplicate "error:" line.
		if !errors.Is(err, gci.ErrReported) {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
}

// jsonOut is bound to the persistent --json flag on the root command, so every
// command inherits a single --json (shown under INHERITED FLAGS on subcommands).
var jsonOut bool

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "copilot-instructions <command> <subcommand> [flags]",
		Short: "Sync your Copilot custom instructions to every coding surface",
		Long: "Install custom Copilot instructions from one or more repositories.\n" +
			"Locally, instructions apply automatically in Copilot CLI, GitHub Copilot\n" +
			"app, and VS Code. Instructions apply in Codespaces with additional setup.",
		Example: heredoc(`
			$ gh copilot-instructions add acme/team-instructions
			$ gh copilot-instructions source list
			$ gh copilot-instructions auto-pull enable --every day`),
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		// With no subcommand, show the source list once sources are configured;
		// on a fresh install (no sources) show help, so first-time users get the
		// full command overview instead of an empty table. --json always lists
		// (emitting [] when empty) so scripts get machine-readable output.
		RunE: func(cmd *cobra.Command, args []string) error {
			if !jsonOut && !sourcesConfigured() {
				return cmd.Help()
			}
			return runList(jsonOut, false)
		},
	}
	root.PersistentFlags().BoolVarP(&jsonOut, "json", "j", false, "Output JSON")
	// Canonical command groups.
	root.AddCommand(sourceCmd(), fileCmd(), autoPullCmd())
	// Hidden top-level aliases keep common commands reachable under short names
	// (e.g. `add` == `source add`, `sources` == `source list`). Each is an
	// independent command instance sharing the same behavior; the alias helper
	// renames it and hides it from help. Keep in sync with topLevelShortcuts,
	// which documents them in the root help.
	root.AddCommand(
		alias("add", "source add", addCmd()),
		alias("pull", "source pull", pullCmd()),
		alias("sources", "source", listCmd()),
		alias("files", "file", fileListCmd()),
	)
	applyGHStyle(root)
	return root
}

// sourceCmd is the `source` group: manage the configured instruction sources.
// Bare `source` defaults to `list`.
func sourceCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "source <command> [flags]",
		Short: "Manage instruction sources",
		Long: "Manage the repositories your instructions are pulled from: list them, pull\n" +
			"them, add one (and pull it), or remove one (or all of them).",
		Example: heredoc(`
			$ gh copilot-instructions source add acme/team-instructions
			$ gh copilot-instructions source list
			$ gh copilot-instructions source pull`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(jsonOut, false)
		},
	}
	c.AddCommand(listCmd(), addCmd(), removeCmd(), pullCmd())
	return c
}

// fileCmd is the `file` group: inspect the installed instruction files. Bare
// `file` defaults to `list`.
func fileCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "file <command> [flags]",
		Short: "Show pulled instruction files",
		Long: "List every instruction file installed from your configured sources,\n" +
			"with the source each came from.",
		Example: heredoc(`
			$ gh copilot-instructions file list
			$ gh copilot-instructions file list --json | jq -r '.[].remotePath'`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return newApp().RenderFileList(jsonOut)
		},
	}
	c.AddCommand(fileListCmd())
	return c
}

func fileListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:         "list",
		Short:       "List all pulled instruction files",
		Annotations: map[string]string{"helpProxy": "file"},
		Long: "List every instruction file installed from your configured sources,\n" +
			"with the source each came from.",
		Example: heredoc(`
			$ gh copilot-instructions file list
			$ gh copilot-instructions file list --json | jq -r '.[].remotePath'`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return newApp().RenderFileList(jsonOut)
		},
	}
	return c
}

// alias turns a command builder into a hidden top-level alias: it renames the
// command to `use` (its invocation name), hides it from help, and points its
// help at the canonical `proxy` command path (so `--help` on the alias renders
// the same page as its target). Used for the shortcuts documented in the root
// help's SHORTCUTS section.
func alias(use, proxy string, c *cobra.Command) *cobra.Command {
	c.Use = use
	c.Hidden = true
	if c.Annotations == nil {
		c.Annotations = map[string]string{}
	}
	c.Annotations["helpProxy"] = proxy
	return c
}

// sourcesConfigured reports whether any sources are configured (via the env var
// or the local file), so the root command can show help on a fresh install.
func sourcesConfigured() bool {
	srcs, origin, _ := gci.DefaultPaths().LoadSources()
	return origin != gci.OriginNone && len(srcs) > 0
}

// runList renders the configured sources (the `list` command, and the default
// action when no subcommand is given).
func runList(asJSON, raw bool) error {
	if asJSON && raw {
		return fmt.Errorf("cannot use --json and --raw together")
	}
	return newApp().RenderList(asJSON, raw)
}

func newApp() *gci.App { return gci.New(os.Stdout, os.Stderr) }

func addCmd() *cobra.Command {
	var ref, path, token string
	c := &cobra.Command{
		Use:   "add [<owner/repo> | <blob-url>]",
		Short: "Add a source and pull it",
		Long: "Add a source, then pull. Give the source as owner/repo or a GitHub blob\n" +
			"URL. With owner/repo, --ref and --path select a ref and a path within the\n" +
			"repository; a blob URL already carries its ref and path, so those flags\n" +
			"are ignored. Quote a glob path.\n\n" +
			"Your gh auth is used by default. If gh cannot access a repository, you may\n" +
			"provide a personal access token (with permission to read repository\n" +
			"contents) using --token.",
		Example: heredoc(`
			# Add a source by owner/repo (default branch, default path: **/*.instructions.md)
			$ gh copilot-instructions source add acme/team-instructions

			# Pin a ref and select a path within the repository
			$ gh copilot-instructions source add acme/team-instructions --ref main --path 'instructions/*.md'

			# Add a specific GitHub blob source
			$ gh copilot-instructions source add https://github.com/acme/team-instructions/blob/main/style.instructions.md`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := ""
			if len(args) == 1 {
				arg = args[0]
			}
			s, err := buildSource(arg, ref, path, token)
			if err != nil {
				return err
			}
			return newApp().Add(s, jsonOut)
		},
	}
	c.Flags().StringVarP(&ref, "ref", "r", "", "Branch, tag, or commit SHA (default: the repository's default branch)")
	c.Flags().StringVarP(&path, "path", "p", "", "Glob, file, or directory within the repository (default \"**/*.instructions.md\")")
	c.Flags().StringVarP(&token, "token", "t", "", "Personal access token (read repository contents) for repositories that gh cannot access")
	return c
}

func pullCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "pull [<slug> | <owner/repo>]",
		Short: "Pull one or all sources",
		Long: "Pull all configured sources, or just the one matching the given slug\n" +
			"or owner/repo.",
		Example: heredoc(`
			# Pull every configured source
			$ gh copilot-instructions source pull

			# Pull just one source by slug or owner/repo
			$ gh copilot-instructions source pull acme/team-instructions

			# List the repos whose commit changed on this pull
			$ gh copilot-instructions source pull --json | jq -r '.[] | select(.state=="updated") | .repo'`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filter := ""
			if len(args) == 1 {
				filter = args[0]
			}
			return newApp().Pull(filter, jsonOut)
		},
	}
	return c
}

func listCmd() *cobra.Command {
	var raw bool
	c := &cobra.Command{
		Use:         "list",
		Short:       "List all sources and their states",
		Long:        "List all sources and their states.",
		Annotations: map[string]string{"helpProxy": "source"},
		Example: heredoc(`
			$ gh copilot-instructions source list
			$ gh copilot-instructions source list --raw
			$ gh copilot-instructions source list --json | jq -r '.[].repository'`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(jsonOut, raw)
		},
	}
	c.Flags().BoolVar(&raw, "raw", false, "Output config-file lines to paste into a Codespaces secret")
	return c
}

func removeCmd() *cobra.Command {
	var ref, path string
	var all bool
	c := &cobra.Command{
		Use:   "remove [<owner/repo> | <blob-url> | <slug>]",
		Short: "Remove one source and its files, or --all",
		Long: "Remove one configured source and prune the files it installed, or use\n" +
			"--all to remove every source, all installed files, and the local config.\n\n" +
			"Identify the source the way you added it: owner/repo (optionally with\n" +
			"--ref/--path), a GitHub blob URL, or its slug (the SLUG column of the\n" +
			"list output).",
		Example: heredoc(`
			# Remove a source by owner/repo
			$ gh copilot-instructions source remove acme/team-instructions

			# Remove a specific ref/path variant (the way it was added)
			$ gh copilot-instructions source remove acme/team-instructions --ref main --path instructions

			# Remove by slug, from the SLUG column of the list output
			$ gh copilot-instructions source remove a1b2c3d4

			# Remove everything this extension installed
			$ gh copilot-instructions source remove --all`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := newApp()
			arg := ""
			if len(args) == 1 {
				arg = args[0]
			}
			if all {
				if arg != "" || ref != "" || path != "" {
					return fmt.Errorf("--all takes no other arguments")
				}
				return app.RemoveAll(jsonOut)
			}
			if arg == "" {
				return fmt.Errorf("specify owner/repo, a GitHub blob URL, or a slug to remove, or use --all")
			}
			// owner/repo and blob URLs contain a slash; a slug never does.
			if strings.Contains(arg, "/") {
				s, err := buildRemoveTarget(arg, ref, path)
				if err != nil {
					return err
				}
				return app.Remove(s.Spec(), jsonOut)
			}
			if ref != "" || path != "" {
				return fmt.Errorf("--ref and --path apply to owner/repo, not a slug")
			}
			return app.Remove(arg, jsonOut)
		},
	}
	c.Flags().StringVarP(&ref, "ref", "r", "", "Branch, tag, or commit SHA (default: the repository's default branch)")
	c.Flags().StringVarP(&path, "path", "p", "", "Glob, file, or directory within the repository (default \"**/*.instructions.md\")")
	c.Flags().BoolVar(&all, "all", false, "Remove every source, all installed files, and config")
	return c
}

func autoPullCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "auto-pull <command> [flags]",
		Short: "Toggle automatic pulling of all sources",
		Long: "Enable or disable a recurring background pull, so this machine keeps its\n" +
			"instructions up-to-date automatically. When enabled, macOS (launchd)\n" +
			"runs `gh copilot-instructions pull` on a regular cadence. Mac only.",
		Example: heredoc(`
			# Show whether auto-pull is enabled and how often it runs
			$ gh copilot-instructions auto-pull

			# Enable it with the default daily cadence
			$ gh copilot-instructions auto-pull enable

			# Enable it every 3 hours (or every 2 days, every week, ...)
			$ gh copilot-instructions auto-pull enable --every 3h

			# Disable it
			$ gh copilot-instructions auto-pull disable`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return newApp().AutoPullStatus(jsonOut)
		},
	}

	var every string
	enable := &cobra.Command{
		Use:   "enable",
		Short: "Enable scheduled background pulling",
		Long: "Install a recurring job that runs gh copilot-instructions pull. Re-run\n" +
			"with a different --every to change the cadence. macOS only for now.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cadence, err := gci.ParseCadence(every)
			if err != nil {
				return err
			}
			return newApp().AutoPullEnable(cadence, jsonOut)
		},
	}
	enable.Flags().StringVar(&every, "every", gci.DefaultEvery, "Cadence: hour, day, or week with shorthands, e.g. 3h, 2d, 1w")

	disable := &cobra.Command{
		Use:   "disable",
		Short: "Disable scheduled background pulling",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return newApp().AutoPullDisable(jsonOut)
		},
	}

	status := &cobra.Command{
		Use:         "status",
		Short:       "Show whether auto-pull is enabled and its cadence",
		Annotations: map[string]string{"helpProxy": "auto-pull"},
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return newApp().AutoPullStatus(jsonOut)
		},
	}

	c.AddCommand(enable, disable, status)
	return c
}

// heredoc trims a leading newline and reindents each line to gh's two-space
// example indentation (input lines are written with three leading tabs). Blank
// lines stay empty (no trailing whitespace).
func heredoc(s string) string {
	s = strings.TrimPrefix(s, "\n")
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		ln = strings.TrimPrefix(ln, "\t\t\t")
		if ln == "" {
			lines[i] = ""
			continue
		}
		lines[i] = "  " + ln
	}
	return strings.Join(lines, "\n")
}

// buildSource builds a source for `add` from a positional argument plus flags.
// The argument is either a GitHub blob URL - which carries its own ref and path,
// so --ref/--path are ignored and ResolveSpec disambiguates a slashed branch
// name against the API - or a bare owner/repo, whose ref and path come from the
// flags. (Gist support will slot in here as another recognized form.)
func buildSource(arg, ref, path, token string) (gci.Source, error) {
	if gci.IsGitHubURL(arg) {
		s, err := newApp().ResolveSpec(arg)
		if err != nil {
			return s, err
		}
		if token != "" {
			s.Token = token
		}
		return s, nil
	}
	s, err := gci.ParseRepo(arg)
	if err != nil {
		return s, err
	}
	applyRefPath(&s, ref, path)
	if token != "" {
		s.Token = token
	}
	return s, nil
}

// buildRemoveTarget builds the source to remove from a positional argument plus
// flags. Like buildSource it accepts a blob URL or a bare owner/repo, but it
// resolves offline (ParseSpec), because remove only needs to identify an
// already-configured source and must never require the network; the slug is the
// escape hatch for the rare slashed-ref blob URL.
func buildRemoveTarget(arg, ref, path string) (gci.Source, error) {
	if gci.IsGitHubURL(arg) {
		return gci.ParseSpec(arg) // ref/path flags ignored for a URL
	}
	s, err := gci.ParseRepo(arg)
	if err != nil {
		return s, err
	}
	applyRefPath(&s, ref, path)
	return s, nil
}

// applyRefPath overlays the --ref/--path flag values onto a source (a leading
// slash on the path is trimmed). Empty values leave the source unchanged.
func applyRefPath(s *gci.Source, ref, path string) {
	if ref != "" {
		s.Ref = ref
	}
	if path != "" {
		s.Path = strings.TrimPrefix(path, "/")
	}
}
