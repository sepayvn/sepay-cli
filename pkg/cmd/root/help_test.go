package root

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func walk(c *cobra.Command, fn func(*cobra.Command)) {
	fn(c)
	for _, child := range c.Commands() {
		walk(child, fn)
	}
}

// pflag renders a backquoted word in a usage string as the flag's value name, so
// "sent as `Authorization: Apikey`" printed "--api-key Authorization: Apikey" in help.
func TestHelp_FlagValueNamesAreTypeNames(t *testing.T) {
	allowed := map[string]bool{
		"": true, "string": true, "strings": true, "stringArray": true,
		"int": true, "duration": true, "float": true, "uint": true,
	}
	root, _ := newTestRoot(t)
	walk(root, func(c *cobra.Command) {
		check := func(fl *pflag.Flag) {
			name, _ := pflag.UnquoteUsage(fl)
			assert.True(t, allowed[name], "%s --%s renders value name %q; drop the backquotes from its usage",
				c.CommandPath(), fl.Name, name)
		}
		c.LocalFlags().VisitAll(check)
		c.InheritedFlags().VisitAll(check)
	})
}

func renderHelp(t *testing.T, args ...string) string {
	t.Helper()
	cmd, out := newTestRoot(t)
	cmd.SetOut(out)
	cmd.SetArgs(append(args, "--help"))
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	return out.String()
}

// A reader of --help looks for flags first; examples and the guidance block used to push them off the first screen.
func TestHelp_FlagsComeBeforeAgentGuidance(t *testing.T) {
	for _, args := range [][]string{{"apikey", "get"}, {"webhook", "create"}, {"webhook"}, {}} {
		t.Run(strings.Join(append([]string{"sepay"}, args...), " "), func(t *testing.T) {
			help := renderHelp(t, args...)
			flags := strings.Index(help, "\nFlags:\n")
			guidance := strings.Index(help, "[Agent guidance]")
			require.NotEqual(t, -1, flags)
			require.NotEqual(t, -1, guidance)
			assert.Less(t, flags, guidance)
		})
	}
}

func TestHelp_SubcommandsCarryOneGuidancePointer(t *testing.T) {
	sub := renderHelp(t, "webhook", "create")
	assert.Contains(t, sub, moreGuidance)
	for _, line := range baseGuidance {
		assert.NotContains(t, sub, line)
	}
	assert.Contains(t, sub, "Active env: sandbox")
	assert.Contains(t, sub, `needs the "webhook:write" scope`)

	head := strings.Join(strings.SplitN(sub, "\n", 16)[:15], "\n")
	assert.Contains(t, head, "\nFlags:\n", "Flags: must start within the first 15 lines")

	root := renderHelp(t)
	for _, line := range baseGuidance {
		assert.Contains(t, root, line)
	}
	assert.NotContains(t, root, moreGuidance)
}

var placeholder = regexp.MustCompile(`[<\[]([^>\]]*)[>\]]`)

// Positional placeholders name their noun in kebab-case; one the CLI prompts for when it is
// missing is written <required> and the Long says what happens without a terminal.
func TestHelp_PositionalPlaceholders(t *testing.T) {
	kebab := regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*(\|[a-z][a-z0-9]*(-[a-z0-9]+)*)*$`)
	bare := map[string]bool{"xid": true, "id": true, "ref": true}
	root, _ := newTestRoot(t)
	walk(root, func(c *cobra.Command) {
		if !c.HasParent() || !c.IsAvailableCommand() {
			return
		}
		_, argsPart, _ := strings.Cut(c.Use, " ")
		for _, m := range placeholder.FindAllStringSubmatch(argsPart, -1) {
			if m[1] == "command" {
				continue
			}
			assert.Regexp(t, kebab, m[1], "%s: placeholder %q", c.CommandPath(), m[0])
			assert.False(t, bare[m[1]], "%s: placeholder %q does not name its noun", c.CommandPath(), m[0])
			if strings.HasPrefix(m[0], "<") && c.Args != nil && c.Args(c, nil) == nil {
				assert.Contains(t, strings.ToLower(c.Long), "prompted for on a terminal",
					"%s accepts no argument, so its Long must say when %s is asked for", c.CommandPath(), m[0])
			}
		}
	})
}

func TestRoot_EveryCommandHasAGroup(t *testing.T) {
	root, _ := newTestRoot(t)
	for _, c := range root.Commands() {
		if c.IsAdditionalHelpTopicCommand() {
			continue
		}
		assert.NotEmpty(t, c.GroupID, "%s has no help group", c.Name())
	}
	help := renderHelp(t)
	assert.NotContains(t, help, "Additional Commands:")
	for _, title := range []string{"Core Commands:", "Product Commands:", "Account Commands:", "Utility Commands:"} {
		assert.Contains(t, help, "\n"+title+"\n")
	}
}

// A command with a positional or three flags is not self-explanatory from its Short.
func TestHelp_NonTrivialCommandsHaveLong(t *testing.T) {
	root, _ := newTestRoot(t)
	walk(root, func(c *cobra.Command) {
		if !c.HasParent() || !c.IsAvailableCommand() || c.HasAvailableSubCommands() {
			return
		}
		flags := 0
		c.LocalNonPersistentFlags().VisitAll(func(fl *pflag.Flag) {
			if !fl.Hidden && fl.Name != "help" {
				flags++
			}
		})
		_, positional := strings.CutPrefix(c.Use, c.Name()+" ")
		if flags >= 3 || positional {
			assert.NotEmpty(t, c.Long, "%s (%d flags) needs a Long", c.CommandPath(), flags)
		}
	})
}

var shellOperator = regexp.MustCompile(`\s[|<>]\s`)

// exampleCommands returns every sepay invocation in a command's Example, cut before a pipe or redirect.
func exampleCommands(example string) []string {
	var cmds []string
	for _, line := range strings.Split(example, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "sepay "); i >= 0 && !strings.HasPrefix(line, "#") {
			line = line[i:]
			if loc := shellOperator.FindStringIndex(line); loc != nil {
				line = line[:loc[0]]
			}
			cmds = append(cmds, strings.TrimSpace(line))
		}
	}
	return cmds
}

// An Example is copied verbatim; one naming a flag or command the CLI lacks fails on first use.
func TestHelp_EveryExampleParses(t *testing.T) {
	root, _ := newTestRoot(t)
	var lines []string
	walk(root, func(c *cobra.Command) { lines = append(lines, exampleCommands(c.Example)...) })
	require.GreaterOrEqual(t, len(lines), 40)

	for _, line := range lines {
		t.Run(line, func(t *testing.T) {
			root, _ := newTestRoot(t)
			target, rest, err := root.Find(tokenize(line)[1:])
			require.NoError(t, err, "command path does not resolve")
			require.NoError(t, target.ParseFlags(rest), "flag does not exist or rejects its value")
			assert.NoError(t, target.ValidateArgs(target.Flags().Args()), "wrong positional arguments")
			assert.NoError(t, target.ValidateRequiredFlags(), "a required flag is missing")
		})
	}
}

func TestHelp_EntryCommandsHaveExamples(t *testing.T) {
	root, _ := newTestRoot(t)
	for _, path := range []string{
		"webhook listen", "sandbox init", "auth login", "login", "va issue", "va list", "transaction list",
		"pg checkout init", "webhook update", "webhook create", "webhook list", "bank-account list",
		"bank-account create", "apikey list", "apikey create", "paycode list", "paycode create", "completion", "config set",
	} {
		c, _, err := root.Find(strings.Fields(path))
		require.NoError(t, err)
		assert.NotEmpty(t, c.Example, "sepay %s needs an Example", path)
	}
	// Every list, create, issue and init one level under a noun, including ones added later.
	walk(root, func(c *cobra.Command) {
		entry := map[string]bool{"list": true, "create": true, "issue": true, "init": true}
		if c.Hidden || !entry[c.Name()] || c.Parent() == nil || c.Parent().Parent() != root {
			return
		}
		assert.NotEmpty(t, c.Example, "%s needs an Example", c.CommandPath())
	})
}

// Help that tells the reader to run a sandbox-only command on production sends them into exit 2.
func TestHelp_NeverPointsASandboxOnlyCommandAtProduction(t *testing.T) {
	root, _ := newTestRoot(t)
	var sandboxOnly []string
	walk(root, func(c *cobra.Command) {
		if cmdutil.Annotation(c, cmdutil.AnnEnvs) == "sandbox" {
			sandboxOnly = append(sandboxOnly, c.CommandPath())
		}
	})
	require.Contains(t, sandboxOnly, "sepay webhook listen")

	walk(root, func(c *cobra.Command) {
		text := c.Long + "\n" + c.Example + "\n" + cmdutil.Annotation(c, cmdutil.AnnGuidance)
		for _, line := range strings.Split(text, "\n") {
			if !strings.Contains(line, "--env production") {
				continue
			}
			for _, path := range sandboxOnly {
				assert.NotContains(t, line, path+" ", "%s help: %q", c.CommandPath(), line)
			}
		}
	})
}

// tokenize splits a shell line on spaces outside double quotes.
func tokenize(line string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range line {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ' ' && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
