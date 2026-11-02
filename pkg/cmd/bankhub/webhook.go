package bankhub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	ibh "github.com/sepayvn/sepay-cli/internal/bankhub"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// webhook is the merchant's Bank Hub webhook config; GET masks secret_key.
type webhook struct {
	WebhookURL         string      `json:"webhook_url" yaml:"webhook_url"`
	AuthType           string      `json:"auth_type" yaml:"auth_type"`
	SecretKey          string      `json:"secret_key" yaml:"secret_key"`
	RequestContentType string      `json:"request_content_type" yaml:"request_content_type"`
	Active             api.FlexInt `json:"active" yaml:"active"`
	AllowEvents        []string    `json:"allow_events" yaml:"allow_events"`
}

func newWebhookCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{Use: "webhook <command>", Aliases: []string{"wh"}, Short: "Manage the Bank Hub webhook"}
	cmd.AddCommand(newWebhookListCmd(f), newWebhookSetCmd(f))
	return cmd
}

func newWebhookSetCmd(f *cmdutil.Factory) *cobra.Command {
	var events []string
	var secret string
	var active bool
	cmd := &cobra.Command{
		Use:   "set [url]",
		Short: "Create or update the merchant webhook (upsert)",
		Args:  cmdutil.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			url, err := f.InputOrArg(cmdutil.FirstArg(args), "a webhook URL", "Webhook URL")
			if err != nil {
				return err
			}
			// The upsert replaces the only webhook, so reject a bad URL before the confirm.
			if err := cmdutil.ValidateDestinationURL(url, "webhook URL"); err != nil {
				return err
			}
			body := map[string]any{"webhook_url": url}
			if len(events) > 0 {
				// Bank Hub requires allow_events as a JSON-encoded string, not an array.
				encoded, err := json.Marshal(events)
				if err != nil {
					return err
				}
				body["allow_events"] = string(encoded)
			}
			if cmd.Flags().Changed("secret") {
				resolved, serr := f.SecretFromFlag(secret, "secret")
				if serr != nil {
					return serr
				}
				body["secret_key"] = resolved
				body["auth_type"] = "SECRET_KEY"
			}
			if cmd.Flags().Changed("active") {
				body["active"] = boolToFlag(active)
			}
			return runWebhookSet(cmd.Context(), f, body)
		},
	}
	fl := cmd.Flags()
	fl.StringSliceVar(&events, "events", nil, "Events to subscribe: '*' or comma-separated event names")
	fl.StringVar(&secret, "secret", "", "Signing secret key, or - to read it from stdin")
	fl.BoolVar(&active, "active", true, "Whether the webhook is active")
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnConfirms:    "always",
		cmdutil.AnnDestructive: "irreversible",
		cmdutil.AnnGuidance:    "Replaces the merchant's single webhook wholesale; the previous URL and secret cannot be read back.",
	})
}

func runWebhookSet(ctx context.Context, f *cmdutil.Factory, body map[string]any) error {
	if err := f.ConfirmMutation("set the merchant webhook", true); err != nil {
		return err
	}
	client, err := f.NewBankHubClient()
	if err != nil {
		return err
	}
	resp, err := client.Post(ctx, "/v1/webhook", body)
	if err != nil {
		return err
	}
	w, err := ibh.DataInto[webhook](resp)
	if err != nil {
		return err
	}
	// The POST echoes secret_key raw.
	w.SecretKey = cmdutil.MaskSecret(w.SecretKey)

	return f.RenderOutput(w, func() error {
		events := "*"
		if len(w.AllowEvents) > 0 {
			events = strings.Join(w.AllowEvents, ", ")
		}
		f.Printf(
			"%s webhook set\nURL:      %s\nActive:   %s\nEvents:   %s\n",
			f.SuccessIcon(), w.WebhookURL, f.YesNo(int(w.Active) == 1), events)
		return nil
	})
}

func newWebhookListCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "Show the merchant webhook configuration",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWebhookList(cmd.Context(), f)
		},
	}
}

func runWebhookList(ctx context.Context, f *cmdutil.Factory) error {
	client, err := f.NewBankHubClient()
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, "/v1/webhook", nil)
	if err != nil {
		return err
	}

	// data is null when no webhook is configured.
	var w *webhook
	if len(resp.Data) > 0 && string(bytes.TrimSpace(resp.Data)) != "null" {
		var wv webhook
		if err := json.Unmarshal(resp.Data, &wv); err != nil {
			return fmt.Errorf("could not decode webhook: %w", err)
		}
		w = &wv
	}

	return f.RenderOutput(w, func() error {
		if w == nil {
			fmt.Fprintln(f.IOStreams.Out, "No webhook configured.")
			return nil
		}
		events := "*"
		if len(w.AllowEvents) > 0 {
			events = strings.Join(w.AllowEvents, ", ")
		}
		f.Printf(
			"URL:      %s\nAuth:     %s\nSecret:   %s\nContent:  %s\nActive:   %s\nEvents:   %s\n",
			w.WebhookURL, jsonfmt.OrDash(w.AuthType), jsonfmt.OrDash(w.SecretKey),
			jsonfmt.OrDash(w.RequestContentType), f.YesNo(int(w.Active) == 1), events)
		return nil
	})
}
