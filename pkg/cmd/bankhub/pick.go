package bankhub

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	ibh "github.com/sepayvn/sepay-cli/internal/bankhub"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
)

const pickerMaxPages = 20

func fetchAllCompanies(ctx context.Context, client *ibh.Client) ([]company, error) {
	var all []company
	for page := 1; page <= pickerMaxPages; page++ {
		resp, err := client.Get(ctx, "/v1/company", url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}})
		if err != nil {
			return nil, err
		}
		items, meta, err := ibh.ListInto[company](resp)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if meta == nil || !meta.HasMore {
			break
		}
	}
	return all, nil
}

// fetchAllBankAccounts lists the bank accounts, only company's when it is set.
func fetchAllBankAccounts(ctx context.Context, client *ibh.Client, company string) ([]bankAccount, error) {
	var all []bankAccount
	for page := 1; page <= pickerMaxPages; page++ {
		query := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		if company != "" {
			query.Set("company_xid", company)
		}
		resp, err := client.Get(ctx, "/v1/bank-account", query)
		if err != nil {
			return nil, err
		}
		items, meta, err := ibh.ListInto[bankAccount](resp)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if meta == nil || !meta.HasMore {
			break
		}
	}
	return all, nil
}

// pickCompany returns val when set, else prompts on a TTY; flag names the missing flag in the usage error.
func pickCompany(ctx context.Context, f *cmdutil.Factory, val, flag string) (string, error) {
	if val != "" {
		return val, nil
	}
	if !f.CanPrompt() {
		if flag == "" {
			return "", &cmdutil.FlagError{Err: fmt.Errorf("a company xid is required")}
		}
		return "", &cmdutil.FlagError{Err: fmt.Errorf("--%s is required", flag)}
	}
	client, err := f.NewBankHubClient()
	if err != nil {
		return "", err
	}
	companies, err := fetchAllCompanies(ctx, client)
	if err != nil {
		return "", err
	}
	if len(companies) == 0 {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("no companies found")}
	}
	choices := make([]cmdutil.PromptChoice, len(companies))
	for i, c := range companies {
		choices[i] = cmdutil.PromptChoice{Label: fmt.Sprintf("%s (%s)", c.FullName, c.Status), Value: c.XID}
	}
	return f.SelectChoiceOrFlag("", flag, "Select a company", choices)
}

// pickBankAccount is pickCompany for bank accounts; a company narrows the list to its accounts.
func pickBankAccount(ctx context.Context, f *cmdutil.Factory, val, flag, label, company string) (string, error) {
	if val != "" {
		return val, nil
	}
	if !f.CanPrompt() {
		if flag == "" {
			return "", &cmdutil.FlagError{Err: fmt.Errorf("a bank account xid is required")}
		}
		return "", &cmdutil.FlagError{Err: fmt.Errorf("--%s is required", flag)}
	}
	client, err := f.NewBankHubClient()
	if err != nil {
		return "", err
	}
	accts, err := fetchAllBankAccounts(ctx, client, company)
	if err != nil {
		return "", err
	}
	if len(accts) == 0 {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("no bank accounts found")}
	}
	choices := make([]cmdutil.PromptChoice, len(accts))
	for i, a := range accts {
		choices[i] = cmdutil.PromptChoice{Label: fmt.Sprintf("%s · %s · %s", a.BrandName, a.AccountNumber, a.AccountHolderName), Value: a.XID}
	}
	return f.SelectChoiceOrFlag("", flag, label, choices)
}
