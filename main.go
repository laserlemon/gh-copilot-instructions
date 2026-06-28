// Command gh-copilot-instructions is a gh CLI extension that pulls your Copilot
// custom instructions from one or more repos into ~/.copilot/instructions/, so
// they apply automatically across Copilot CLI, VS Code, and the desktop app.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/laserlemon/gh-copilot-instructions/internal/gci"
	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "copilot-instructions <command>",
		Short: "Sync your Copilot custom instructions to every coding surface",
		Long: "Pull your Copilot custom instructions from one or more repos into\n" +
			"~/.copilot/instructions/, where Copilot CLI, VS Code, and the GitHub\n" +
			"Copilot desktop app all read them automatically - no per-repo setup.",
		Example: heredoc(`
			# Add your team's shared instructions and pull them
			$ gh copilot-instructions add github/team-instructions

			# Update every configured source to its latest commit
			$ gh copilot-instructions pull

			# See what's configured and installed
			$ gh copilot-instructions list`),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(addCmd(), pullCmd(), listCmd(), removeCmd())
	applyGHStyle(root)
	return root
}

func newApp() *gci.App { return gci.New(os.Stdout, os.Stderr) }

func addCmd() *cobra.Command {
	var repo, ref, glob, pathAlias, token string
	var asJSON bool
	c := &cobra.Command{
		Use:   "add [<owner/repo[@ref][:path]>]",
		Short: "Add a source and pull it",
		Long: "Add a source, then pull. Provide a positional spec, or use flags, or\n" +
			"mix them (a flag overrides the matching part of the spec). Quote a glob.\n\n" +
			"For a private repository, --token needs read access to repository contents\n" +
			"(a fine-grained PAT with \"Contents: read\", or a classic token with the\n" +
			"\"repo\" scope). Public repositories need no token; your gh auth is used by\n" +
			"default.\n\n" +
			"With --json, the added source is reported as a one-element array whose\n" +
			`state is "pulled", "updated", or "failed".`,
		Example: heredoc(`
			# Add a source by owner/repo (default branch, default glob)
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
			pathOrGlob := glob
			if pathOrGlob == "" {
				pathOrGlob = pathAlias // accept the deprecated --path
			}
			s, err := buildSource(spec, repo, ref, pathOrGlob, token)
			if err != nil {
				return err
			}
			return newApp().Add(s, asJSON)
		},
	}
	c.Flags().StringVar(&repo, "repo", "", "Source repository (`owner/repo`)")
	c.Flags().StringVar(&ref, "ref", "", "Branch, tag, or commit SHA (default: the repository's default branch)")
	c.Flags().StringVar(&glob, "glob", "", "Glob, file, or directory within the repository (default: **/*.instructions.md)")
	c.Flags().StringVar(&pathAlias, "path", "", "")
	_ = c.Flags().MarkDeprecated("path", "use --glob instead")
	c.Flags().StringVar(&token, "token", "", "Token with Contents: read for a private repository (default: your gh auth)")
	c.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return c
}

func pullCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "pull [<id | owner/repo>]",
		Short: "Pull all configured sources, or just one",
		Long: "Pull all configured sources, or just the one matching the given id or\n" +
			"owner/repo.\n\n" +
			"With --json, each source is reported with a state of \"pulled\",\n" +
			"\"updated\" (its commit moved), or \"failed\".",
		Example: heredoc(`
			# Pull every configured source
			$ gh copilot-instructions pull

			# Pull just one source by id or owner/repo
			$ gh copilot-instructions pull github/team-instructions

			# List the repositories whose commit changed on this pull
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
		Short: "List configured sources and their pulled state",
		Long: "List configured sources and their pulled state.\n\n" +
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
			if asJSON && raw {
				return fmt.Errorf("cannot use --json and --raw together")
			}
			return newApp().RenderList(asJSON, raw)
		},
	}
	c.Flags().BoolVar(&raw, "raw", false, "Output config-file lines to paste into a Codespaces secret")
	c.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	return c
}

func removeCmd() *cobra.Command {
	var all, asJSON bool
	c := &cobra.Command{
		Use:   "remove [<id | owner/repo>]",
		Short: "Remove one source (and prune its files), or --all",
		Long: "Remove one source (and prune the files it installed), or use --all to\n" +
			"remove every source, all installed files, and the local config.\n\n" +
			"With --json, the remaining sources are reported (like list --json).",
		Example: heredoc(`
			# Remove one source and prune its files
			$ gh copilot-instructions remove github/team-instructions

			# Remove everything this extension installed
			$ gh copilot-instructions remove --all`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := newApp()
			if all {
				if len(args) > 0 {
					return fmt.Errorf("--all takes no argument")
				}
				return app.RemoveAll(asJSON)
			}
			if len(args) != 1 {
				return fmt.Errorf("specify an <id | owner/repo> to remove, or --all")
			}
			return app.Remove(args[0], asJSON)
		},
	}
	c.Flags().BoolVar(&all, "all", false, "Remove every source, all installed files, and config")
	c.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
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

// buildSource combines an optional positional spec with flag overrides.
func buildSource(spec, repo, ref, path, token string) (gci.Source, error) {
	var s gci.Source
	if spec != "" {
		parsed, err := gci.ParseSpec(spec)
		if err != nil {
			return s, err
		}
		s = parsed
	}
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
		return s, fmt.Errorf("a repo is required: pass owner/repo[:path] or --repo owner/repo")
	}
	if _, err := gci.ParseSpec(s.Repo); err != nil {
		return s, err
	}
	return s, nil
}
