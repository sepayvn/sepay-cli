package webhook

import (
	"context"
	"fmt"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

func newDeleteCmd(f *cmdutil.Factory) *cobra.Command {
	return cmdutil.Annotate(&cobra.Command{
		Use:     "delete <webhook-xid>",
		Aliases: []string{"rm", "del"},
		Short:   "Delete a webhook",
		Long: `Delete a webhook; SePay stops delivering to it.

<webhook-xid> is the ID column of "sepay webhook list". Prompted for on a terminal; required otherwise.`,
		Example: `  sepay webhook delete <webhook-xid> --yes`,
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			xid, err := pickWebhook(cmd.Context(), f, cmdutil.FirstArg(args))
			if err != nil {
				return err
			}
			return runDelete(cmd.Context(), f, xid)
		},
	}, map[string]string{
		cmdutil.AnnScope:       "webhook:write",
		cmdutil.AnnDestructive: "irreversible",
		cmdutil.AnnConfirms:    "always",
	})
}

func runDelete(ctx context.Context, f *cmdutil.Factory, xid string) error {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return err
	}
	if err := f.ConfirmMutation(fmt.Sprintf("delete webhook %s", xid), true); err != nil {
		return err
	}

	client, err := f.NewAPIClient()
	if err != nil {
		return err
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
