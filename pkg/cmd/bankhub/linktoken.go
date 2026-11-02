package bankhub

import (
	"context"
	"fmt"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// linkToken is a Bank Hub account-linking token; get returns it unwrapped.
type linkToken struct {
	XID                   string             `json:"xid" yaml:"xid"`
	Purpose               string             `json:"purpose" yaml:"purpose"`
	Status                string             `json:"status" yaml:"status"`
	CompletionRedirectURI string             `json:"completion_redirect_uri" yaml:"completion_redirect_uri"`
	IsMobileApp           any                `json:"is_mobile_app" yaml:"is_mobile_app"`
	Language              string             `json:"language" yaml:"language"`
	SessionCount          api.FlexInt        `json:"session_count" yaml:"session_count"`
	CreatedAt             string             `json:"created_at" yaml:"created_at"`
	UpdatedAt             string             `json:"updated_at" yaml:"updated_at"`
	ExpiresAt             string             `json:"expires_at" yaml:"expires_at"`
	Company               linkTokenCompany   `json:"company" yaml:"company"`
	Sessions              []linkTokenSession `json:"sessions" yaml:"sessions"`
	Events                []linkTokenEvent   `json:"events" yaml:"events"`
}

type linkTokenCompany struct {
	XID      string `json:"xid" yaml:"xid"`
	FullName string `json:"full_name" yaml:"full_name"`
	Status   string `json:"status" yaml:"status"`
}

type linkTokenSession struct {
	XID        string `json:"xid" yaml:"xid"`
	Context    string `json:"context" yaml:"context"`
	State      string `json:"state" yaml:"state"`
	Status     string `json:"status" yaml:"status"`
	Result     any    `json:"result" yaml:"result"`
	FinishedAt string `json:"finished_at" yaml:"finished_at"`
	ExpiresAt  string `json:"expires_at" yaml:"expires_at"`
	CreatedAt  string `json:"created_at" yaml:"created_at"`
	UpdatedAt  string `json:"updated_at" yaml:"updated_at"`
}

type linkTokenEvent struct {
	XID       string `json:"xid" yaml:"xid"`
	Name      string `json:"name" yaml:"name"`
	Metadata  any    `json:"metadata" yaml:"metadata"`
	CreatedAt string `json:"created_at" yaml:"created_at"`
}

// linkTokenPurposes are the accepted purposes (BhLinkTokenModel).
var linkTokenPurposes = []string{
	"LINK_BANK_ACCOUNT", "UNLINK_BANK_ACCOUNT", "CREATE_VA", "DELETE_VA", "INACTIVE_VA", "ACTIVE_VA",
}

var linkTokenLanguages = []string{"vi", "en"}

// linkTokenCreated is the unwrapped create response.
type linkTokenCreated struct {
	XID           string `json:"xid" yaml:"xid"`
	HostedLinkURL string `json:"hosted_link_url" yaml:"hosted_link_url"`
	LinkToken     string `json:"link_token" yaml:"link_token"`
	ExpiresAt     string `json:"expires_at" yaml:"expires_at"`
}

func newLinkTokenCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{Use: "link-token <command>", Aliases: []string{"lt"}, Short: "Manage account-linking tokens"}
	cmd.AddCommand(
		newLinkTokenCreateCmd(f),
		newLinkTokenGetCmd(f),
		newLinkTokenRevokeCmd(f),
	)
	return cmd
}

func newLinkTokenCreateCmd(f *cmdutil.Factory) *cobra.Command {
	var company, purpose, bankAccount, redirectURI, language string
	var mobile, showToken bool
	cmd := &cobra.Command{
		Use:     "create",
		Aliases: []string{"new"},
		Short:   "Create an account-linking token (returns a hosted-link URL)",
		Long: `Create an account-linking token and print its hosted-link URL: the URL to send
the end user through.

The token value is masked unless you pass --show-token. That value is the only
one "link-token get" accepts, and this response is the only place it is ever
returned. "link-token revoke" takes the xid instead.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmdutil.ValidateChoices(cmd, map[string][]string{
				"purpose":  linkTokenPurposes,
				"language": linkTokenLanguages,
			}); err != nil {
				return err
			}
			company, err := pickCompany(cmd.Context(), f, company, "company")
			if err != nil {
				return err
			}
			purpose, err = f.SelectOrFlag(purpose, "purpose", "Link token purpose", linkTokenPurposes)
			if err != nil {
				return err
			}
			body := map[string]any{"company_xid": company, "purpose": purpose}
			if purpose == "UNLINK_BANK_ACCOUNT" {
				bankAccount, err = f.InputOrFlag(bankAccount, "bank-account", "Bank account xid to unlink")
				if err != nil {
					return err
				}
				body["bank_account_xid"] = bankAccount
			}
			if redirectURI != "" {
				body["completion_redirect_uri"] = redirectURI
			}
			if cmd.Flags().Changed("mobile") {
				body["is_mobile_app"] = boolToFlag(mobile)
			}
			if language != "" {
				body["language"] = language
			}
			return runLinkTokenCreate(cmd.Context(), f, body, showToken)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&company, "company", "", "Company xid the token is for")
	fl.StringVar(&purpose, "purpose", "", "Purpose: "+strings.Join(linkTokenPurposes, "|"))
	fl.StringVar(&bankAccount, "bank-account", "", "Bank account xid (required for UNLINK_BANK_ACCOUNT)")
	fl.StringVar(&redirectURI, "redirect-uri", "", "URL to return to after the hosted flow")
	fl.StringVar(&language, "language", "", "Hosted-flow language: "+strings.Join(linkTokenLanguages, "|")+" (default vi)")
	fl.BoolVar(&mobile, "mobile", false, "Mark the token as a mobile-app flow")
	fl.BoolVar(&showToken, "show-token", false, `Print the token value (masked by default); the only value "link-token get" accepts`)
	// --bank-account is required only for UNLINK_BANK_ACCOUNT.
	return cmdutil.MarkRequired(cmd, "company", "purpose")
}

func runLinkTokenCreate(ctx context.Context, f *cmdutil.Factory, body map[string]any, showToken bool) error {
	if err := f.ConfirmMutation("create link token", false); err != nil {
		return err
	}
	client, err := f.NewBankHubClient()
	if err != nil {
		return err
	}
	resp, err := client.Post(ctx, "/v1/link-token/create", body)
	if err != nil {
		return err
	}
	var lt linkTokenCreated
	if err := resp.Into(&lt); err != nil {
		return err
	}
	// An unexpected shape decodes to zero fields without error.
	if lt.XID == "" || lt.HostedLinkURL == "" {
		return fmt.Errorf("could not read the created link token: the response carried no xid or hosted_link_url")
	}
	// link_token is a live capability, masked in every format unless --show-token.
	if !showToken {
		lt.LinkToken = cmdutil.MaskSecret(lt.LinkToken)
	}

	return f.RenderOutput(lt, func() error {
		f.Printf("%s created link token %s\n", f.SuccessIcon(), lt.XID)
		if showToken {
			f.Printf("Token:    %s\n", lt.LinkToken)
		}
		f.Printf("URL:      %s\nExpires:  %s\n", lt.HostedLinkURL, lt.ExpiresAt)
		return nil
	})
}

func newLinkTokenRevokeCmd(f *cmdutil.Factory) *cobra.Command {
	return cmdutil.Annotate(&cobra.Command{
		Use:   "revoke [link-token-xid]",
		Short: "Revoke a link token by its xid",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			xid, err := f.InputOrArg(cmdutil.FirstArg(args), "a link token xid", "Link token xid")
			if err != nil {
				return err
			}
			return runLinkTokenRevoke(cmd.Context(), f, xid)
		},
	}, map[string]string{
		cmdutil.AnnDestructive: "irreversible",
		cmdutil.AnnConfirms:    "always",
	})
}

func runLinkTokenRevoke(ctx context.Context, f *cmdutil.Factory, xid string) error {
	if err := f.ConfirmMutation("revoke link token "+xid, true); err != nil {
		return err
	}
	client, err := f.NewBankHubClient()
	if err != nil {
		return err
	}
	if _, err := client.Post(ctx, "/v1/link-token/revoke", map[string]string{"link_token_xid": xid}); err != nil {
		return err
	}
	msg := "Link token revoked."
	return f.RenderOutput(map[string]string{"xid": xid, "message": msg}, func() error {
		fmt.Fprintln(f.IOStreams.Out, f.SuccessIcon()+" "+msg)
		return nil
	})
}

func boolToFlag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func newLinkTokenGetCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "get [link-token]",
		Short: "Show a link token by its token value",
		Long: `Show a link token by its token value: what "link-token create --show-token"
prints, not the xid. "link-token revoke" is the verb that takes the xid.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := f.InputOrArg(cmdutil.FirstArg(args), "a link token", "Link token")
			if err != nil {
				return err
			}
			return runLinkTokenGet(cmd.Context(), f, token)
		},
	}
}

func runLinkTokenGet(ctx context.Context, f *cmdutil.Factory, token string) error {
	client, err := f.NewBankHubClient()
	if err != nil {
		return err
	}
	resp, err := client.Post(ctx, "/v1/link-token/get", map[string]string{"link_token": token})
	if err != nil {
		return err
	}
	var lt linkToken
	if err := resp.Into(&lt); err != nil {
		return err
	}
	if lt.XID == "" {
		return fmt.Errorf("could not read the link token: the response carried no xid")
	}

	return f.RenderOutput(lt, func() error {
		f.Printf(
			"XID:      %s\nPurpose:  %s\nStatus:   %s\nLanguage: %s\nCompany:  %s (%s)\nExpires:  %s\nSessions: %d\n",
			lt.XID, lt.Purpose, lt.Status, lt.Language,
			lt.Company.FullName, lt.Company.Status, lt.ExpiresAt, int(lt.SessionCount))
		return nil
	})
}
