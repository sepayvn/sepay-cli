package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// NewCmdSessions builds "sepay auth sessions".
func NewCmdSessions(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List your OAuth sessions held server-side",
		Long: `List the OAuth sessions SePay holds for the active profile's account, so you
can see which id to pass to "sepay auth revoke --token <id>". Token values are
never shown; the session used for this call is marked with "*".

These are server-side sessions, not the profiles stored on this machine; those
are "sepay auth status" (alias "ls").`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSessions(cmd.Context(), f)
		},
	}
	return cmd
}

func runSessions(ctx context.Context, f *cmdutil.Factory) error {
	host, token, err := f.TokenForProfile("")
	if err != nil {
		return err
	}
	sessions, err := f.NewOAuthClient(host).ListTokens(ctx, token)
	if err != nil {
		return authAPIError("list sessions", err)
	}
	if sessions == nil {
		sessions = []auth.TokenSession{} // marshal as [], never null
	}

	// Empty case after the machine formats, so --json still prints [].
	switch f.Global.Output {
	case "json":
		return jsonfmt.RenderJSON(f.IOStreams.Out, sessions)
	case "yaml":
		return jsonfmt.RenderYAML(f.IOStreams.Out, sessions)
	}
	if len(sessions) == 0 {
		fmt.Fprintln(f.IOStreams.ErrOut, "no active sessions")
		return nil
	}
	return renderSessions(f, sessions)
}

func renderSessions(f *cmdutil.Factory, sessions []auth.TokenSession) error {
	t := jsonfmt.NewTable("", "ID", "SCOPES", "CREATED", "LAST USED", "STATUS").Truncatable("SCOPES")
	for _, s := range sessions {
		marker := ""
		if s.Current {
			marker = f.IOStreams.Green("*")
		}
		status := f.IOStreams.Green("active")
		if s.Revoked {
			status = f.IOStreams.Red("revoked")
		}
		t.AddRow(marker, s.ID, jsonfmt.OrDash(strings.Join(s.Scopes, ",")), jsonfmt.OrDash(s.CreatedAt), jsonfmt.OrDash(s.LastUsedAt), status)
	}
	return f.RenderTable(t)
}
