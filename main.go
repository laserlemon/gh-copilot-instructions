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

func rootCmd() *cobra.Command {
	var asJSON bool
	root := &cobra.Command{
		Use:   "copilot-instructions [<command>]",
		Short: "Sync your Copilot custom instructions to every coding surface",
		Long: "Install custom Copilot instructions from one or more repositories.\n" +
			"Locally, instructions apply automatically in Copilot CLI, GitHub Copilot\n" +
			"app, and VS Code. Instructions apply in Codespaces with additional setup.",
		Example: heredoc(`
			$ gh copilot-instructions add laserlemon/my-instructions
			$ gh copilot-instructions auto-pull --every day
			$ gh copilot-instructions list`),
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		// With no subcommand, default to `list`. --raw is list-specific, so it's
		// not offered here; use `list --raw` for the config-file format.
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(asJSON, false)
		},
	}
	root.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	root.AddCommand(addCmd(), pullCmd(), listCmd(), removeCmd(), autoPullCmd(), codespacesCmd())
	applyGHStyle(root)
	return root
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
	var repo, ref, path, token string
	var asJSON bool
	c := &cobra.Command{
		Use:   "add [<owner/repo[@ref][:path]>]",
		Short: "Add a source and pull it",
		Long: "Add a source, then pull. Provide a positional spec, or use flags, or\n" +
			"mix them (a flag overrides the matching part of the spec). Quote a glob.\n\n" +
			"Your gh auth is used by default. If gh cannot access a repository, you may\n" +
			"provide a personal access token (with permission to read repository\n" +
			"contents) using --token.",
		Example: heredoc(`
			# Add a source by owner/repo (default branch, default path)
			$ gh copilot-instructions add github/team-instructions

			# Pin a ref and select a path within the repository
			$ gh copilot-instructions add github/team-instructions@main:instructions

			# Build the source from flags instead of a spec
			$ gh copilot-instructions add --repo github/team-instructions --ref v1.2.0`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec := ""
			if len(args) == 1 {
				spec = args[0]
			}
			s, err := buildSource(spec, repo, ref, path, token)
			if err != nil {
				return err
			}
			return newApp().Add(s, asJSON)
		},
	}
	c.Flags().StringVar(&repo, "repo", "", "Source repository (`owner/repo`)")
	c.Flags().StringVar(&ref, "ref", "", "Branch, tag, or commit SHA (default: the repository's default branch)")
	c.Flags().StringVar(&path, "path", "", "Glob, file, or directory within the repository (default: **/*.instructions.md)")
	c.Flags().StringVar(&token, "token", "", "Personal access token (read repository contents) for when gh cannot access a repository")
	c.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return c
}

func pullCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "pull [<id | owner/repo>]",
		Short: "Pull one or all sources",
		Long: "Pull all configured sources, or just the one matching the given id or\n" +
			"owner/repo.\n\n" +
			"With --json, each source is reported with a state of \"pulled\",\n" +
			"\"updated\" (its commit moved), or \"failed\".",
		Example: heredoc(`
			# Pull every configured source
			$ gh copilot-instructions pull

			# Pull just one source by id or owner/repo
			$ gh copilot-instructions pull github/team-instructions

			# List the repos whose commit changed on this pull
			$ gh copilot-instructions pull --json | jq -r '.[] | select(.state=="updated") | .repo'`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filter := ""
			if len(args) == 1 {
				filter = args[0]
			}
			return newApp().Pull(filter, asJSON)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return c
}

func listCmd() *cobra.Command {
	var asJSON, raw bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List all sources and their states",
		Long: "List all sources and their states.\n\n" +
			"Use --raw to print the sources in config-file format (one per line,\n" +
			"including any inline tokens) - ready to paste into the multiline\n" +
			"GH_COPILOT_INSTRUCTIONS Codespaces secret.",
		Example: heredoc(`
			# List configured sources and their state
			$ gh copilot-instructions list

			# Emit the config to paste into a Codespaces secret
			$ gh copilot-instructions list --raw

			# Machine-readable output for scripting
			$ gh copilot-instructions list --json | jq -r '.[].repo'`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(asJSON, raw)
		},
	}
	c.Flags().BoolVar(&raw, "raw", false, "Output config-file lines to paste into a Codespaces secret")
	c.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return c
}

func removeCmd() *cobra.Command {
	var repo, ref, path string
	var all, asJSON bool
	c := &cobra.Command{
		Use:   "remove [<slug | owner/repo[@ref][:path]>]",
		Short: "Remove one source and its files, or --all",
		Long: "Remove one configured source and prune the files it installed, or use\n" +
			"--all to remove every source, all installed files, and the local config.\n\n" +
			"Identify the source the way you added it: an owner/repo[@ref][:path] spec,\n" +
			"a GitHub blob URL, or the equivalent --repo/--ref/--path flags. You can\n" +
			"also pass a source's slug from the SLUG column of the list output.\n\n" +
			"With --json, the remaining sources are reported (like list --json).",
		Example: heredoc(`
			# Remove a source by owner/repo
			$ gh copilot-instructions remove github/team-instructions

			# Remove a specific ref/path variant (the way it was added)
			$ gh copilot-instructions remove github/team-instructions@main:instructions

			# Remove by slug, from the SLUG column of the list output
			$ gh copilot-instructions remove a1b2c3d4

			# Remove everything this extension installed
			$ gh copilot-instructions remove --all`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := newApp()
			spec := ""
			if len(args) == 1 {
				spec = args[0]
			}
			if all {
				if spec != "" || repo != "" || ref != "" || path != "" {
					return fmt.Errorf("--all takes no other arguments")
				}
				return app.RemoveAll(asJSON)
			}
			// A spec/URL or --repo/--ref/--path builds the exact source, the same
			// way add identifies it; a bare token is treated as a slug.
			if repo != "" || ref != "" || path != "" || strings.Contains(spec, "/") {
				s, err := buildRemoveTarget(spec, repo, ref, path)
				if err != nil {
					return err
				}
				return app.Remove(s.Spec(), asJSON)
			}
			if spec == "" {
				return fmt.Errorf("specify a slug, owner/repo[@ref][:path], or a GitHub URL to remove, or use --all")
			}
			return app.Remove(spec, asJSON)
		},
	}
	c.Flags().StringVar(&repo, "repo", "", "Source repository (`owner/repo`)")
	c.Flags().StringVar(&ref, "ref", "", "Branch, tag, or commit SHA (default: the repository's default branch)")
	c.Flags().StringVar(&path, "path", "", "Glob, file, or directory within the repository (default: **/*.instructions.md)")
	c.Flags().BoolVar(&all, "all", false, "Remove every source, all installed files, and config")
	c.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return c
}

func autoPullCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "auto-pull [enable | disable | status]",
		Short: "Toggle automatic pulling of all sources",
		Long: "Enable or disable a recurring background pull, so this machine keeps its\n" +
			"instructions fresh with no manual step. When enabled, macOS (launchd) runs\n" +
			"gh copilot-instructions pull on a cadence. macOS only for now. Other\n" +
			"platforms print how to schedule it themselves.\n\n" +
			"Run with no argument (or status) to see the current state.",
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
			return newApp().AutoPullStatus(asJSON)
		},
	}
	c.PersistentFlags().BoolVar(&asJSON, "json", false, "Output JSON")

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
			return newApp().AutoPullEnable(cadence, asJSON)
		},
	}
	enable.Flags().StringVar(&every, "every", gci.DefaultEvery, "Cadence: hour, day, or week (with h/d/w shorthands and a count, e.g. 3h, 2d, 1w)")

	disable := &cobra.Command{
		Use:   "disable",
		Short: "Disable scheduled background pulling",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return newApp().AutoPullDisable(asJSON)
		},
	}

	status := &cobra.Command{
		Use:   "status",
		Short: "Show whether auto-pull is enabled and its cadence",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return newApp().AutoPullStatus(asJSON)
		},
	}

	c.AddCommand(enable, disable, status)
	return c
}

// codespacesCmd is the `codespaces` group. `check` is the default subcommand, so
// `gh copilot-instructions codespaces` runs the read-only readiness doctor.
// (setup/update land in later phases; see the codespaces feature issue.)
func codespacesCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "codespaces [check]",
		Short: "Check and set up instructions for Codespaces",
		Long: "Get your instructions working in Codespaces. A Codespace starts without\n" +
			"your home directory, so it needs a GH_COPILOT_INSTRUCTIONS secret and a\n" +
			"per-Codespace step that installs this extension and pulls your sources.\n\n" +
			"Run with no argument (or check) for a read-only readiness report. Run it\n" +
			"inside a Codespace to verify the setup actually took effect there.",
		Example: heredoc(`
			# Report Codespaces readiness (nothing is changed)
			$ gh copilot-instructions codespaces

			# Machine-readable readiness for scripting
			$ gh copilot-instructions codespaces check --json`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return newApp().CodespacesCheck(asJSON)
		},
	}
	c.PersistentFlags().BoolVar(&asJSON, "json", false, "Output JSON")

	check := &cobra.Command{
		Use:   "check",
		Short: "Report Codespaces readiness without changing anything",
		Long: "Run every readiness detector and report each as ✓/✗/!, with a copy-paste\n" +
			"fix for any gap. Never writes. Supports --json. Run inside a Codespace to\n" +
			"check whether the setup actually took effect there instead.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return newApp().CodespacesCheck(asJSON)
		},
	}

	c.AddCommand(check)
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

// buildSource combines an optional positional spec with flag overrides. It uses
// ResolveSpec so an ambiguous GitHub blob URL (a slashed branch name) is
// disambiguated against the API, matching how add stores the source.
func buildSource(spec, repo, ref, path, token string) (gci.Source, error) {
	var base gci.Source
	if spec != "" {
		parsed, err := newApp().ResolveSpec(spec)
		if err != nil {
			return base, err
		}
		base = parsed
	}
	return mergeSource(base, repo, ref, path, token)
}

// buildRemoveTarget builds the source to remove from an optional spec plus flag
// overrides. Unlike buildSource it resolves offline (ParseSpec), because remove
// only needs to identify an already-configured source and must never require the
// network; the slug is the escape hatch for the rare slashed-ref blob URL.
func buildRemoveTarget(spec, repo, ref, path string) (gci.Source, error) {
	var base gci.Source
	if spec != "" {
		parsed, err := gci.ParseSpec(spec)
		if err != nil {
			return base, err
		}
		base = parsed
	}
	return mergeSource(base, repo, ref, path, "")
}

// mergeSource applies flag overrides onto a parsed base source and validates it.
func mergeSource(base gci.Source, repo, ref, path, token string) (gci.Source, error) {
	s := base
	if repo != "" {
		s.Repo = repo
	}
	if ref != "" {
		s.Ref = ref
	}
	if path != "" {
		s.Path = strings.TrimPrefix(path, "/")
	}
	if token != "" {
		s.Token = token
	}
	if s.Repo == "" {
		return s, fmt.Errorf("a repo is required: pass owner/repo or --repo owner/repo")
	}
	if _, err := gci.ParseSpec(s.Repo); err != nil {
		return s, err
	}
	return s, nil
}
