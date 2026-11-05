// Package va implements "sepay va".
package va

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmd/bankaccount"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

const maxVAPickerPages = 50

// FetchAll returns every VA under a bank account, following pagination.
func FetchAll(ctx context.Context, client *api.Client, baXID string) ([]VA, error) {
	baXIDSeg, err := cmdutil.PathSegment("--bank-account", baXID)
	if err != nil {
		return nil, err
	}
	var all []VA
	base := "/bank-accounts/" + baXIDSeg + "/va"
	for page := 1; page <= maxVAPickerPages; page++ {
		env, err := client.Get(ctx, base, url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}})
		if err != nil {
			return nil, err
		}
		vas, pg, err := api.ListInto[VA](env)
		if err != nil {
			return nil, err
		}
		all = append(all, vas...)
		if pg == nil || !pg.HasMore {
			break
		}
	}
	return all, nil
}

// Label renders a VA for a picker, e.g. "SBSEPAY… · NGUYEN VAN A (shop)".
func Label(v VA) string {
	s := v.VA
	if v.SubHolderName != "" {
		s += " · " + v.SubHolderName
	}
	if v.Label != "" {
		s += " (" + v.Label + ")"
	}
	return s
}

// PickOne returns val when set, else prompts for a VA and returns its xid.
func PickOne(ctx context.Context, f *cmdutil.Factory, baXID, val, flag, label string) (string, error) {
	return pickVA(ctx, f, baXID, val, flag, label, func(v VA) string { return v.ID })
}

// PickNumber is PickOne returning the VA number, which sandbox simulate matches on.
func PickNumber(ctx context.Context, f *cmdutil.Factory, baXID, val, flag, label string) (string, error) {
	return pickVA(ctx, f, baXID, val, flag, label, func(v VA) string { return v.VA })
}

func pickVA(ctx context.Context, f *cmdutil.Factory, baXID, val, flag, label string, value func(VA) string) (string, error) {
	if val != "" {
		return val, nil
	}
	if !f.CanPrompt() {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("--%s is required", flag)}
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return "", err
	}
	vas, err := FetchAll(ctx, client, baXID)
	if err != nil {
		return "", err
	}
	if len(vas) == 0 {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("no virtual accounts under that bank account")}
	}
	choices := make([]cmdutil.PromptChoice, 0, len(vas))
	for _, v := range vas {
		choices = append(choices, cmdutil.PromptChoice{Label: Label(v), Value: value(v)})
	}
	return f.SelectChoiceOrFlag("", flag, label, choices)
}

// VA is one virtual account from GET /v2/bank-accounts/{ba_xid}/va.
type VA struct {
	ID            string      `json:"id" yaml:"id"`
	VA            string      `json:"va" yaml:"va"`
	SubHolderName string      `json:"sub_holder_name" yaml:"sub_holder_name"`
	Label         string      `json:"label" yaml:"label"`
	Active        api.FlexInt `json:"active" yaml:"active"`
	Official      api.FlexInt `json:"official" yaml:"official"`
	Static        api.FlexInt `json:"static" yaml:"static"`
	BankAccountID string      `json:"bank_account_id" yaml:"bank_account_id"`
	CreatedAt     string      `json:"created_at" yaml:"created_at"`
	UpdatedAt     string      `json:"updated_at" yaml:"updated_at"`
}

// NewCmdVA builds the "sepay va" command group.
func NewCmdVA(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "va <command>",
		Short: "Virtual accounts (VA) under your bank accounts",
		Long: `A virtual account (VA) is an extra account number under one of your bank
accounts. Give each order or customer its own VA and an incoming transfer
identifies itself by where it arrived, with no pay code to type.

Every VA has a parent bank account, so "va list" and "va issue" take
--bank-account (see: sepay bank-account list). issue and revoke work in sandbox only.`,
	}
	cmd.AddCommand(
		newListCmd(f),
		newGetCmd(f),
		newIssueCmd(f),
		newRevokeCmd(f),
	)
	return cmd
}

func newListCmd(f *cmdutil.Factory) *cobra.Command {
	var bankAccount string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List virtual accounts under a bank account",
		Long: `List the virtual accounts under one bank account. Filter by status, official or
static, or search the VA number or label.

--bank-account is prompted for on a terminal; required otherwise. The ID column is
the <va-xid> "va get" and "va revoke" take.`,
		Example: `  sepay va list --bank-account <bank-account-xid>
  sepay va list --bank-account <bank-account-xid> --official 1 --json`,
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVar(&bankAccount, "bank-account", "", "Bank account xid (VA is nested)")
	collect := cmdutil.BindListFlags(cmd.Flags(), map[string]string{
		"q":               "Search VA number or label",
		"active":          "Filter by status: 0|1|true|false",
		"official":        "Filter by official VA: 0|1",
		"static":          "Filter by static VA: 0|1",
		"created-at-sort": "Sort by created date: asc|desc",
	})
	cmdutil.RenameFlag(cmd.Flags(), "search", "q", "Search VA number or label")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if err := cmdutil.ValidateChoices(cmd, map[string][]string{
			"active":          {"0", "1", "true", "false"},
			"official":        {"0", "1"},
			"static":          {"0", "1"},
			"created-at-sort": {"asc", "desc"},
		}); err != nil {
			return err
		}
		ba, err := bankaccount.PickOne(cmd.Context(), f, bankAccount, "bank-account", "Bank account")
		if err != nil {
			return err
		}
		return runList(cmd.Context(), f, ba, collect())
	}
	return cmd
}

func newGetCmd(f *cmdutil.Factory) *cobra.Command {
	var bankAccount string
	cmd := &cobra.Command{
		Use:   "get <va-xid>",
		Short: "Show a virtual account",
		Long: `Show one virtual account.

<va-xid> is the ID column of "sepay va list --bank-account <bank-account-xid>".
The VA and its --bank-account are prompted for on a terminal; required otherwise.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ba, err := bankaccount.PickOne(cmd.Context(), f, bankAccount, "bank-account", "Bank account")
			if err != nil {
				return err
			}
			vaArg := cmdutil.FirstArg(args)
			if vaArg, err = PickOne(cmd.Context(), f, ba, vaArg, "va", "Virtual account"); err != nil {
				return err
			}
			return runGet(cmd.Context(), f, ba, vaArg)
		},
	}
	cmd.Flags().StringVar(&bankAccount, "bank-account", "", "Bank account xid (VA is nested)")
	return cmd
}

func runList(ctx context.Context, f *cmdutil.Factory, baXID string, query url.Values) error {
	baXIDSeg, err := cmdutil.PathSegment("--bank-account", baXID)
	if err != nil {
		return err
	}

	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	env, err := client.Get(ctx, "/bank-accounts/"+baXIDSeg+"/va", query)
	if err != nil {
		return err
	}
	vas, page, err := api.ListInto[VA](env)
	if err != nil {
		return err
	}

	if err := f.RenderOutput(vas, func() error {
		if len(vas) == 0 {
			fmt.Fprintln(f.IOStreams.ErrOut, "No virtual accounts found.")
			return nil
		}
		t := jsonfmt.NewTable("ID", "VA", "HOLDER", "LABEL", "OFFICIAL", "STATIC", "ACTIVE").Truncatable("HOLDER", "LABEL")
		for _, v := range vas {
			t.AddRow(v.ID, v.VA, v.SubHolderName, v.Label,
				f.YesNo(int(v.Official) == 1), f.YesNo(int(v.Static) == 1), f.YesNo(int(v.Active) == 1))
		}
		if err := f.RenderTable(t); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	f.PrintPageInfo(page, len(vas))
	return nil
}

func runGet(ctx context.Context, f *cmdutil.Factory, baXID, vaXID string) error {
	baXIDSeg, err := cmdutil.PathSegment("--bank-account", baXID)
	if err != nil {
		return err
	}
	vaXIDSeg, err := cmdutil.PathSegment("VA", vaXID)
	if err != nil {
		return err
	}

	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	path := "/bank-accounts/" + baXIDSeg + "/va/" + vaXIDSeg
	env, err := client.Get(ctx, path, nil)
	if err != nil {
		return err
	}
	v, err := api.DataInto[VA](env)
	if err != nil {
		return err
	}
	return f.RenderOutput(v, func() error { return renderVADetail(f, v) })
}

func renderVADetail(f *cmdutil.Factory, v VA) error {
	t := jsonfmt.NewTable("FIELD", "VALUE").Truncatable("VALUE")
	t.AddRow("ID", v.ID)
	t.AddRow("VA", v.VA)
	t.AddRow("Holder", v.SubHolderName)
	t.AddRow("Label", v.Label)
	t.AddRow("Official", f.YesNo(int(v.Official) == 1))
	t.AddRow("Static", f.YesNo(int(v.Static) == 1))
	t.AddRow("Active", f.YesNo(int(v.Active) == 1))
	t.AddRow("Bank account", v.BankAccountID)
	t.AddRow("Created", v.CreatedAt)
	t.AddRow("Updated", v.UpdatedAt)
	return f.RenderTable(t)
}

// issueBody is the VA issue request; kind and number are server-decided.
type issueBody struct {
	Label        string `json:"label,omitempty"`
	TID          string `json:"tid,omitempty"`
	TerminalName string `json:"terminal_name,omitempty"`
}

// issueResult is the issue response.
type issueResult struct {
	OtpRequired bool `json:"otp_required" yaml:"otp_required"`
	VA          `yaml:",inline"`
}

func newIssueCmd(f *cmdutil.Factory) *cobra.Command {
	var bankAccount, label, tid, terminalName string
	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Issue a virtual account on a bank account (sandbox only)",
		Long: `Issue a virtual account under one of your bank accounts (sandbox only). Whether it
is official (a number issued by the bank) follows the bank account's setup, not a
flag. --tid and --terminal-name apply to Vietcombank OneQR accounts only.

--bank-account is prompted for on a terminal; required otherwise.`,
		Example: `  sepay va issue --bank-account <bank-account-xid> --label "Order 1001"`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Production has no VA-issue endpoint, and --yes skips the confirm.
			if err := f.AssertSandboxOnly("va issue"); err != nil {
				return err
			}
			ba, err := bankaccount.PickOne(cmd.Context(), f, bankAccount, "bank-account", "Bank account")
			if err != nil {
				return err
			}
			return runIssue(cmd.Context(), f, ba, issueBody{Label: label, TID: tid, TerminalName: terminalName})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&bankAccount, "bank-account", "", "Bank account xid to issue under")
	fl.StringVar(&label, "label", "", "Display label")
	fl.StringVar(&tid, "tid", "", "Terminal id (Vietcombank OneQR)")
	fl.StringVar(&terminalName, "terminal-name", "", "Terminal name (Vietcombank OneQR, auto-creates a terminal)")
	cmdutil.MarkRequired(cmd, "bank-account")
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnEnvs: "sandbox",
	})
}

func runIssue(ctx context.Context, f *cmdutil.Factory, baXID string, body issueBody) error {
	baXIDSeg, err := cmdutil.PathSegment("--bank-account", baXID)
	if err != nil {
		return err
	}
	if err := f.ConfirmMutation("issue virtual account", false); err != nil {
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
	env, err := client.Post(ctx, "/bank-accounts/"+baXIDSeg+"/va", api.WriteOptions{Body: body, IdempotencyKey: key})
	if err != nil {
		return err
	}
	res, err := api.DataInto[issueResult](env)
	if err != nil {
		return err
	}
	return f.RenderOutput(res, func() error {
		f.ErrPrintf("Issued virtual account %s.\n", res.VA.VA)
		return renderVADetail(f, res.VA)
	})
}

func newRevokeCmd(f *cmdutil.Factory) *cobra.Command {
	var bankAccount string
	cmd := &cobra.Command{
		Use:   "revoke <va-xid|va-number>",
		Short: "Revoke a virtual account (sandbox only)",
		Long: `Revoke a virtual account, named by its xid or by its account number.

Both are in "sepay va list --bank-account <bank-account-xid>". The VA and its
--bank-account are prompted for on a terminal; required otherwise.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := f.AssertSandboxOnly("va revoke"); err != nil {
				return err
			}
			ba, err := bankaccount.PickOne(cmd.Context(), f, bankAccount, "bank-account", "Bank account")
			if err != nil {
				return err
			}
			vaArg := cmdutil.FirstArg(args)
			if vaArg, err = PickOne(cmd.Context(), f, ba, vaArg, "va", "Virtual account"); err != nil {
				return err
			}
			return runRevoke(cmd.Context(), f, ba, vaArg)
		},
	}
	cmd.Flags().StringVar(&bankAccount, "bank-account", "", "Bank account xid (required; VA is nested)")
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnEnvs:        "sandbox",
		cmdutil.AnnDestructive: "irreversible",
		cmdutil.AnnConfirms:    "always",
	})
}

func runRevoke(ctx context.Context, f *cmdutil.Factory, baXID, va string) error {
	baXIDSeg, err := cmdutil.PathSegment("--bank-account", baXID)
	if err != nil {
		return err
	}
	vaSeg, err := cmdutil.PathSegment("VA", va)
	if err != nil {
		return err
	}
	if err := f.ConfirmMutation(fmt.Sprintf("revoke virtual account %s", va), true); err != nil {
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
	path := "/bank-accounts/" + baXIDSeg + "/va/" + vaSeg
	if _, err := client.Delete(ctx, path, api.WriteOptions{IdempotencyKey: key}); err != nil {
		return err
	}
	res := struct {
		VA      string `json:"va" yaml:"va"`
		Revoked bool   `json:"revoked" yaml:"revoked"`
	}{va, true}
	return f.RenderOutput(res, func() error {
		f.ErrPrintf("Revoked virtual account %s.\n", va)
		return nil
	})
}
