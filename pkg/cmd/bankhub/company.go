package bankhub

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	ibh "github.com/sepayvn/sepay-cli/internal/bankhub"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// company is a Bank Hub company; list rows carry active, detail adds configuration.
type company struct {
	XID           string         `json:"xid" yaml:"xid"`
	FullName      string         `json:"full_name" yaml:"full_name"`
	Status        string         `json:"status" yaml:"status"`
	Active        *api.FlexInt   `json:"active,omitempty" yaml:"active,omitempty"`
	Configuration map[string]any `json:"configuration,omitempty" yaml:"configuration,omitempty"`
	CreatedAt     string         `json:"created_at" yaml:"created_at"`
	UpdatedAt     string         `json:"updated_at" yaml:"updated_at"`
}

// companyCreated is the create response, which echoes only these two fields.
type companyCreated struct {
	XID      string `json:"xid" yaml:"xid"`
	FullName string `json:"full_name" yaml:"full_name"`
}

func newCompanyCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{Use: "company <command>", Aliases: []string{"co"}, Short: "Bank Hub companies, one per merchant"}
	cmd.AddCommand(
		newCompanyListCmd(f),
		newCompanyGetCmd(f),
		newCompanyCreateCmd(f),
		newCompanyUpdateCmd(f),
	)
	return cmd
}

// companyStatuses is the full status set; the list filter matches it case-sensitively.
var companyStatuses = []string{"Pending", "Active", "Suspended", "Terminated", "Cancelled", "Fraud"}

var onOffChoices = []string{"on", "off"}

// configText renders a decoded configuration value; a JSON number decodes as float64, which %v
// would print as 5e+06.
func configText(v any) string {
	if n, ok := v.(float64); ok {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// checkTransactionAmount accepts what --transaction-amount documents: a whole amount or Unlimited.
func checkTransactionAmount(v string) error {
	if v == "Unlimited" {
		return nil
	}
	if n, err := strconv.ParseInt(v, 10, 64); err != nil || n < 0 {
		return &cmdutil.FlagError{Err: fmt.Errorf("--transaction-amount must be a whole amount in VND or Unlimited, got %q", v)}
	}
	return nil
}

// companyStatusesCreate are the initial states create accepts.
var companyStatusesCreate = []string{"Pending", "Active"}

func newCompanyCreateCmd(f *cmdutil.Factory) *cobra.Command {
	var name, status string
	cmd := &cobra.Command{
		Use:     "create",
		Aliases: []string{"new"},
		Short:   "Create a company",
		Example: `  sepay bankhub company create --name "Shop ABC" --status Active`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmdutil.ValidateChoice(cmd, "status", companyStatusesCreate); err != nil {
				return err
			}
			fullName, err := f.InputOrFlag(name, "name", "Company full name")
			if err != nil {
				return err
			}
			if status == "" && f.CanPrompt() {
				if status, err = f.SelectOrFlag("", "status", "Status", companyStatusesCreate); err != nil {
					return err
				}
			}
			return runCompanyCreate(cmd.Context(), f, fullName, status)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "Company full name")
	fl.StringVar(&status, "status", "", "Initial status: "+strings.Join(companyStatusesCreate, "|")+" (default Pending)")
	return cmdutil.MarkRequired(cmd, "name")
}

func runCompanyCreate(ctx context.Context, f *cmdutil.Factory, name, status string) error {
	if err := f.ConfirmMutation("create company "+name, "", false); err != nil {
		return err
	}
	client, err := f.NewBankHubClient()
	if err != nil {
		return err
	}
	body := map[string]string{"full_name": name}
	if status != "" {
		body["status"] = status
	}
	resp, err := client.Post(ctx, "/v1/company/create", body)
	if err != nil {
		return err
	}
	c, err := ibh.DataInto[companyCreated](resp)
	if err != nil {
		return err
	}
	return f.RenderOutput(c, func() error {
		f.Printf("%s created company %s (%s)\n", f.SuccessIcon(), c.FullName, c.XID)
		return nil
	})
}

func newCompanyUpdateCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "update <company-xid>",
		Aliases: []string{"edit"},
		Short:   "Update a company's name, status or pay code setting",
		Long: `Change a company's name, status, pay code matching or transaction cap; only the
flags you pass change. With no flag on a terminal it asks which fields to change,
offering the stored values.

<company-xid> is the XID column of "sepay bankhub company list". Prompted for on a terminal; required otherwise.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cmdutil.ValidateChoices(cmd, map[string][]string{
				"status":       companyStatuses,
				"payment-code": onOffChoices,
			}); err != nil {
				return err
			}
			if v, _ := cmd.Flags().GetString("transaction-amount"); cmd.Flags().Changed("transaction-amount") {
				if err := checkTransactionAmount(v); err != nil {
					return err
				}
			}
			body := changedStringFlags(cmd, map[string]string{
				"name":               "full_name",
				"status":             "status",
				"payment-code":       "payment_code",
				"transaction-amount": "transaction_amount",
			})
			if len(body) == 0 && !f.CanPrompt() {
				return &cmdutil.FlagError{Err: fmt.Errorf("nothing to update: pass at least one of --name/--status/--payment-code/--transaction-amount")}
			}
			xid, err := pickCompany(cmd.Context(), f, cmdutil.FirstArg(args), "")
			if err != nil {
				return err
			}
			if len(body) == 0 {
				if body, err = askCompanyFields(cmd.Context(), f, xid); err != nil {
					return err
				}
			}
			return runCompanyUpdate(cmd.Context(), f, xid, body)
		},
	}
	fl := cmd.Flags()
	fl.String("name", "", "New full name")
	fl.String("status", "", "New status: "+strings.Join(companyStatuses, "|"))
	fl.String("payment-code", "", "Pay code matching: on|off")
	fl.String("transaction-amount", "", "Max transaction amount: a whole amount in VND, or Unlimited")
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnConfirms: "always",
	})
}

// askCompanyFields reads the company, asks which fields to change and then for each new value,
// offering the stored one, and returns the edit body.
func askCompanyFields(ctx context.Context, f *cmdutil.Factory, xid string) (map[string]string, error) {
	c, err := fetchCompany(ctx, f, xid)
	if err != nil {
		return nil, err
	}
	stored := func(key string) string {
		if v, ok := c.Configuration[key]; ok && v != nil {
			return configText(v)
		}
		return ""
	}
	fields := []struct {
		label, field, current string
		ask                   func() (string, error)
	}{
		{"Name", "full_name", c.FullName, func() (string, error) { return f.InputWithDefault("Company full name", c.FullName) }},
		{"Status", "status", c.Status, func() (string, error) { return f.SelectDefault("Status", companyStatuses, c.Status) }},
		{"Pay code matching", "payment_code", stored("payment_code"), func() (string, error) {
			return f.SelectDefault("Pay code matching", onOffChoices, stored("payment_code"))
		}},
		{"Transaction amount", "transaction_amount", stored("transaction_amount"), func() (string, error) {
			v, err := f.InputWithDefault("Max transaction amount (whole VND or Unlimited)", stored("transaction_amount"))
			if err != nil {
				return "", err
			}
			v = strings.TrimSpace(v)
			return v, checkTransactionAmount(v)
		}},
	}
	choices := make([]cmdutil.PromptChoice, len(fields))
	for i, fd := range fields {
		label := fd.label
		if fd.current != "" {
			label += " (" + fd.current + ")"
		}
		choices[i] = cmdutil.PromptChoice{Label: label, Value: strconv.Itoa(i)}
	}
	picked, err := f.MultiSelectChoicesOrFlag(nil, "", "What do you want to edit?", choices)
	if err != nil {
		return nil, err
	}
	body := map[string]string{}
	for _, p := range picked {
		i, _ := strconv.Atoi(p)
		v, err := fields[i].ask()
		if err != nil {
			return nil, err
		}
		body[fields[i].field] = v
	}
	if len(body) == 0 {
		return nil, &cmdutil.FlagError{Err: fmt.Errorf("provide at least one field to update")}
	}
	return body, nil
}

func runCompanyUpdate(ctx context.Context, f *cmdutil.Factory, xid string, body map[string]string) error {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return err
	}
	if err := f.ConfirmMutation("update company "+xid, xid, true); err != nil {
		return err
	}
	client, err := f.NewBankHubClient()
	if err != nil {
		return err
	}
	if _, err := client.Post(ctx, "/v1/company/edit/"+xidSeg, body); err != nil {
		return err
	}
	msg := "Company updated."
	return f.RenderOutput(map[string]string{"xid": xid, "message": msg}, func() error {
		fmt.Fprintln(f.IOStreams.Out, f.SuccessIcon()+" "+msg)
		return nil
	})
}

// changedStringFlags maps explicitly set flags to API field names.
func changedStringFlags(cmd *cobra.Command, flagToField map[string]string) map[string]string {
	body := map[string]string{}
	for flag, field := range flagToField {
		if cmd.Flags().Changed(flag) {
			v, _ := cmd.Flags().GetString(flag)
			body[field] = v
		}
	}
	return body
}

func newCompanyListCmd(f *cmdutil.Factory) *cobra.Command {
	var status, q string
	var page, perPage int
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List companies",
		Long: `List Bank Hub companies, one per merchant you onboard. --status filters by state;
a company must be Active before it can get a link token.

The XID column is the <company-xid> other company commands take.`,
		Example: `  sepay bankhub company list --status Active
  sepay bankhub company list --search "Shop ABC" --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmdutil.ValidateChoice(cmd, "status", companyStatuses); err != nil {
				return err
			}
			query := url.Values{}
			if status != "" {
				query.Set("status", status)
			}
			if q != "" {
				query.Set("q", q)
			}
			if page > 0 {
				query.Set("page", strconv.Itoa(page))
			}
			if perPage > 0 {
				query.Set("per_page", strconv.Itoa(perPage))
			}
			return runCompanyList(cmd.Context(), f, query)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&status, "status", "", "Filter by status: "+strings.Join(companyStatuses, "|"))
	fl.StringVar(&q, "search", "", "Search companies")
	cmdutil.AliasFlag(fl, "q", "search")
	fl.IntVar(&page, "page", 0, "Page number")
	fl.IntVar(&perPage, "per-page", 0, "Rows per page (max 100)")
	return cmd
}

func runCompanyList(ctx context.Context, f *cmdutil.Factory, query url.Values) error {
	client, err := f.NewBankHubClient()
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, "/v1/company", query)
	if err != nil {
		return err
	}
	companies, meta, err := ibh.ListInto[company](resp)
	if err != nil {
		return err
	}

	return f.RenderOutput(companies, func() error {
		if len(companies) == 0 {
			fmt.Fprintln(f.IOStreams.ErrOut,
				"No companies found.\nCreate one: sepay bankhub company create --name \"<name>\"")
			return nil
		}
		t := jsonfmt.NewTable("XID", "NAME", "STATUS", "ACTIVE", "CREATED").Truncatable("NAME")
		for _, c := range companies {
			t.AddRow(c.XID, c.FullName, c.Status, f.YesNo(c.Active != nil && int(*c.Active) == 1), c.CreatedAt)
		}
		if err := f.RenderTable(t); err != nil {
			return err
		}
		if meta != nil && meta.Total > 0 {
			fmt.Fprintf(f.IOStreams.ErrOut, "page %d/%d · %d of %d\n",
				meta.CurrentPage, meta.PageCount, len(companies), meta.Total)
		}
		return nil
	})
}

func newCompanyGetCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "get <company-xid>",
		Short: "Show a company and its configuration",
		Long: `Show a company and its configuration.

<company-xid> is the XID column of "sepay bankhub company list". Prompted for on a terminal; required otherwise.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var arg string
			if len(args) > 0 {
				arg = args[0]
			}
			xid, err := pickCompany(cmd.Context(), f, arg, "")
			if err != nil {
				return err
			}
			return runCompanyGet(cmd.Context(), f, xid)
		},
	}
}

func fetchCompany(ctx context.Context, f *cmdutil.Factory, xid string) (company, error) {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return company{}, err
	}
	client, err := f.NewBankHubClient()
	if err != nil {
		return company{}, err
	}
	resp, err := client.Get(ctx, "/v1/company/"+xidSeg, nil)
	if err != nil {
		return company{}, err
	}
	return ibh.DataInto[company](resp)
}

func runCompanyGet(ctx context.Context, f *cmdutil.Factory, xid string) error {
	c, err := fetchCompany(ctx, f, xid)
	if err != nil {
		return err
	}

	return f.RenderOutput(c, func() error {
		f.Printf("XID:     %s\nName:    %s\nStatus:  %s\nCreated: %s\n",
			c.XID, c.FullName, c.Status, c.CreatedAt)
		if len(c.Configuration) == 0 {
			return nil
		}
		fmt.Fprintln(f.IOStreams.Out)
		t := jsonfmt.NewTable("SETTING", "VALUE").Truncatable("VALUE")
		for _, k := range sortedKeys(c.Configuration) {
			t.AddRow(k, configText(c.Configuration[k]))
		}
		return f.RenderTable(t)
	})
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
