package root

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/internal/bankhub"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/internal/einvoice"
	"github.com/sepayvn/sepay-cli/internal/pg"
	"github.com/sepayvn/sepay-cli/internal/product"
	"github.com/sepayvn/sepay-cli/pkg/cmd/docs"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fencedBash = regexp.MustCompile("(?s)```bash\n(.*?)```")

var runnableTopics = []string{"overview", "flows", "api"}

func documentedCommands(t *testing.T) []string {
	t.Helper()
	var all []string
	for _, topic := range runnableTopics {
		all = append(all, flowCommands(t, topic)...)
	}
	return all
}

func flowCommands(t *testing.T, topic string) []string {
	t.Helper()
	body, err := docs.Topic(topic)
	require.NoError(t, err)

	var cmds []string
	for _, block := range fencedBash.FindAllStringSubmatch(string(body), -1) {
		var current string
		for _, line := range strings.Split(block[1], "\n") {
			if i := strings.Index(line, " #"); i >= 0 {
				line = line[:i]
			}
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasSuffix(line, "\\") {
				current += strings.TrimSuffix(line, "\\") + " "
				continue
			}
			current += line
			if strings.HasPrefix(current, "sepay ") {
				cmds = append(cmds, current)
			}
			current = ""
		}
	}
	return cmds
}

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

// An agent trusts a documented flag; one the CLI never had sends it chasing its own input.
func TestDocsFlows_EveryCommandParses(t *testing.T) {
	cmds := documentedCommands(t)
	require.NotEmpty(t, cmds)

	for _, line := range cmds {
		t.Run(line, func(t *testing.T) {
			root, _ := newTestRoot(t)
			fields := tokenize(line)[1:] // drop "sepay"

			target, rest, err := root.Find(fields)
			require.NoError(t, err, "command path does not resolve")
			require.NoError(t, target.ParseFlags(rest), "flag does not exist or rejects its value")
			assert.NoError(t, target.ValidateArgs(target.Flags().Args()), "wrong positional arguments")
			assert.NoError(t, target.ValidateRequiredFlags(), "a required flag is missing")
		})
	}
}

// Some requirements resolve in RunE, so run each command with clients stubbed; a usage error is a doc defect.
func TestDocsFlows_NoUsageErrors(t *testing.T) {
	stub := func(f *cmdutil.Factory) {
		fail := errors.New("no backend in tests")
		f.Config = func() (*config.Config, error) { return nil, fail }
		f.Credentials = func() (*auth.Manager, error) { return nil, fail }
		f.NewAPIClient = func() (*api.Client, error) { return nil, fail }
		f.NewPGClient = func() (*pg.Client, error) { return nil, fail }
		f.NewBankHubClient = func() (*bankhub.Client, error) { return nil, fail }
		f.NewEInvoiceClient = func() (*einvoice.Client, error) { return nil, fail }
		// `bankhub auth` reaches the broker directly, past the client stubs.
		f.ProductManager = func() (*product.Manager, error) { return nil, fail }
	}

	for _, line := range documentedCommands(t) {
		// login drives the device flow, so only the parse test covers it.
		if strings.HasPrefix(line, "sepay login") {
			continue
		}
		t.Run(line, func(t *testing.T) {
			ios, _, _, _ := iostreams.Test()
			f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
			stub(f)
			cmd := NewCmdRoot(f)
			cmd.SetArgs(tokenize(line)[1:])

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := cmd.ExecuteContext(ctx)
			assert.NotEqual(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err),
				"documented command is rejected as misuse: %v", err)
		})
	}
}

func TestDocsGlossary_EveryCommandParses(t *testing.T) {
	for _, line := range flowCommands(t, "glossary") {
		t.Run(line, func(t *testing.T) {
			root, _ := newTestRoot(t)
			fields := tokenize(line)[1:]
			target, rest, err := root.Find(fields)
			require.NoError(t, err)
			require.NoError(t, target.ParseFlags(rest))
		})
	}
}
