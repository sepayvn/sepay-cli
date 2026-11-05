package root

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func topicBody(t *testing.T, name string) string {
	t.Helper()
	body, err := topicFiles.ReadFile("topics/" + name + ".md")
	require.NoError(t, err)
	return string(body)
}

func TestHelpTopic_PrintsTheTopicLoggedOut(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	for _, topic := range helpTopics {
		for _, args := range [][]string{{"help", topic.name}, {topic.name}, {topic.name, "--help"}} {
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				root, out := newTestRoot(t)
				root.SetArgs(args)
				require.NoError(t, root.ExecuteContext(context.Background()))
				assert.Equal(t, topicBody(t, topic.name), out.String())
			})
		}
	}
}

// A script asking for a topic that does not exist must fail, not read root help on stdout as the answer.
func TestHelpTopic_UnknownTopicIsAUsageError(t *testing.T) {
	for _, args := range [][]string{{"help", "nope"}, {"help", "webhook", "nope"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root, out := newTestRoot(t)
			root.SetArgs(args)
			err := root.ExecuteContext(context.Background())
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), "topics: environment, errors, exit-codes, glossary")
			assert.Empty(t, out.String())
		})
	}
}

func TestHelpTopic_IsNotACommand(t *testing.T) {
	root, _ := newTestRoot(t)
	for _, topic := range helpTopics {
		c, _, err := root.Find([]string{topic.name})
		require.NoError(t, err)
		assert.True(t, c.IsAdditionalHelpTopicCommand(), "%s must stay out of the command list and --map", topic.name)
	}
}

func TestHelpTopic_CompletesTopicsAndCommands(t *testing.T) {
	root, _ := newTestRoot(t)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"__complete", "help", ""})
	require.NoError(t, root.ExecuteContext(context.Background()))
	for _, want := range []string{"glossary\t", "exit-codes\t", "webhook\t"} {
		assert.Contains(t, out.String(), want)
	}
}

var inlineCommand = regexp.MustCompile("`(sepay [^`]+)`")

// A topic that names a command the CLI does not have sends the reader after nothing.
func TestHelpTopic_EveryNamedCommandExists(t *testing.T) {
	root, _ := newTestRoot(t)
	for _, topic := range helpTopics {
		for _, m := range inlineCommand.FindAllStringSubmatch(topicBody(t, topic.name), -1) {
			var path []string
			for _, field := range strings.Fields(m[1])[1:] {
				if strings.HasPrefix(field, "-") || strings.ContainsAny(field, "<[|") {
					break
				}
				path = append(path, field)
			}
			if len(path) == 0 {
				continue
			}
			found, rest, err := root.Find(path)
			if err == nil && len(rest) > 0 && found.HasSubCommands() {
				err = fmt.Errorf("no subcommand %q", rest[0])
			}
			assert.NoError(t, err, "%s: `%s`", topic.name, m[1])
		}
	}
}

func TestHelpTopic_ExitCodesCoverEveryCode(t *testing.T) {
	body := topicBody(t, "exit-codes")
	for _, code := range []cmdutil.ExitCode{
		cmdutil.ExitOK, cmdutil.ExitError, cmdutil.ExitUsage, cmdutil.ExitNotImplemented, cmdutil.ExitAuth,
		cmdutil.ExitScope, cmdutil.ExitForward, cmdutil.ExitOTP, cmdutil.ExitBlocked, cmdutil.ExitInterrupted,
	} {
		assert.Contains(t, body, fmt.Sprintf("| %d |", code))
	}
}

func TestHelpTopic_GlossaryDefinesTheOpaqueNouns(t *testing.T) {
	body := topicBody(t, "glossary")
	for _, term := range []string{"paycode", "VA (virtual account)", "quota", "bankhub", "settlement", "xid"} {
		assert.Contains(t, body, term)
	}
}
