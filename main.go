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
		Use:   "copilot-instructions",
		Short: "Sync your Copilot custom instructions to every coding surface",
		Long: "Pull your Copilot custom instructions from one or more repos into\n" +
			"~/.copilot/instructions/, where Copilot CLI, VS Code, and the GitHub\n" +
			"Copilot desktop app all read them automatically — no per-repo setup.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(addCmd(), pullCmd(), listCmd(), removeCmd())
	return root
}

func newApp() *gci.App { return gci.New(os.Stdout, os.Stderr) }

func addCmd() *cobra.Command {
	var repo, ref, path, token string
	c := &cobra.Command{
		Use:   "add [<owner/repo[@ref][:path]>]",
		Short: "Add a source and pull it",
		Long: "Add a source, then pull. Provide a positional spec, or use flags, or\n" +
			"mix them (a flag overrides the matching part of the spec). Quote a glob path.",
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
			return newApp().Add(s)
		},
	}
	c.Flags().StringVar(&repo, "repo", "", "Source repository (owner/repo)")
	c.Flags().StringVar(&ref, "ref", "", "Branch, tag, or commit SHA (default: the repo's default branch)")
	c.Flags().StringVar(&path, "path", "", "Glob/file/dir within the repo (default: **/*.instructions.md)")
	c.Flags().StringVar(&token, "token", "", "Token for a private source (default: your gh auth)")
	return c
}

func pullCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "pull [<id | owner/repo>]",
		Short: "Pull all configured sources, or just one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filter := ""
			if len(args) == 1 {
				filter = args[0]
			}
			return newApp().Pull(filter)
		},
	}
	return c
}

func listCmd() *cobra.Command {
	var asJSON, raw bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List configured sources and their pulled state",
		Long: "List configured sources and their pulled state.\n\n" +
			"Use --raw to print the sources in config-file format (one per line,\n" +
			"including any inline tokens) — ready to paste into the multiline\n" +
			"GH_COPILOT_INSTRUCTIONS Codespaces secret.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if asJSON && raw {
				return fmt.Errorf("--json and --raw are mutually exclusive")
			}
			return newApp().RenderList(asJSON, raw)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	c.Flags().BoolVar(&raw, "raw", false, "Output config-file lines to paste into a Codespaces secret")
	return c
}

func removeCmd() *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use:   "remove [<id | owner/repo>]",
		Short: "Remove one source (and prune its files), or --all",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := newApp()
			if all {
				if len(args) > 0 {
					return fmt.Errorf("--all takes no argument")
				}
				return app.RemoveAll()
			}
			if len(args) != 1 {
				return fmt.Errorf("specify an <id | owner/repo> to remove, or --all")
			}
			return app.Remove(args[0])
		},
	}
	c.Flags().BoolVar(&all, "all", false, "Remove every source, all installed files, and config")
	return c
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
