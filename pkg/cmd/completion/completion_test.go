package completion

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var completionHeading = regexp.MustCompile(`(?m)^## .*completion.*$`)

func installLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "sepay completion ") || strings.HasPrefix(line, "mkdir ") {
			out = append(out, line)
		}
	}
	return out
}

// The help and both READMEs give one set of install steps; they drifted apart once.
func TestCompletion_HelpMatchesREADME(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	help := installLines(NewCmdCompletion(&cmdutil.Factory{IOStreams: ios}).Long)
	require.NotEmpty(t, help)
	for _, readme := range []string{"README.md", "README.en.md"} {
		body, err := os.ReadFile("../../../" + readme)
		require.NoError(t, err)
		text := string(body)
		loc := completionHeading.FindStringIndex(text)
		require.NotNil(t, loc, readme)
		block := text[loc[1]:]
		end := strings.Index(block, "\n## ")
		require.GreaterOrEqual(t, end, 0, readme)
		block = block[:end]
		assert.Equal(t, help, installLines(block), readme)
	}
	assert.NotContains(t, NewCmdCompletion(&cmdutil.Factory{IOStreams: ios}).Long, "source <(")
}
