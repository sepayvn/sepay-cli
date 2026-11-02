package docs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	ios, _, out, _ := iostreams.Test()
	cmd := NewCmdDocs(&cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}})
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestDocs_ListsTopics(t *testing.T) {
	out, err := run(t)
	require.NoError(t, err)
	for _, topic := range []string{"api", "glossary", "flows", "exit-codes", "errors"} {
		assert.Contains(t, out, topic)
	}
}

func TestDocs_ListsTopicsAsJSON(t *testing.T) {
	ios, _, out, _ := iostreams.Test()
	cmd := NewCmdDocs(&cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "json"}})
	cmd.SetArgs(nil)
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	var got []string
	require.NoError(t, json.Unmarshal(out.Bytes(), &got), out.String())
	assert.Equal(t, topics(), got)
}

func TestDocs_OverviewPositionsTheTool(t *testing.T) {
	out, err := run(t, "overview")
	require.NoError(t, err)
	for _, want := range []string{
		"Vietnamese payment platform",
		"What you can do with it",
		"Which path is yours",
		"Not a runtime dependency",
	} {
		assert.Contains(t, out, want)
	}
}

func TestDocs_GlossaryDefinesTheOpaqueNouns(t *testing.T) {
	out, err := run(t, "glossary")
	require.NoError(t, err)
	for _, term := range []string{"paycode", "VA (virtual account)", "quota", "bankhub", "settlement", "xid"} {
		assert.Contains(t, out, term)
	}
}

func TestDocs_ExitCodesCoverEveryCode(t *testing.T) {
	out, err := run(t, "exit-codes")
	require.NoError(t, err)
	for _, code := range []string{"| 0 |", "| 1 |", "| 2 |", "| 3 |", "| 4 |", "| 5 |", "| 6 |", "| 7 |"} {
		assert.Contains(t, out, code)
	}
}

func TestDocs_UnknownTopicIsAUsageError(t *testing.T) {
	_, err := run(t, "nope")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "available:")
}

func TestDocs_RejectsPathTraversal(t *testing.T) {
	for _, name := range []string{"../glossary", "../../go.mod", "content/glossary"} {
		_, err := run(t, name)
		require.Error(t, err, name)
		assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	}
}

func TestDocs_TopicsAreNonEmptyMarkdown(t *testing.T) {
	for _, topic := range topics() {
		out, err := run(t, topic)
		require.NoError(t, err, topic)
		assert.True(t, strings.HasPrefix(out, "# "), "%s should open with a heading", topic)
		assert.Greater(t, len(out), 400, "%s looks like a placeholder", topic)
	}
}

func TestDocs_WebhookPayloadCoversTheHandlerContract(t *testing.T) {
	out, err := run(t, "webhook-payload")
	require.NoError(t, err)
	for _, want := range []string{
		"transferAmount", "subAccount", "referenceCode", "accumulated",
		"X-SePay-Signature", "X-SePay-Timestamp",
		"Deduplicate on `id`",
	} {
		assert.Contains(t, out, want)
	}
}
