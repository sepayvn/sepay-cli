package einvoice

import (
	"context"
	"time"

	"github.com/sepayvn/sepay-cli/internal/product"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// authView is the printed credential status; it never carries a token.
type authView struct {
	Product   string `json:"product" yaml:"product"`
	Env       string `json:"env" yaml:"env"`
	AuthType  string `json:"auth_type" yaml:"auth_type"`
	ExpiresAt string `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
}

func newAuthCmd(f *cmdutil.Factory) *cobra.Command {
	var refresh, insecure bool
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Show or refresh the sandbox E-Invoice credential",
		Long: "Mint (or reuse) the sandbox E-Invoice Bearer credential via the SePay broker.\n" +
			"einvoice commands other than invoice list and get mint it automatically; run this to inspect it,\n" +
			"pre-warm CI, or force a refresh. The token is never printed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuth(cmd.Context(), f, refresh, insecure)
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&refresh, "refresh", false, "Force a fresh credential from the broker")
	fl.BoolVar(&insecure, "insecure-storage", false, "Store the credential in a 0600 file instead of the OS keychain")
	return cmd
}

func runAuth(ctx context.Context, f *cmdutil.Factory, refresh, insecure bool) error {
	mgr, err := f.ProductManager()
	if err != nil {
		return err
	}
	if insecure {
		mgr.SetInsecure(true)
	}

	var cred *product.Credential
	if refresh {
		cred, err = mgr.Refresh(ctx, product.EInvoice)
	} else {
		cred, err = mgr.Get(ctx, product.EInvoice)
	}
	if err != nil {
		return err
	}
	if mgr.UsedFile() {
		f.WarnPlaintextStorage("the E-Invoice credential", mgr.StorePath(), false)
	}

	view := authView{Product: product.EInvoice, Env: mgr.Env(), AuthType: cred.AuthType}
	if !cred.ExpiresAt.IsZero() {
		view.ExpiresAt = cred.ExpiresAt.Format(time.RFC3339)
	}

	return f.RenderOutput(view, func() error {
		t := jsonfmt.NewTable("PRODUCT", "ENV", "AUTH", "EXPIRES")
		t.AddRow(view.Product, view.Env, view.AuthType, jsonfmt.OrDash(view.ExpiresAt))
		return f.RenderTable(t)
	})
}
