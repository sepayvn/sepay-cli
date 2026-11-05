package root

import (
	"context"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/bankhub"
	"github.com/sepayvn/sepay-cli/internal/einvoice"
	"github.com/sepayvn/sepay-cli/internal/pg"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const goodXID = "11111111-2222-4333-8444-555555555555"

// Defeats e.g. `va revoke .. --bank-account X --yes` becoming DELETE /bank-accounts/X.
func TestPathSegment_EveryIDCommandRefusesBeforeAnyRequest(t *testing.T) {
	commands := [][]string{
		{"transaction", "get", "ID"},
		{"bank-account", "get", "ID"},
		{"bank-account", "update", "ID", "--label", "x"},
		{"bank-account", "delete", "ID", "--yes"},
		{"va", "list", "--bank-account", "ID"},
		{"va", "issue", "--bank-account", "ID"},
		{"va", "get", "ID", "--bank-account", goodXID},
		{"va", "get", goodXID, "--bank-account", "ID"},
		{"va", "revoke", "ID", "--bank-account", goodXID, "--yes"},
		{"va", "revoke", goodXID, "--bank-account", "ID", "--yes"},
		{"webhook", "get", "ID"},
		{"webhook", "update", "ID", "--name", "x"},
		{"webhook", "delete", "ID", "--yes"},
		{"apikey", "get", "ID"},
		{"apikey", "update", "ID", "--name", "x"},
		{"apikey", "delete", "ID", "--yes"},
		{"pg", "order", "get", "ID"},
		{"bankhub", "company", "get", "ID"},
		{"bankhub", "company", "update", "ID", "--name", "x", "--yes"},
		{"bankhub", "bank-account", "get", "ID"},
		{"bankhub", "transaction", "get", "ID"},
		{"einvoice", "account", "get", "ID"},
		{"einvoice", "invoice", "get", "ID"},
		{"einvoice", "invoice", "create-status", "ID"},
		{"einvoice", "invoice", "issue-status", "ID"},
	}
	bad := []string{"", ".", "..", "%2e%2e", "%2E.", "a/b", "a%2Fb", `a\b`}

	for _, tmpl := range commands {
		for _, id := range bad {
			args := make([]string, len(tmpl))
			for i, a := range tmpl {
				if a == "ID" {
					a = id
				}
				args[i] = a
			}
			t.Run(strings.Join(tmpl, " ")+" "+id, func(t *testing.T) {
				ios, _, _, _ := iostreams.Test()
				f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Env: "sandbox"}}
				reached := func(what string) { t.Errorf("built the %s client for id %q", what, id) }
				f.NewAPIClient = func() (*api.Client, error) { reached("API"); return nil, assert.AnError }
				f.NewPGClient = func() (*pg.Client, error) { reached("PG"); return nil, assert.AnError }
				f.NewBankHubClient = func() (*bankhub.Client, error) { reached("Bank Hub"); return nil, assert.AnError }
				f.NewEInvoiceClient = func() (*einvoice.Client, error) { reached("eInvoice"); return nil, assert.AnError }

				cmd := NewCmdRoot(f)
				cmd.SetArgs(append([]string{"--no-input"}, args...))
				err := cmd.ExecuteContext(context.Background())
				require.Error(t, err)
				assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err), err.Error())
			})
		}
	}
}
