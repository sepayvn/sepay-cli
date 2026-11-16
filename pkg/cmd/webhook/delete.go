package webhook

import (
	"context"
	"fmt"
	"net/http"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

func newDeleteCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "delete <webhook-xid>",
		Aliases: []string{"rm", "del"},
		Short:   "Delete a webhook",
		Long: `Delete a webhook; SePay stops delivering to it.

<webhook-xid> is the ID column of "sepay webhook list". Prompted for on a terminal; required otherwise.`,
		Example: `  sepay webhook delete <webhook-xid> --yes`,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := f.CheckProdWrite("delete webhook", cmdutil.FirstArg(args)); err != nil {
				return err
			}
			xid, err := pickWebhook(cmd.Context(), f, cmdutil.FirstArg(args))
			if err != nil {
				return err
			}
			return runDelete(cmd.Context(), f, xid)
		},
	}
	cmdutil.AddDryRunFlag(cmd, f)
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnScope:       "webhook:write",
		cmdutil.AnnDestructive: "irreversible",
		cmdutil.AnnConfirms:    "sandbox=always,production=terminal",
	})
}

func runDelete(ctx context.Context, f *cmdutil.Factory, xid string) error {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return err
	}
	if err := f.ConfirmMutation(fmt.Sprintf("delete webhook %s", xid), xid, true); err != nil {
		return err
	}

	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	if f.Global.DryRun {
		stored, err := fetchWebhook(ctx, client, xid)
		if err != nil {
			return err
		}
		before := stored.redacted()
		return f.RenderDryRun(cmdutil.DryRun{
			Request: cmdutil.DryRunRequest{Method: http.MethodDelete, Path: "/webhooks/" + xidSeg},
			Before:  before,
		}, func() error {
			if err := renderWebhookDetail(ctx, f, before); err != nil {
				return err
			}
			f.ErrPrintf("Would delete webhook %s.\n", xid)
			return nil
		})
	}
	if _, err := client.Delete(ctx, "/webhooks/"+xidSeg, api.WriteOptions{}); err != nil {
		return err
	}
	res := struct {
		ID      string `json:"id" yaml:"id"`
		Deleted bool   `json:"deleted" yaml:"deleted"`
	}{xid, true}
	return f.RenderOutput(res, func() error {
		f.ErrPrintf("%s Deleted webhook %s.\n", f.SuccessIcon(), xid)
		return nil
	})
}
