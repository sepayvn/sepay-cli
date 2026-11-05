// Package bankaccount implements "sepay bank-account".
package bankaccount

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// maxPickerPages bounds FetchAll against a broken has_more.
const maxPickerPages = 50

// FetchAll returns every bank account, following pagination.
func FetchAll(ctx context.Context, client *api.Client) ([]BankAccount, error) {
	var all []BankAccount
	for page := 1; page <= maxPickerPages; page++ {
		env, err := client.Get(ctx, "/bank-accounts", url.Values{
			"per_page": {"100"},
			"page":     {strconv.Itoa(page)},
		})
		if err != nil {
			return nil, err
		}
		accts, pg, err := api.ListInto[BankAccount](env)
		if err != nil {
			return nil, err
		}
		all = append(all, accts...)
		if pg == nil || !pg.HasMore {
			break
		}
	}
	return all, nil
}

// PickOne returns val when set, else prompts for a bank account on a TTY and returns its xid.
func PickOne(ctx context.Context, f *cmdutil.Factory, val, flag, label string) (string, error) {
	if val != "" {
		return val, nil
	}
	if !f.CanPrompt() {
		if flag == "" {
			return "", &cmdutil.FlagError{Err: fmt.Errorf("a bank account xid is required")}
		}
		return "", &cmdutil.FlagError{Err: fmt.Errorf("--%s is required", flag)}
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return "", err
	}
	accts, err := FetchAll(ctx, client)
	if err != nil {
		return "", err
	}
	if len(accts) == 0 {
		return "", errNoBankAccounts()
	}
	return f.SelectChoiceOrFlag("", flag, label, toChoices(accts))
}

// PickMany prompts for several bank accounts and returns their xids.
func PickMany(ctx context.Context, f *cmdutil.Factory, flag, label string) ([]string, error) {
	if !f.CanPrompt() {
		return nil, &cmdutil.FlagError{Err: fmt.Errorf("--%s is required", flag)}
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return nil, err
	}
	accts, err := FetchAll(ctx, client)
	if err != nil {
		return nil, err
	}
	if len(accts) == 0 {
		return nil, errNoBankAccounts()
	}
	return f.MultiSelectChoicesOrFlag(nil, flag, label, toChoices(accts))
}

func errNoBankAccounts() error {
	return &cmdutil.FlagError{Err: fmt.Errorf("no bank accounts found; create one first (sepay bank-account create)")}
}

// Fetch returns one bank account by xid.
func Fetch(ctx context.Context, client *api.Client, xid string) (BankAccount, error) {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return BankAccount{}, err
	}
	env, err := client.Get(ctx, "/bank-accounts/"+xidSeg, nil)
	if err != nil {
		return BankAccount{}, err
	}
	return api.DataInto[BankAccount](env)
}

func toChoices(accts []BankAccount) []cmdutil.PromptChoice {
	choices := make([]cmdutil.PromptChoice, 0, len(accts))
	for _, a := range accts {
		choices = append(choices, cmdutil.PromptChoice{Label: Label(a), Value: a.ID})
	}
	return choices
}

// Label renders a bank account as "<bank> · <number> · <holder> (<label>)".
func Label(a BankAccount) string {
	s := a.BankShortName + " · " + a.AccountNumber
	if a.AccountHolderName != "" {
		s += " · " + a.AccountHolderName
	}
	if a.Label != "" {
		s += " (" + a.Label + ")"
	}
	return s
}

// Bank is a supported bank from GET /v2/banks; Code is what create expects.
type Bank struct {
	Code      string `json:"code"`
	ShortName string `json:"short_name"`
	BrandName string `json:"brand_name"`
	FullName  string `json:"full_name"`
	Bin       string `json:"bin"`
}

// FetchBanks lists the supported banks.
func FetchBanks(ctx context.Context, client *api.Client) ([]Bank, error) {
	env, err := client.Get(ctx, "/banks", nil)
	if err != nil {
		return nil, err
	}
	banks, _, err := api.ListInto[Bank](env)
	return banks, err
}

// CheckBankCode returns the code of the supported bank val names by code, short name or brand
// name, in any letter case, before an account is created with it.
func CheckBankCode(ctx context.Context, f *cmdutil.Factory, val string) (string, error) {
	client, err := f.NewAPIClient()
	if err != nil {
		return "", err
	}
	banks, err := FetchBanks(ctx, client)
	if err != nil {
		return "", err
	}
	codes := make([]string, len(banks))
	for i, b := range banks {
		if b.names(val) {
			return b.Code, nil
		}
		codes[i] = b.Code
	}
	return "", &cmdutil.FlagError{Err: fmt.Errorf("--bank %q is not a supported bank code (have: %s)", val, strings.Join(codes, ", "))}
}

// preferredBank is the default starter bank wherever the server lists it.
const preferredBank = "ACB"

// DefaultBankCode returns ACB when the server supports it, else the first bank it lists.
func DefaultBankCode(ctx context.Context, f *cmdutil.Factory) (string, error) {
	client, err := f.NewAPIClient()
	if err != nil {
		return "", err
	}
	banks, err := FetchBanks(ctx, client)
	if err != nil {
		return "", err
	}
	if len(banks) == 0 {
		return "", errors.New("the server lists no supported bank; pass --bank")
	}
	for _, b := range banks {
		if b.names(preferredBank) {
			return b.Code, nil
		}
	}
	return banks[0].Code, nil
}

// names reports whether val is this bank's code, short name or brand name. The server accepts
// any of them for a new account: a deployment may list ACB under the code ACBE2E.
func (b Bank) names(val string) bool {
	return strings.EqualFold(b.Code, val) || strings.EqualFold(b.ShortName, val) || strings.EqualFold(b.BrandName, val)
}

// PickBankCode returns val when set, else prompts for a bank on a TTY and returns its code.
func PickBankCode(ctx context.Context, f *cmdutil.Factory, val string) (string, error) {
	if val != "" {
		return val, nil
	}
	if !f.CanPrompt() {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("--bank is required")}
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return "", err
	}
	banks, err := FetchBanks(ctx, client)
	if err != nil {
		return "", err
	}
	if len(banks) == 0 {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("no banks available")}
	}
	choices := make([]cmdutil.PromptChoice, 0, len(banks))
	for _, b := range banks {
		label := b.ShortName
		if b.FullName != "" {
			label += " · " + b.FullName
		}
		choices = append(choices, cmdutil.PromptChoice{Label: label, Value: b.Code})
	}
	return f.SelectChoiceOrFlag("", "bank", "Bank", choices)
}

// BankAccount is one bank account from GET /v2/bank-accounts.
type BankAccount struct {
	ID                string      `json:"id" yaml:"id"`
	AccountHolderName string      `json:"account_holder_name" yaml:"account_holder_name"`
	AccountNumber     string      `json:"account_number" yaml:"account_number"`
	Accumulated       int64       `json:"accumulated" yaml:"accumulated"`
	LastTransaction   *string     `json:"last_transaction" yaml:"last_transaction"`
	Label             string      `json:"label" yaml:"label"`
	AccountType       string      `json:"account_type" yaml:"account_type"`
	Active            api.FlexInt `json:"active" yaml:"active"`
	CreatedAt         string      `json:"created_at" yaml:"created_at"`
	BankShortName     string      `json:"bank_short_name" yaml:"bank_short_name"`
	BankFullName      string      `json:"bank_full_name" yaml:"bank_full_name"`
	BankBin           string      `json:"bank_bin" yaml:"bank_bin"`
	BankCode          string      `json:"bank_code" yaml:"bank_code"`
	// ConnectionType is "api", "sms", "api+sms", or null when the account is not connected.
	ConnectionType *string `json:"connection_type" yaml:"connection_type"`
	// OfficialVASupported: the bank's API issues official VAs for this account type. It says
	// nothing about SMS or SePay-side (non-official) VAs, which any connection can carry.
	OfficialVASupported bool `json:"official_va_supported" yaml:"official_va_supported"`
	// VAOnly: the account notifies virtual-account transactions only, never the account number.
	VAOnly bool `json:"va_only" yaml:"va_only"`
}

// OverSMS reports whether the account is connected over SMS, alone or alongside the API.
func (a BankAccount) OverSMS() bool {
	return a.ConnectionType != nil && strings.Contains(*a.ConnectionType, "sms")
}

// Connection renders ConnectionType, "-" when the account is not connected.
func (a BankAccount) Connection() string {
	if a.ConnectionType == nil || *a.ConnectionType == "" {
		return "-"
	}
	return *a.ConnectionType
}

// Notifies renders what the account can notify: the account number, virtual accounts, or both.
func (a BankAccount) Notifies() string {
	switch {
	case a.VAOnly:
		return "virtual accounts only"
	case a.OfficialVASupported || a.OverSMS():
		return "account number and virtual accounts"
	}
	return "account number, and any virtual account it has"
}

// createdBankAccount is the create response; FirstVAStatus is "not_applicable", "created" or "failed".
type createdBankAccount struct {
	BankAccount
	FirstVA       *string `json:"first_va" yaml:"first_va"`
	FirstVAStatus string  `json:"first_va_status" yaml:"first_va_status"`
}

// NewCmdBankAccount builds the "sepay bank-account" command group.
func NewCmdBankAccount(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "bank-account <command>",
		Aliases: []string{"ba"},
		Short:   "Bank accounts SePay watches for transfers",
		Long: `A bank account is a real account of yours that SePay watches: each transfer the
bank reports on it becomes a transaction (sepay transaction). Resources are
addressed by xid, the public id every list prints.

Start with "sepay bank-account list". Virtual accounts (sepay va) and webhooks
(sepay webhook) both target a bank account. create and delete work in sandbox only.`,
	}
	cmd.AddCommand(
		newListCmd(f),
		newGetCmd(f),
		newUpdateCmd(f),
		newCreateCmd(f),
		newDeleteCmd(f),
	)
	return cmd
}

func newListCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List bank accounts",
		Long: `List your bank accounts, a page at a time. Filter by bank, status, balance or the
date of the last transaction; sort by creation date, balance or last transaction.

The ID column is the <bank-account-xid> other commands take.`,
		Example: `  sepay bank-account list
  sepay bank-account list --bank ACB --active 1
  sepay bank-account list --json`,
		Args: cobra.NoArgs,
	}
	collect := cmdutil.BindListFlags(cmd.Flags(), map[string]string{
		"bank-short-name":       "Filter by bank (e.g. ACB, BIDV)",
		"q":                     "Search holder name, label, or account number",
		"active":                "Filter by status: 0|1|true|false",
		"accumulated-min":       "Minimum accumulated balance",
		"accumulated-max":       "Maximum accumulated balance",
		"last-transaction-from": "Last transaction on or after this date (YYYY-MM-DD)",
		"last-transaction-to":   "Last transaction on or before this date (YYYY-MM-DD)",
		"created-at-sort":       "Sort by created date: asc|desc",
		"last-transaction-sort": "Sort by last transaction: asc|desc",
		"accumulated-sort":      "Sort by balance: asc|desc",
	})
	cmdutil.RenameFlag(cmd.Flags(), "bank", "bank-short-name", "Filter by bank (e.g. ACB, BIDV)")
	cmdutil.RenameFlag(cmd.Flags(), "search", "q", "Search holder name, label, or account number")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		sort := []string{"asc", "desc"}
		if err := cmdutil.ValidateChoices(cmd, map[string][]string{
			// The API accepts both spellings for active.
			"active":                {"0", "1", "true", "false"},
			"created-at-sort":       sort,
			"last-transaction-sort": sort,
			"accumulated-sort":      sort,
		}); err != nil {
			return err
		}
		if err := cmdutil.ValidateAmounts(cmd, "accumulated-min", "accumulated-max"); err != nil {
			return err
		}
		if err := cmdutil.ValidateDates(cmd, "last-transaction-from", "last-transaction-to"); err != nil {
			return err
		}
		if err := cmdutil.ValidateDateOrder(cmd, "last-transaction-from", "last-transaction-to"); err != nil {
			return err
		}
		if err := cmdutil.IncludeEndDay(cmd, "last-transaction-to"); err != nil {
			return err
		}
		if err := cmdutil.ValidateAmountOrder(cmd, "accumulated-min", "accumulated-max"); err != nil {
			return err
		}
		return runList(cmd.Context(), f, collect())
	}
	return cmd
}

func newGetCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "get <bank-account-xid>",
		Short: "Show a bank account",
		Long: `Show one bank account with its bank, number, holder, label and status.

<bank-account-xid> is the ID column of "sepay bank-account list". Prompted for on a terminal; required otherwise.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			xid, err := PickOne(cmd.Context(), f, cmdutil.FirstArg(args), "", "Bank account")
			if err != nil {
				return err
			}
			return runGet(cmd.Context(), f, xid)
		},
	}
}

func runList(ctx context.Context, f *cmdutil.Factory, query url.Values) error {
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	env, err := client.Get(ctx, "/bank-accounts", query)
	if err != nil {
		return err
	}
	accounts, page, err := api.ListInto[BankAccount](env)
	if err != nil {
		return err
	}

	if err := f.RenderOutput(accounts, func() error {
		if len(accounts) == 0 {
			fmt.Fprintln(f.IOStreams.ErrOut, "No bank accounts found.")
			return nil
		}
		t := jsonfmt.NewTable("ID", "BANK", "ACCOUNT", "HOLDER", "LABEL", "CONNECTION", "ACCUMULATED", "ACTIVE").Truncatable("BANK", "HOLDER", "LABEL")
		for _, a := range accounts {
			t.AddRow(a.ID, a.BankShortName, a.AccountNumber, a.AccountHolderName,
				a.Label, a.Connection(), jsonfmt.FormatVND(a.Accumulated), f.YesNo(int(a.Active) == 1))
		}
		if err := f.RenderTable(t); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	f.PrintPageInfo(page, len(accounts))
	return nil
}

func runGet(ctx context.Context, f *cmdutil.Factory, xid string) error {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return err
	}

	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	env, err := client.Get(ctx, "/bank-accounts/"+xidSeg, nil)
	if err != nil {
		return err
	}
	a, err := api.DataInto[BankAccount](env)
	if err != nil {
		return err
	}

	return f.RenderOutput(a, func() error { return renderDetail(f, a) })
}

func renderDetail(f *cmdutil.Factory, a BankAccount) error {
	last := "-"
	if a.LastTransaction != nil {
		last = *a.LastTransaction
	}
	t := jsonfmt.NewTable("FIELD", "VALUE").Truncatable("VALUE")
	t.AddRow("ID", a.ID)
	t.AddRow("Bank", a.BankShortName+" ("+a.BankFullName+")")
	t.AddRow("Account", a.AccountNumber)
	t.AddRow("Holder", a.AccountHolderName)
	t.AddRow("Label", a.Label)
	t.AddRow("Type", a.AccountType)
	t.AddRow("Connection", a.Connection())
	t.AddRow("Notifies", a.Notifies())
	t.AddRow("Official VA", f.YesNo(a.OfficialVASupported))
	t.AddRow("Accumulated", jsonfmt.FormatVND(a.Accumulated))
	t.AddRow("Last transaction", last)
	t.AddRow("Active", f.YesNo(int(a.Active) == 1))
	t.AddRow("Created", a.CreatedAt)
	return f.RenderTable(t)
}

// Enums the create endpoint accepts; connection_type is lowercased first.
var (
	accountTypes = []string{"individual", "business_household", "enterprise"}
	connections  = []string{"sms", "api"}
)

type createBody struct {
	Bank        string `json:"bank"`
	AccountType string `json:"account_type"`
	Connection  string `json:"connection_type"`
	HolderName  string `json:"holder_name,omitempty"`
	Label       string `json:"label,omitempty"`
}

func newCreateCmd(f *cmdutil.Factory) *cobra.Command {
	var bank, accountType, connection, holderName, label string
	cmd := &cobra.Command{
		Use:     "create",
		Aliases: []string{"new"},
		Short:   "Create a bank account (sandbox only)",
		Long: `Create a sandbox bank account. The account number is issued by SePay, so there
is no flag to choose one; read it back from "bank-account get" or the create
output. --holder-name is generated too when you leave it out.

The connection is API unless you pass --connection sms. SMS is deprecated: only
plans that still include it may use it, and only those may pick a bank without
an API connection.`,
		Example: `  sepay bank-account create --bank ACB --account-type individual --label "Shop"`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Production has no create endpoint; fail before the picker's API call.
			if err := f.AssertSandboxOnly("bank-account create"); err != nil {
				return err
			}
			// The server lowercases connection_type itself.
			connection = strings.ToLower(connection)
			if connection == "" {
				connection = "api"
			}
			if accountType != "" && !slices.Contains(accountTypes, accountType) {
				return &cmdutil.FlagError{Err: fmt.Errorf("--account-type must be one of: %s", strings.Join(accountTypes, ", "))}
			}
			if !slices.Contains(connections, connection) {
				return &cmdutil.FlagError{Err: fmt.Errorf("--connection must be one of: %s", strings.Join(connections, ", "))}
			}
			bankFromFlag := bank != ""
			var err error
			if bank, err = PickBankCode(cmd.Context(), f, bank); err != nil {
				return err
			}
			if accountType, err = f.SelectOrFlag(accountType, "account-type", "Account type", accountTypes); err != nil {
				return err
			}
			// After the local checks, so a missing flag is still reported without a request.
			if bankFromFlag {
				if bank, err = CheckBankCode(cmd.Context(), f, bank); err != nil {
					return err
				}
			}
			return runCreate(cmd.Context(), f, createBody{
				Bank:        bank,
				AccountType: accountType,
				Connection:  connection,
				HolderName:  holderName,
				Label:       label,
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&bank, "bank", "", "Bank code, e.g. ACB (required)")
	fl.StringVar(&accountType, "account-type", "", strings.Join(accountTypes, "|")+" (required)")
	fl.StringVar(&connection, "connection", "api", strings.Join(connections, "|")+"; sms only on plans that include it")
	fl.StringVar(&holderName, "holder-name", "", "Account holder name (auto-generated if empty)")
	fl.StringVar(&label, "label", "", "Display label")
	cmdutil.MarkRequired(cmd, "bank", "account-type")
	return cmdutil.Annotate(cmd, map[string]string{cmdutil.AnnEnvs: "sandbox"})
}

func runCreate(ctx context.Context, f *cmdutil.Factory, body createBody) error {
	if err := f.ConfirmMutation("create bank account", false); err != nil {
		return err
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	key, err := f.IdempotencyKey()
	if err != nil {
		return err
	}
	env, err := client.Post(ctx, "/bank-accounts", api.WriteOptions{Body: body, IdempotencyKey: key})
	if err != nil {
		return err
	}
	a, err := api.DataInto[createdBankAccount](env)
	if err != nil {
		return err
	}
	err = f.RenderOutput(a, func() error {
		f.ErrPrintf("Created bank account %s.\n", a.ID)
		if err := renderDetail(f, a.BankAccount); err != nil {
			return err
		}
		if a.FirstVA != nil && *a.FirstVA != "" {
			f.Printf("First VA: %s\n", *a.FirstVA)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Warn, not fail: the account was created.
	if a.FirstVAStatus == "failed" {
		f.ErrPrintf(
			"! the account was created but its first VA was not; run `sepay va issue --bank-account %s` to add one, otherwise `transactions simulate va_credit` and the QR builder have no VA to use\n", a.ID)
	}
	return nil
}

func newUpdateCmd(f *cmdutil.Factory) *cobra.Command {
	var label string
	cmd := &cobra.Command{
		Use:     "update <bank-account-xid>",
		Aliases: []string{"edit"},
		Short:   "Update a bank account's label",
		Long: `Change a bank account's label, the name lists show for it.

<bank-account-xid> is the ID column of "sepay bank-account list". Prompted for on a terminal; required otherwise.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			xid, err := PickOne(cmd.Context(), f, cmdutil.FirstArg(args), "", "Bank account")
			if err != nil {
				return err
			}
			if label, err = f.InputOrFlag(label, "label", "New label"); err != nil {
				return err
			}
			return runUpdate(cmd.Context(), f, xid, map[string]any{"label": label})
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "Display label")
	return cmdutil.Annotate(cmd, map[string]string{cmdutil.AnnScope: "bank-account:write"})
}

func runUpdate(ctx context.Context, f *cmdutil.Factory, xid string, body map[string]any) error {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return err
	}

	if err := f.ConfirmMutation("update bank account", false); err != nil {
		return err
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	env, err := client.Put(ctx, "/bank-accounts/"+xidSeg, api.WriteOptions{Body: body})
	if err != nil {
		return err
	}
	res, err := api.DataInto[idOnly](env)
	if err != nil {
		return err
	}
	return f.RenderOutput(res, func() error {
		f.ErrPrintf("Updated bank account %s.\n", res.ID)
		return nil
	})
}

func newDeleteCmd(f *cmdutil.Factory) *cobra.Command {
	return cmdutil.Annotate(&cobra.Command{
		Use:     "delete <bank-account-xid>",
		Aliases: []string{"rm", "del"},
		Short:   "Delete a bank account (sandbox only)",
		Long: `Delete a sandbox bank account.

<bank-account-xid> is the ID column of "sepay bank-account list". Prompted for on a terminal; required otherwise.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Production has no delete endpoint, and --yes skips the confirm.
			if err := f.AssertSandboxOnly("bank-account delete"); err != nil {
				return err
			}
			xid, err := PickOne(cmd.Context(), f, cmdutil.FirstArg(args), "", "Bank account")
			if err != nil {
				return err
			}
			return runDelete(cmd.Context(), f, xid)
		},
	}, map[string]string{
		cmdutil.AnnEnvs:        "sandbox",
		cmdutil.AnnDestructive: "irreversible",
		cmdutil.AnnConfirms:    "always",
	})
}

func runDelete(ctx context.Context, f *cmdutil.Factory, xid string) error {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return err
	}
	if err := f.ConfirmMutation(fmt.Sprintf("delete bank account %s (also removes its VAs and transactions)", xid), true); err != nil {
		return err
	}

	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	if _, err := client.Delete(ctx, "/bank-accounts/"+xidSeg, api.WriteOptions{}); err != nil {
		return err
	}
	res := idOnly{ID: xid}
	return f.RenderOutput(res, func() error {
		f.ErrPrintf("Deleted bank account %s.\n", xid)
		return nil
	})
}

// idOnly is the {id} response of update.
type idOnly struct {
	ID string `json:"id" yaml:"id"`
}
