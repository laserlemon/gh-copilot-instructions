package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// applyGHStyle makes the command tree render help the way built-in gh commands
// do: uppercase section headers (USAGE, COMMANDS, FLAGS, EXAMPLES, LEARN MORE),
// a "gh "-prefixed usage line, and a "--help" flag described as "Show help for
// command" (with no -h shorthand). Setting the funcs on the root is enough -
// cobra walks up to the nearest ancestor for a command without its own.
func applyGHStyle(root *cobra.Command) {
	// gh's help flag: no -h shorthand, gh-style description. Defining it here
	// stops cobra from adding its own "-h, --help   help for X".
	root.PersistentFlags().Bool("help", false, "Show help for command")
	// Match gh's command groups (gh pr, gh issue): no auto-generated `help` or
	// `completion` command. Discovery is via `--help` on each command. Replacing
	// the help command with a hidden one makes `help` an unknown command (as in
	// `gh pr help`); DisableDefaultCmd drops the `completion` command.
	root.SetHelpCommand(&cobra.Command{Use: "no-help", Hidden: true})
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetHelpFunc(ghHelp)
	root.SetUsageFunc(ghUsage)
}

// ghHelp prints the full help page (description + usage body).
func ghHelp(c *cobra.Command, _ []string) {
	w := c.OutOrStdout()
	if desc := longOrShort(c); desc != "" {
		fmt.Fprintf(w, "%s\n\n", desc)
	}
	ghUsageBody(w, c)
}

// ghUsage prints just the usage body (no description); used when cobra needs a
// command's usage string on its own.
func ghUsage(c *cobra.Command) error {
	ghUsageBody(c.OutOrStderr(), c)
	return nil
}

func ghUsageBody(w io.Writer, c *cobra.Command) {
	fmt.Fprintf(w, "USAGE\n  gh %s\n", c.UseLine())

	if len(c.Aliases) > 0 {
		fmt.Fprintf(w, "\nALIASES\n  %s\n", c.NameAndAliases())
	}

	subs := visibleSubcommands(c)
	if len(subs) > 0 {
		fmt.Fprint(w, "\nCOMMANDS\n")
		pad := 0
		for _, s := range subs {
			if n := len(s.Name()) + 1; n > pad { // +1 for the trailing colon
				pad = n
			}
		}
		for _, s := range subs {
			fmt.Fprintf(w, "  %-*s  %s\n", pad, s.Name()+":", s.Short)
		}
	}

	if c.HasAvailableLocalFlags() {
		fmt.Fprintf(w, "\nFLAGS\n%s", c.LocalFlags().FlagUsages())
	}
	if c.HasAvailableInheritedFlags() {
		fmt.Fprintf(w, "\nINHERITED FLAGS\n%s", c.InheritedFlags().FlagUsages())
	}

	if c.HasExample() {
		fmt.Fprintf(w, "\nEXAMPLES\n%s\n", c.Example)
	}

	if len(subs) > 0 {
		fmt.Fprintf(w, "\nLEARN MORE\n  Use `gh %s <command> --help` for more information about a command.\n", c.CommandPath())
	}
}

// visibleSubcommands returns the child commands gh would show (skipping hidden
// ones). Like gh's command groups, the auto-generated help/completion commands
// are not shown (see applyGHStyle).
func visibleSubcommands(c *cobra.Command) []*cobra.Command {
	var subs []*cobra.Command
	for _, s := range c.Commands() {
		if s.IsAvailableCommand() {
			subs = append(subs, s)
		}
	}
	return subs
}

func longOrShort(c *cobra.Command) string {
	if c.Long != "" {
		return c.Long
	}
	return c.Short
}
