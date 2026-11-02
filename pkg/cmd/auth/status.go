package auth

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

type statusOptions struct {
	activeOnly bool
}

// NewCmdStatus builds "sepay auth status".
func NewCmdStatus(f *cmdutil.Factory) *cobra.Command {
	opts := statusOptions{}
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"st"},
		Short:   "View stored profiles and the active one",
		Long: `List every stored profile (host, granted scopes, active marker) and which
one is active, with what each one's stored session is still good for. Tokens are
never shown.

The session column is read from the stored credential, without asking the server.
A session reading "expired, renews on next use" needs nothing from you: the next
command exchanges the refresh token for a new one. A session revoked from another
place still reads "ok" until a command is refused; the CLI then renews it, or marks
it "revoked, run: sepay login" when the server has ended it.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runStatus(f, &opts)
		},
	}
	cmd.Flags().BoolVar(&opts.activeOnly, "active", false, "Show only the active profile")
	return cmd
}

type profileStatus struct {
	Profile string   `json:"profile"`
	Host    string   `json:"host"`
	Email   string   `json:"email,omitempty"`
	Scopes  []string `json:"scopes,omitempty"`
	Active  bool     `json:"active"`
	// one of the session constants
	Session string `json:"session"`
}

// Session states. An expired access token with a refresh token renews transparently, so it is its own state.
const (
	sessionOK      = "ok"
	sessionRenewal = "expired, renews on next use"
	sessionDead    = "expired, run: sepay login"
	sessionRevoked = "revoked, run: sepay login"
	sessionUnknown = "unknown"
)

func runStatus(f *cmdutil.Factory, opts *statusOptions) error {
	mgr, err := f.Credentials()
	if err != nil {
		return err
	}
	hosts := mgr.Hosts()

	rows := collectStatus(mgr, hosts, opts.activeOnly)
	if len(rows) == 0 {
		return cmdutil.AuthError{Err: errors.New("not logged in to any profile, run: sepay login")}
	}

	switch f.Global.Output {
	case "json":
		return jsonfmt.RenderJSON(f.IOStreams.Out, rows)
	case "yaml":
		return jsonfmt.RenderYAML(f.IOStreams.Out, rows)
	}
	if err := renderStatusTable(f, rows); err != nil {
		return err
	}
	// After a logout no row is marked; without this line the table reads as healthy.
	if hosts.Active == "" {
		fmt.Fprintln(f.IOStreams.ErrOut, "no active profile; run: sepay auth switch <profile>")
	}
	return nil
}

func collectStatus(mgr *auth.Manager, hosts *config.Hosts, activeOnly bool) []profileStatus {
	var rows []profileStatus
	for _, name := range sortedProfileNames(hosts) {
		if activeOnly && name != hosts.Active {
			continue
		}
		p := hosts.Profiles[name]
		rows = append(rows, profileStatus{
			Profile: name,
			Host:    cmdutil.DisplayHost(p.Host),
			Email:   p.Email,
			Scopes:  p.Scopes,
			Active:  name == hosts.Active,
			Session: sessionState(mgr, name),
		})
	}
	return rows
}

// sessionState reads the keychain; --map=json skips it because it runs on every --help.
func sessionState(mgr *auth.Manager, profile string) string {
	cred, err := mgr.Get(profile)
	if err != nil {
		return sessionUnknown
	}
	switch {
	case cred.Revoked():
		return sessionRevoked
	case !cred.Expired():
		return sessionOK
	case cred.RefreshToken != "":
		return sessionRenewal
	default:
		return sessionDead
	}
}

func renderStatusTable(f *cmdutil.Factory, rows []profileStatus) error {
	t := jsonfmt.NewTable("", "PROFILE", "EMAIL", "HOST", "SESSION", "SCOPES").Truncatable("EMAIL", "SCOPES")
	for _, r := range rows {
		marker := ""
		if r.Active {
			marker = f.IOStreams.Green("*")
		}
		session := r.Session
		if r.Session == sessionDead {
			session = f.IOStreams.Red(session)
		}
		t.AddRow(marker, r.Profile, jsonfmt.OrDash(r.Email), r.Host, session,
			jsonfmt.OrDash(strings.Join(r.Scopes, ",")))
	}
	return f.RenderTable(t)
}
