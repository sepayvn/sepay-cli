package einvoice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	iei "github.com/sepayvn/sepay-cli/internal/einvoice"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// invoice is an E-Invoice document; only the detail carries Items.
type invoice struct {
	ReferenceCode    string        `json:"reference_code" yaml:"reference_code"`
	InvoiceNumber    string        `json:"invoice_number" yaml:"invoice_number"`
	InvoiceSeries    string        `json:"invoice_series,omitempty" yaml:"invoice_series,omitempty"`
	DocumentType     int           `json:"document_type,omitempty" yaml:"document_type,omitempty"`
	IssuedDate       string        `json:"issued_date" yaml:"issued_date"`
	PdfURL           *string       `json:"pdf_url" yaml:"pdf_url"`
	XMLURL           *string       `json:"xml_url" yaml:"xml_url"`
	Status           string        `json:"status" yaml:"status"`
	TaxAuthorityCode *string       `json:"tax_authority_code,omitempty" yaml:"tax_authority_code,omitempty"`
	ProviderStatus   *string       `json:"provider_status,omitempty" yaml:"provider_status,omitempty"`
	PaymentMethod    string        `json:"payment_method,omitempty" yaml:"payment_method,omitempty"`
	Buyer            *buyer        `json:"buyer,omitempty" yaml:"buyer,omitempty"`
	TotalBeforeTax   api.FlexNum   `json:"total_before_tax" yaml:"total_before_tax"`
	TaxAmount        api.FlexNum   `json:"tax_amount" yaml:"tax_amount"`
	TotalAmount      api.FlexNum   `json:"total_amount" yaml:"total_amount"`
	Notes            string        `json:"notes,omitempty" yaml:"notes,omitempty"`
	Source           string        `json:"source,omitempty" yaml:"source,omitempty"`
	OriginalInvoice  *original     `json:"original_invoice,omitempty" yaml:"original_invoice,omitempty"`
	Items            []invoiceItem `json:"items,omitempty" yaml:"items,omitempty"`
}

// original is the invoice a replacement or an adjustment points to; any field may be null.
type original struct {
	ReferenceCode *string `json:"reference_code" yaml:"reference_code"`
	InvoiceNumber *string `json:"invoice_number" yaml:"invoice_number"`
	InvoiceSeries *string `json:"invoice_series" yaml:"invoice_series"`
	IssuedDate    *string `json:"issued_date" yaml:"issued_date"`
}

type buyer struct {
	Type      string `json:"type,omitempty" yaml:"type,omitempty"`
	Name      string `json:"name" yaml:"name"`
	LegalName string `json:"legal_name,omitempty" yaml:"legal_name,omitempty"`
	TaxCode   string `json:"tax_code,omitempty" yaml:"tax_code,omitempty"`
	Address   string `json:"address,omitempty" yaml:"address,omitempty"`
	Email     string `json:"email,omitempty" yaml:"email,omitempty"`
	Phone     string `json:"phone,omitempty" yaml:"phone,omitempty"`
}

type invoiceItem struct {
	LineNumber  api.FlexInt `json:"line_number" yaml:"line_number"`
	LineType    api.FlexInt `json:"line_type" yaml:"line_type"`
	ItemCode    string      `json:"item_code,omitempty" yaml:"item_code,omitempty"`
	ItemName    string      `json:"item_name" yaml:"item_name"`
	Unit        string      `json:"unit,omitempty" yaml:"unit,omitempty"`
	Quantity    api.FlexNum `json:"quantity" yaml:"quantity"`
	UnitPrice   api.FlexNum `json:"unit_price" yaml:"unit_price"`
	TotalAmount api.FlexNum `json:"total_amount" yaml:"total_amount"`
	TaxRate     api.FlexNum `json:"tax_rate" yaml:"tax_rate"`
	TaxAmount   api.FlexNum `json:"tax_amount" yaml:"tax_amount"`
}

// invoiceScope is the scope production requires of invoice list and get on top of "read"; the sandbox
// host does not check it. Help, --map and the exit-5 text name it.
const invoiceScope = "einvoice:read"

// invoicePermission names the permission that grants invoiceScope, as the user permission page of
// my.sepay.vn shows it (group › box), so an admin can find the box to tick.
const invoicePermission = "Hóa đơn điện tử (eInvoice) › Xem danh sách hóa đơn điện tử"

// invoiceReadAnnotations mark the invoice reads as production-capable; the group's annotations
// describe the sandbox-only commands.
var invoiceReadAnnotations = map[string]string{
	cmdutil.AnnEnvs:  "both",
	cmdutil.AnnScope: invoiceScope,
}

// documentTypes names each document_type of the e-invoice API; JSON keeps the number.
var documentTypes = map[int]string{
	1: "Draft", 2: "New invoice", 3: "Cancelled", 4: "Replacement", 5: "Replaced",
	6: "Increase adjustment", 7: "Decrease adjustment", 8: "Information adjustment",
	9: "Increase/decrease adjustment", 10: "Adjusted",
}

// documentTypeHelp lists the document_type numbers --type takes with the names TYPE prints.
func documentTypeHelp() string {
	parts := make([]string, 0, len(documentTypes))
	for n := 1; n <= len(documentTypes); n++ {
		parts = append(parts, fmt.Sprintf("%d %s", n, documentTypes[n]))
	}
	return strings.Join(parts, ", ")
}

// documentTypeName names a document_type; an undocumented one prints as its number, a missing one
// (0) as nothing.
func documentTypeName(t int) string {
	if name, ok := documentTypes[t]; ok {
		return name
	}
	if t == 0 {
		return ""
	}
	return strconv.Itoa(t)
}

// providerStatuses names the e-invoice provider's own processing status (provider_status).
var providerStatuses = map[string]string{
	"1": "Draft", "2": "Signing error", "3": "Not sent to tax authority",
	"4": "Issued / Accepted by tax authority", "5": "Tax authority pending",
	"6": "Not qualified for tax code issuance", "7": "[Form 04/SS] Declaration not created",
	"8": "[Form 04/SS] Not accepted by tax authority", "9": "[Form 04/SS] Approved by tax authority",
}

// providerStatusName names a provider_status; an undocumented one prints as sent.
func providerStatusName(s string) string {
	if name, ok := providerStatuses[s]; ok {
		return name
	}
	return s
}

// lineTypeNote is the line_type of a free-text note line, which carries no figures.
const lineTypeNote = 4

// lineTypes names every line_type but plain goods (1).
var lineTypes = map[int]string{2: "promotion", 3: "discount", lineTypeNote: "note"}

// taxRateText prints a line's tax rate, naming the two codes the e-invoice API uses instead of a
// percentage: -2 (not subject to VAT) and -1 (not declared).
func taxRateText(r api.FlexNum) string {
	switch r {
	case -2:
		return "exempt"
	case -1:
		return "undeclared"
	}
	return r.String()
}

// invoiceStatusColor paints an invoice status: issued green, cancelled red, draft yellow.
func invoiceStatusColor(ios *iostreams.IOStreams, status string) string {
	switch status {
	case "issued":
		return ios.Green(status)
	case "cancelled":
		return ios.Red(status)
	case "draft":
		return ios.Yellow(status)
	}
	return status
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// newInvoiceCmd also returns the two commands that read production, for the einvoice env hook.
func newInvoiceCmd(f *cmdutil.Factory) (*cobra.Command, map[*cobra.Command]bool) {
	cmd := &cobra.Command{
		Use:   "invoice <command>",
		Short: "Invoices (create and issue are async: submit, then poll)",
	}
	list, get := newInvoiceListCmd(f), newInvoiceGetCmd(f)
	cmd.AddCommand(
		list,
		get,
		newInvoiceCreateCmd(f),
		newInvoiceCheckCmd(f, "create-status", "/v1/invoices/create/check/", "Poll a create submission to Success/Failed", "create"),
		newInvoiceIssueCmd(f),
		newInvoiceCheckCmd(f, "issue-status", "/v1/invoices/issue/check/", "Poll an issue submission to Success/Failed", "issue"),
		// No backend route yet (404).
		cmdutil.Stub(&cobra.Command{Use: "download <reference-code>", Short: "Download the invoice PDF/XML", Args: cobra.ExactArgs(1)}),
		cmdutil.Stub(&cobra.Command{Use: "delete <reference-code>", Aliases: []string{"rm", "del"}, Short: "Delete a draft invoice", Args: cobra.ExactArgs(1)}),
	)
	return cmd, map[*cobra.Command]bool{list: true, get: true}
}

// tracking is the async 202 response for create/issue.
type tracking struct {
	TrackingCode string `json:"tracking_code" yaml:"tracking_code"`
	TrackingURL  string `json:"tracking_url,omitempty" yaml:"tracking_url,omitempty"`
	Message      string `json:"message,omitempty" yaml:"message,omitempty"`
}

// checkResult is a create/issue status poll (Pending|Success|Failed).
type checkResult struct {
	ReferenceCode string   `json:"reference_code" yaml:"reference_code"`
	Status        string   `json:"status" yaml:"status"`
	Message       string   `json:"message,omitempty" yaml:"message,omitempty"`
	Invoice       *invoice `json:"invoice,omitempty" yaml:"invoice,omitempty"`
}

func newInvoiceCreateCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "create <payload>",
		Aliases: []string{"new"},
		Short:   "Submit an invoice (async); payload is @file, - for stdin, or inline JSON",
		Long: `Submit an invoice for creation. <payload> is the invoice JSON: @path/to/file.json,
- to read stdin, or an inline JSON string. Prompted for on a terminal; required otherwise.

This is asynchronous: the command prints a tracking code, and
"einvoice invoice create-status <tracking-code>" reports the result.`,
		Example: `  sepay einvoice invoice create @invoice.json
  cat invoice.json | sepay einvoice invoice create -`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg, err := f.InputOrArg(cmdutil.FirstArg(args), "an invoice payload", "Invoice JSON (@file, - for stdin, or inline)")
			if err != nil {
				return err
			}
			body, err := readInvoicePayload(f, arg)
			if err != nil {
				return err
			}
			return runInvoiceSubmit(cmd.Context(), f, "/v1/invoices/create", body, "create", "create-status", false)
		},
	}
}

func newInvoiceIssueCmd(f *cmdutil.Factory) *cobra.Command {
	return cmdutil.Annotate(&cobra.Command{
		Use:   "issue <reference-code>",
		Short: "Issue a drafted invoice (async); returns a tracking code",
		Long: `Issue a drafted invoice to the tax authority. This is asynchronous: the command
submits it and prints a tracking code; "einvoice invoice issue-status" reports the result.

<reference-code> is the REF column of "sepay einvoice invoice list" on sandbox. Prompted for on a terminal; required otherwise.`,
		Example: `  sepay einvoice invoice issue <reference-code>
  # then poll the tracking code it prints
  sepay einvoice invoice issue-status <tracking-code>`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := f.InputOrArg(cmdutil.FirstArg(args), "an invoice reference code", "Invoice reference code")
			if err != nil {
				return err
			}
			return runInvoiceSubmit(cmd.Context(), f, "/v1/invoices/issue", map[string]any{"reference_code": ref}, "issue", "issue-status", true)
		},
	}, map[string]string{
		cmdutil.AnnDestructive: "irreversible",
		cmdutil.AnnConfirms:    "always",
		cmdutil.AnnGuidance:    "Issuing sends the invoice to the tax authority. An issued invoice cannot be un-issued; it can only be replaced or adjusted.",
	})
}

// runInvoiceSubmit posts an async create/issue and prints the tracking code.
// irreversible makes the confirm apply in sandbox too (issue goes to the tax authority).
func runInvoiceSubmit(ctx context.Context, f *cmdutil.Factory, path string, body any, action, pollVerb string, irreversible bool) error {
	if err := f.ConfirmMutation(action+" einvoice", "", irreversible); err != nil {
		return err
	}
	client, err := f.NewEInvoiceClient()
	if err != nil {
		return err
	}
	resp, err := client.Post(ctx, path, body)
	if err != nil {
		return err
	}
	tr, err := iei.DataInto[tracking](resp)
	if err != nil {
		return err
	}
	return f.RenderOutput(tr, func() error {
		f.Printf("%s submitted (tracking %s)\n", f.SuccessIcon(), tr.TrackingCode)
		f.ErrPrintf("poll: sepay einvoice invoice %s %s\n", pollVerb, tr.TrackingCode)
		return nil
	})
}

func newInvoiceCheckCmd(f *cmdutil.Factory, use, pathPrefix, short, submitVerb string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <tracking-code>",
		Short: short,
		Long: "Report the status of a submission from \"einvoice invoice " + submitVerb + "\", with the reason when it failed.\n\n" +
			"<tracking-code> is the code \"einvoice invoice " + submitVerb + "\" prints. Prompted for on a terminal; required otherwise.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := f.InputOrArg(cmdutil.FirstArg(args), "a tracking code", "Tracking / reference code")
			if err != nil {
				return err
			}
			refSeg, err := cmdutil.PathSegment("tracking code", ref)
			if err != nil {
				return err
			}
			return runInvoiceCheck(cmd.Context(), f, pathPrefix+refSeg)
		},
	}
}

// runInvoiceCheck polls a create/issue status once.
func runInvoiceCheck(ctx context.Context, f *cmdutil.Factory, path string) error {
	client, err := f.NewEInvoiceClient()
	if err != nil {
		return err
	}
	resp, err := client.Get(ctx, path, nil)
	if err != nil {
		return err
	}
	r, err := iei.DataInto[checkResult](resp)
	if err != nil {
		return err
	}
	failed := strings.EqualFold(r.Status, "Failed")
	if err := f.RenderOutput(r, func() error {
		f.Printf("Ref:     %s\nStatus:  %s\n", r.ReferenceCode, r.Status)
		// Only a Failed message carries information (the provider's reason).
		if failed && r.Message != "" {
			f.Printf("Reason:  %s\n", r.Message)
		}
		if r.Invoice != nil && fullNumber(r.Invoice.InvoiceSeries, r.Invoice.InvoiceNumber) != "" {
			f.Printf("Number:  %s\nTotal:   %s\n",
				fullNumber(r.Invoice.InvoiceSeries, r.Invoice.InvoiceNumber), jsonfmt.FormatMoney(float64(r.Invoice.TotalAmount)))
		}
		return nil
	}); err != nil {
		return err
	}
	// Failed exits non-zero so scripts can detect it.
	if failed {
		return cmdutil.ErrSilent
	}
	return nil
}

// readInvoicePayload reads the create payload from @file, - or inline JSON and requires a boolean is_draft.
// Bytes are returned verbatim so large numeric IDs survive.
func readInvoicePayload(f *cmdutil.Factory, arg string) (json.RawMessage, error) {
	var raw []byte
	var err error
	switch {
	case arg == "-":
		raw, err = io.ReadAll(f.IOStreams.In)
	case strings.HasPrefix(arg, "@"):
		raw, err = os.ReadFile(filepath.Clean(arg[1:]))
	default:
		raw = []byte(arg)
	}
	if err != nil {
		return nil, fmt.Errorf("could not read payload: %w", err)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, &cmdutil.FlagError{Err: fmt.Errorf("invalid JSON payload: %w", err)}
	}
	v, ok := probe["is_draft"]
	if !ok {
		return nil, &cmdutil.FlagError{Err: fmt.Errorf("payload must include is_draft (true or false)")}
	}
	if _, isBool := v.(bool); !isBool {
		return nil, &cmdutil.FlagError{Err: fmt.Errorf("is_draft must be a boolean (true or false)")}
	}
	return json.RawMessage(raw), nil
}

// getInvoice reads one invoice through API v2 with the user's token on both envs.
func getInvoice(ctx context.Context, f *cmdutil.Factory, refSeg string) (invoice, error) {
	client, err := f.NewAPIClient()
	if err != nil {
		return invoice{}, err
	}
	// The server's router urldecodes the path, which reads a bare "+" as a space.
	env, err := client.Get(ctx, "/einvoice/invoices/"+strings.ReplaceAll(refSeg, "+", "%2B"), nil)
	if err != nil {
		return invoice{}, cmdutil.ReadScopeError(err, invoiceScope, invoicePermission)
	}
	return api.DataInto[invoice](env)
}

// invoiceSources and invoiceStatuses are what --source and --status take; the API refuses any other
// value with a 422. --status leaves out pending, which the API also takes: the portal list hides
// pending invoices, and leaving them out of an unfiltered list is up to the server.
var (
	invoiceSources  = []string{"manual", "api"}
	invoiceStatuses = []string{"draft", "issued", "failed", "cancelled"}
)

func newInvoiceListCmd(f *cmdutil.Factory) *cobra.Command {
	var source, status, from, to, search, docType string
	var page, perPage int
	cmd := cmdutil.Annotate(&cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List invoices",
		Long: `List invoices with their number, the invoice they replace or adjust, reference code,
status and total. The filters work as on the portal page einvoice/documents and combine
with AND: --source separates invoices made in the portal (manual) from those made through
the API; --from and --to bound the invoice date, so an invoice with none is left out;
--search looks in the series and number, reference code and the buyer's name, tax code,
phone and email.

The REF column is the <reference-code> "einvoice invoice get" takes; on sandbox, "issue" takes it too.`,
		Example: `  sepay einvoice invoice list --from 2026-11-01 --to 2026-11-10 --status issued --search 1C26TAA
  sepay einvoice invoice list --type 4
  sepay einvoice invoice list --source api --page 2 --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmdutil.ValidateChoices(cmd, map[string][]string{"source": invoiceSources, "status": invoiceStatuses}); err != nil {
				return err
			}
			if err := cmdutil.ValidateDateOnly(cmd, "from", "to"); err != nil {
				return err
			}
			if err := cmdutil.ValidateDateOrder(cmd, "from", "to"); err != nil {
				return err
			}
			fs := cmd.Flags()
			search = strings.TrimSpace(search)
			if (fs.Changed("search") || fs.Changed("q")) && search == "" {
				return &cmdutil.FlagError{Err: fmt.Errorf("--%s must not be empty", cmdutil.SpelledAs(cmd, "search"))}
			}
			if fs.Changed("type") {
				n, err := strconv.Atoi(docType)
				if err != nil || n < 1 || n > 10 {
					return &cmdutil.FlagError{Err: fmt.Errorf("--type must be a number from 1 to 10")}
				}
				docType = strconv.Itoa(n)
			}
			query := url.Values{}
			for param, val := range map[string]string{
				"source":           source,
				"status":           status,
				"from_issued_date": from,
				"to_issued_date":   to,
				"q":                search,
				"document_type":    docType,
			} {
				if val != "" {
					query.Set(param, val)
				}
			}
			if page > 0 {
				query.Set("page", strconv.Itoa(page))
			}
			if perPage > 0 {
				query.Set("per_page", strconv.Itoa(perPage))
			}
			return runInvoiceList(cmd.Context(), f, query)
		},
	}, invoiceReadAnnotations)
	fl := cmd.Flags()
	fl.StringVar(&source, "source", "", "Filter by source: "+strings.Join(invoiceSources, "|"))
	fl.StringVar(&from, "from", "", "Invoice date from (YYYY-MM-DD)")
	fl.StringVar(&to, "to", "", "Invoice date to (YYYY-MM-DD)")
	fl.StringVar(&status, "status", "", "Filter by status: "+strings.Join(invoiceStatuses, "|"))
	fl.StringVar(&docType, "type", "", "Filter by invoice type: "+documentTypeHelp())
	fl.StringVar(&search, "search", "", "Search the series and number, reference code and buyer details")
	cmdutil.AliasFlag(fl, "q", "search")
	fl.IntVar(&page, "page", 0, "Page number")
	fl.IntVar(&perPage, "per-page", 0, "Rows per page (max 100)")
	return cmd
}

// listInvoices reads one page through API v2 with the user's token on both envs: the e-invoice API
// has no date, status or search filter.
func listInvoices(ctx context.Context, f *cmdutil.Factory, query url.Values) ([]invoice, *iei.Paging, error) {
	client, err := f.NewAPIClient()
	if err != nil {
		return nil, nil, err
	}
	env, err := client.Get(ctx, "/einvoice/invoices", query)
	if err != nil {
		return nil, nil, cmdutil.ReadScopeError(err, invoiceScope, invoicePermission)
	}
	invoices, p, err := api.ListInto[invoice](env)
	if err != nil {
		return nil, nil, err
	}
	var paging *iei.Paging
	if p != nil {
		paging = &iei.Paging{Total: p.Total, CurrentPage: p.CurrentPage, PageCount: p.LastPage}
	}
	return invoices, paging, nil
}

func runInvoiceList(ctx context.Context, f *cmdutil.Factory, query url.Values) error {
	invoices, paging, err := listInvoices(ctx, f, query)
	if err != nil {
		return err
	}

	return f.RenderOutput(invoices, func() error {
		if len(invoices) == 0 {
			fmt.Fprintln(f.IOStreams.ErrOut, "No invoices found.")
			return nil
		}
		t := jsonfmt.NewTable("NUMBER", "ORIGINAL", "REF", "TYPE", "STATUS", "ISSUED", "BUYER", "TOTAL").
			Truncatable("BUYER").Droppable("ORIGINAL", "TYPE", "BUYER")
		for _, in := range invoices {
			buyerName := ""
			if in.Buyer != nil {
				buyerName = in.Buyer.Name
			}
			t.AddRow(jsonfmt.OrDash(fullNumber(in.InvoiceSeries, in.InvoiceNumber)), jsonfmt.OrDash(in.OriginalInvoice.number()),
				in.ReferenceCode, documentTypeName(in.DocumentType),
				invoiceStatusColor(f.IOStreams, in.Status), jsonfmt.OrDash(in.IssuedDate), jsonfmt.OrDash(buyerName),
				jsonfmt.FormatMoney(float64(in.TotalAmount)))
		}
		if err := f.RenderTable(t); err != nil {
			return err
		}
		printPaging(f, paging, len(invoices))
		return nil
	})
}

func newInvoiceGetCmd(f *cmdutil.Factory) *cobra.Command {
	return cmdutil.Annotate(&cobra.Command{
		Use:   "get <reference-code>",
		Short: "Show an invoice and its line items",
		Long: `Show an invoice and its line items.

<reference-code> is the REF column of "sepay einvoice invoice list". Prompted for on a terminal; required otherwise.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := f.InputOrArg(cmdutil.FirstArg(args), "an invoice reference code", "Invoice reference code")
			if err != nil {
				return err
			}
			return runInvoiceGet(cmd.Context(), f, ref)
		},
	}, invoiceReadAnnotations)
}

func runInvoiceGet(ctx context.Context, f *cmdutil.Factory, ref string) error {
	refSeg, err := cmdutil.PathSegment("reference code", ref)
	if err != nil {
		return err
	}
	in, err := getInvoice(ctx, f, refSeg)
	if err != nil {
		return err
	}

	return f.RenderOutput(in, func() error {
		printInvoice(f, in)
		if len(in.Items) == 0 {
			return nil
		}
		fmt.Fprintln(f.IOStreams.Out)
		t := jsonfmt.NewTable("#", "ITEM", "QTY", "UNIT PRICE", "TAX%", "AMOUNT").Truncatable("ITEM").KeepsTail("ITEM")
		for _, it := range in.Items {
			t.AddRow(itemCells(it)...)
		}
		return f.RenderTable(t)
	})
}

// fullNumber is the number as the invoice shows it, its series then its number ("C26MMM330"), or ""
// before the provider assigns one: a sandbox draft carries "0", and a real number starts at 1.
func fullNumber(series, number string) string {
	if strings.Trim(number, "0") == "" {
		return ""
	}
	return series + number
}

// number is the original's number as fullNumber writes it, or "" when there is no original.
func (o *original) number() string {
	if o == nil {
		return ""
	}
	return fullNumber(deref(o.InvoiceSeries), deref(o.InvoiceNumber))
}

// line is the original's number, then its reference code in parentheses when it has one.
func (o *original) line() string {
	if o == nil {
		return ""
	}
	ref := deref(o.ReferenceCode)
	if ref == "" {
		return jsonfmt.OrDash(o.number())
	}
	return jsonfmt.OrDash(o.number()) + " (" + ref + ")"
}

// printInvoice writes the invoice header in blocks: the document, its buyer, then the money and
// files. A field the server left empty is skipped, except Number, Issued and Payment, which print "-"
// so a draft reads as not issued yet, and Subtotal, Tax and Total, which print 0.
func printInvoice(f *cmdutil.Factory, in invoice) {
	line := func(label, value string) {
		if value != "" {
			f.Printf("%-11s %s\n", label, value)
		}
	}
	line("Number:", jsonfmt.OrDash(fullNumber(in.InvoiceSeries, in.InvoiceNumber)))
	line("Original:", in.OriginalInvoice.line())
	line("Ref:", in.ReferenceCode)
	line("Type:", documentTypeName(in.DocumentType))
	line("Status:", invoiceStatusColor(f.IOStreams, in.Status))
	line("Issued:", jsonfmt.OrDash(in.IssuedDate))
	line("Payment:", jsonfmt.OrDash(in.PaymentMethod))
	line("Source:", in.Source)
	line("Auth code:", deref(in.TaxAuthorityCode))
	line("Provider:", providerStatusName(deref(in.ProviderStatus)))
	if b := in.Buyer; b != nil {
		fmt.Fprintln(f.IOStreams.Out)
		name := b.Name
		if b.Type != "" {
			name += " " + f.IOStreams.Dim("("+b.Type+")")
		}
		line("Buyer:", name)
		line("  Legal:", b.LegalName)
		line("  Tax ID:", b.TaxCode)
		line("  Address:", b.Address)
		line("  Email:", b.Email)
		line("  Phone:", b.Phone)
	}
	fmt.Fprintln(f.IOStreams.Out)
	line("Subtotal:", jsonfmt.FormatMoney(float64(in.TotalBeforeTax)))
	line("Tax:", jsonfmt.FormatMoney(float64(in.TaxAmount)))
	line("Total:", jsonfmt.FormatMoney(float64(in.TotalAmount)))
	line("Notes:", in.Notes)
	line("PDF:", deref(in.PdfURL))
	line("XML:", deref(in.XMLURL))
}

// itemCells renders one line item: its code and its kind (when it is not plain goods) after the
// name, kept whole when a narrow terminal cuts the name, and its unit after the quantity. A note
// line carries only text; a line without a quantity (a trade discount) leaves QTY and UNIT PRICE
// empty. TAX% shows a line's tax rate; its tax amount is in --json only.
func itemCells(it invoiceItem) []string {
	var tail string
	if it.ItemCode != "" {
		tail += " (" + jsonfmt.Clean(it.ItemCode) + ")"
	}
	if kind := lineTypes[int(it.LineType)]; kind != "" {
		tail += " · " + kind
	}
	name := jsonfmt.Clean(it.ItemName)
	if tail != "" {
		name += jsonfmt.TailSep + tail
	}
	cells := []string{strconv.Itoa(int(it.LineNumber)), name, "", "", "", ""}
	if it.LineType == lineTypeNote {
		return cells
	}
	if it.Quantity != 0 {
		cells[2] = strings.TrimSpace(it.Quantity.String() + " " + it.Unit)
		cells[3] = jsonfmt.FormatMoney(float64(it.UnitPrice))
	}
	cells[4] = taxRateText(it.TaxRate)
	cells[5] = jsonfmt.FormatMoney(float64(it.TotalAmount))
	return cells
}
