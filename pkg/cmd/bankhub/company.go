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
	cmd := &cobra.Command{Use: "company <command>", Aliases: []string{"co"}, Short: "Manage Bank Hub companies"}
	cmd.AddCommand(
		newCompanyListCmd(f),
		newCompanyGetCmd(f),
		newCompanyCreateCmd(f),
		newCompanyEditCmd(f),
	)
	return cmd
}

// companyStatuses is the full status set; the list filter matches it case-sensitively.
var companyStatuses = []string{"Pending", "Active", "Suspended", "Terminated", "Cancelled", "Fraud"}

// companyStatusesCreate are the initial states create accepts.
var companyStatusesCreate = []string{"Pending", "Active"}

func newCompanyCreateCmd(f *cmdutil.Factory) *cobra.Command {
	var name, status string
	cmd := &cobra.Command{
		Use:     "create",
		Aliases: []string{"new"},
		Short:   "Create a company",
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
	if err := f.ConfirmMutation("create company "+name, false); err != nil {
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

func newCompanyEditCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "edit [xid]",
		Aliases: []string{"update"},
		Short:   "Edit a company (name, status, or PayCode config)",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cmdutil.ValidateChoice(cmd, "status", companyStatuses); err != nil {
				return err
			}
			body := changedStringFlags(cmd, map[string]string{
				"name":               "full_name",
				"status":             "status",
				"payment-code":       "payment_code",
				"transaction-amount": "transaction_amount",
			})
			if len(body) == 0 {
				return &cmdutil.FlagError{Err: fmt.Errorf("nothing to update: pass at least one of --name/--status/--payment-code/--transaction-amount")}
			}
			xid, err := pickCompany(cmd.Context(), f, cmdutil.FirstArg(args), "")
			if err != nil {
				return err
			}
			return runCompanyEdit(cmd.Context(), f, xid, body)
		},
	}
	fl := cmd.Flags()
	fl.String("name", "", "New full name")
	fl.String("status", "", "New status: "+strings.Join(companyStatuses, "|"))
	fl.String("payment-code", "", "PayCode: on|off")
	fl.String("transaction-amount", "", "Max transaction amount (integer) or Unlimited")
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnConfirms: "always",
	})
}

func runCompanyEdit(ctx context.Context, f *cmdutil.Factory, xid string, body map[string]string) error {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return err
	}
	if err := f.ConfirmMutation("edit company "+xid, true); err != nil {
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
		Args:    cobra.NoArgs,
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
	fl.StringVar(&q, "q", "", "Search companies")
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
		Use:   "get [xid]",
		Short: "Show a company and its configuration",
		Args:  cobra.MaximumNArgs(1),
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

func runCompanyGet(ctx context.Context, f *cmdutil.Factory, xid string) error {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return err
	}
	client, err := f.NewBankHubClient()
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, "/v1/company/"+xidSeg, nil)
	if err != nil {
		return err
	}
	c, err := ibh.DataInto[company](resp)
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
			t.AddRow(k, fmt.Sprintf("%v", c.Configuration[k]))
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
