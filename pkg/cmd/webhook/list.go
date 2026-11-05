package webhook

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

func newListCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List webhooks",
		Long: `List your webhooks with destination, events, auth and targeting. --active 1 keeps
the enabled ones.

The ID column is the <webhook-xid> other webhook commands take.`,
		Example: `  sepay webhook list
  sepay webhook list --active 1 --json`,
		Args: cobra.NoArgs,
	}
	collect := cmdutil.BindListFlags(cmd.Flags(), map[string]string{
		"active": "Filter by status: 0|1",
	})
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		// The API ignores an unknown filter value and returns an unfiltered page.
		if err := cmdutil.ValidateChoice(cmd, "active", []string{"0", "1"}); err != nil {
			return err
		}
		return runList(cmd.Context(), f, collect())
	}
	return cmd
}

const maxWebhookPickerPages = 50

func fetchAllWebhooks(ctx context.Context, client *api.Client) ([]Webhook, error) {
	var all []Webhook
	for page := 1; page <= maxWebhookPickerPages; page++ {
		env, err := client.Get(ctx, "/webhooks", url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}})
		if err != nil {
			return nil, err
		}
		hooks, pg, err := api.ListInto[Webhook](env)
		if err != nil {
			return nil, err
		}
		all = append(all, hooks...)
		if pg == nil || !pg.HasMore {
			break
		}
	}
	return all, nil
}

// pickWebhook returns val, or prompts for a webhook on an interactive terminal.
func pickWebhook(ctx context.Context, f *cmdutil.Factory, val string) (string, error) {
	if val != "" {
		return val, nil
	}
	if !f.CanPrompt() {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("a webhook xid is required")}
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return "", err
	}
	hooks, err := fetchAllWebhooks(ctx, client)
	if err != nil {
		return "", err
	}
	if len(hooks) == 0 {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("no webhooks found; create one first (sepay webhook create)")}
	}
	choices := make([]cmdutil.PromptChoice, 0, len(hooks))
	for _, w := range hooks {
		label := w.WebhookURL
		if w.Name != "" {
			label = w.Name + " · " + w.WebhookURL
		}
		choices = append(choices, cmdutil.PromptChoice{Label: label + " [" + w.BankMode + "]", Value: w.ID})
	}
	return f.SelectChoiceOrFlag("", "webhook", "Webhook", choices)
}

func runList(ctx context.Context, f *cmdutil.Factory, query url.Values) error {
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	env, err := client.Get(ctx, "/webhooks", query)
	if err != nil {
		return err
	}
	hooks, page, err := api.ListInto[Webhook](env)
	if err != nil {
		return err
	}
	for i := range hooks {
		hooks[i] = hooks[i].redacted()
	}

	if err := f.RenderOutput(hooks, func() error {
		if len(hooks) == 0 {
			fmt.Fprintln(f.IOStreams.ErrOut, "No webhooks found.")
			return nil
		}
		t := jsonfmt.NewTable("ID", "NAME", "URL", "EVENT", "AUTH", "TARGET", "ACTIVE").Truncatable("NAME", "URL", "TARGET")
		for _, w := range hooks {
			t.AddRow(w.ID, w.Name, w.urlLabel(), w.EventType, w.AuthenType, w.BankMode, f.YesNo(bool(w.Active)))
		}
		if err := f.RenderTable(t); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	f.PrintPageInfo(page, len(hooks))
	return nil
}
