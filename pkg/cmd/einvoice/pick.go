package einvoice

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	iei "github.com/sepayvn/sepay-cli/internal/einvoice"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
)

const pickerMaxPages = 20

// printPaging writes a pagination summary to stderr in table mode.
func printPaging(f *cmdutil.Factory, p *iei.Paging, shown int) {
	if p == nil || p.Total == 0 {
		return
	}
	fmt.Fprintf(f.IOStreams.ErrOut, "page %d/%d · %d of %d\n",
		p.CurrentPage, p.PageCount, shown, p.Total)
}

func fetchAllProviderAccounts(ctx context.Context, client *iei.Client) ([]providerAccount, error) {
	var all []providerAccount
	for page := 1; page <= pickerMaxPages; page++ {
		resp, err := client.Get(ctx, "/v1/provider-accounts", url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}})
		if err != nil {
			return nil, err
		}
		items, paging, err := iei.ListInto[providerAccount](resp)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if paging == nil || !paging.HasMore {
			break
		}
	}
	return all, nil
}

// pickProviderAccount returns val when set, else prompts on a TTY and returns the id.
func pickProviderAccount(ctx context.Context, f *cmdutil.Factory, val string) (string, error) {
	if val != "" {
		return val, nil
	}
	if !f.CanPrompt() {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("a provider account id is required")}
	}
	client, err := f.NewEInvoiceClient()
	if err != nil {
		return "", err
	}
	accounts, err := fetchAllProviderAccounts(ctx, client)
	if err != nil {
		return "", err
	}
	if len(accounts) == 0 {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("no provider accounts found")}
	}
	choices := make([]cmdutil.PromptChoice, len(accounts))
	for i, a := range accounts {
		choices[i] = cmdutil.PromptChoice{Label: fmt.Sprintf("%s (%s)", a.ID, a.Provider), Value: a.ID}
	}
	return f.SelectChoiceOrFlag("", "", "Select a provider account", choices)
}
