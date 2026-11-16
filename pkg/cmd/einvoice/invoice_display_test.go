package einvoice

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDocumentTypeName(t *testing.T) {
	for in, want := range map[int]string{
		1: "Draft", 2: "New invoice", 3: "Cancelled", 4: "Replacement", 5: "Replaced", 6: "Increase adjustment",
		7: "Decrease adjustment", 8: "Information adjustment", 9: "Increase/decrease adjustment", 10: "Adjusted",
		0: "", 11: "11",
	} {
		assert.Equal(t, want, documentTypeName(in), in)
	}
}

// The number reads as printed on the invoice, series first; a series without a number, or with the
// "0" a draft carries, has none yet.
func TestFullNumber(t *testing.T) {
	for _, tc := range []struct {
		series, number, want string
	}{
		{"C26MMM", "330", "C26MMM330"},
		{"", "330", "330"},
		{"C26MMM", "", ""},
		{"", "", ""},
		{"C26TSE", "0", ""},
		{"C26TSE", "000", ""},
		{"1C26TSP", "0000125", "1C26TSP0000125"},
		{"C26MMM", "10", "C26MMM10"},
	} {
		assert.Equal(t, tc.want, fullNumber(tc.series, tc.number), tc)
	}
}

func TestProviderStatusName(t *testing.T) {
	for in, want := range map[string]string{
		"1": "Draft", "2": "Signing error", "3": "Not sent to tax authority", "4": "Issued / Accepted by tax authority",
		"5": "Tax authority pending", "6": "Not qualified for tax code issuance", "7": "[Form 04/SS] Declaration not created",
		"8": "[Form 04/SS] Not accepted by tax authority", "9": "[Form 04/SS] Approved by tax authority",
		"": "", "42": "42",
	} {
		assert.Equal(t, want, providerStatusName(in), in)
	}
}

func TestTaxRateText(t *testing.T) {
	for in, want := range map[api.FlexNum]string{-2: "exempt", -1: "undeclared", 0: "0", 5: "5", 8: "8", 10: "10"} {
		assert.Equal(t, want, taxRateText(in), in)
	}
}

func TestInvoiceStatusColor(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	ios.SetColorEnabled(true)
	assert.Equal(t, "\x1b[32missued\x1b[0m", invoiceStatusColor(ios, "issued"))
	assert.Equal(t, "\x1b[31mcancelled\x1b[0m", invoiceStatusColor(ios, "cancelled"))
	assert.Equal(t, "\x1b[33mdraft\x1b[0m", invoiceStatusColor(ios, "draft"))
	assert.Equal(t, "replaced", invoiceStatusColor(ios, "replaced"), "an unknown status stays plain")

	ios.SetColorEnabled(false)
	assert.Equal(t, "issued", invoiceStatusColor(ios, "issued"))
}

// Color reaches the terminal only; JSON stays plain even when color is on.
func TestInvoiceRead_ColorsOnlyTheTerminal(t *testing.T) {
	f, out, _, _ := prodFactory(t, okJSON(prodInvoiceList))
	f.IOStreams.SetColorEnabled(true)
	require.NoError(t, run(f, "invoice", "list"))
	assert.Contains(t, out.String(), "\x1b[32missued\x1b[0m")
	assert.Contains(t, out.String(), "\x1b[33mdraft\x1b[0m")

	f, out, _, _ = prodFactory(t, okJSON(prodInvoiceFull))
	f.IOStreams.SetColorEnabled(true)
	require.NoError(t, run(f, "invoice", "get", "DH1009"))
	assert.Contains(t, out.String(), "Status:     \x1b[32missued\x1b[0m")
	assert.Contains(t, out.String(), "Buyer:      Công ty A \x1b[2m(company)\x1b[0m")

	for _, tc := range []struct {
		args []string
		body string
	}{{[]string{"invoice", "list"}, prodInvoiceList}, {[]string{"invoice", "get", "DH1009"}, prodInvoiceFull}} {
		f, out, _, _ = prodFactory(t, okJSON(tc.body))
		f.IOStreams.SetColorEnabled(true)
		f.Global.Output = "json"
		require.NoError(t, run(f, tc.args...))
		assert.NotContains(t, out.String(), "\x1b", "machine output carries no color")
	}
}

func TestItemCells(t *testing.T) {
	for _, tc := range []struct {
		name string
		item invoiceItem
		want []string
	}{
		{
			"goods with code and unit",
			invoiceItem{LineNumber: 1, LineType: 1, ItemCode: "SP01", ItemName: "Áo", Unit: "cái", Quantity: 2, UnitPrice: 100000, TotalAmount: 200000, TaxRate: 10},
			[]string{"1", "Áo" + jsonfmt.TailSep + " (SP01)", "2 cái", "100.000", "10", "200.000"},
		},
		{
			"no unit leaves no trailing space",
			invoiceItem{LineNumber: 2, LineType: 1, ItemName: "Tư vấn", Quantity: 1.5, UnitPrice: 20000, TotalAmount: 30000, TaxRate: -1},
			[]string{"2", "Tư vấn", "1.5", "20.000", "undeclared", "30.000"},
		},
		{
			"discount without a quantity",
			invoiceItem{LineNumber: 3, LineType: 3, ItemName: "Chiết khấu", TotalAmount: -10000, TaxRate: 10},
			[]string{"3", "Chiết khấu" + jsonfmt.TailSep + " · discount", "", "", "10", "-10.000"},
		},
		{
			"a control character in the code cannot move the tail",
			invoiceItem{LineNumber: 5, LineType: 1, ItemCode: "SP\x1f01", ItemName: "Áo\x1f", Quantity: 1},
			[]string{"5", "Áo" + jsonfmt.TailSep + " (SP01)", "1", "0", "0", "0"},
		},
		{
			"note carries text only",
			invoiceItem{LineNumber: 4, LineType: lineTypeNote, ItemName: "Ghi chú", Quantity: 1, TotalAmount: 5},
			[]string{"4", "Ghi chú" + jsonfmt.TailSep + " · note", "", "", "", ""},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, itemCells(tc.item))
		})
	}
}

// On a narrow terminal the BUYER column is cut so the row fits; ids, dates and amounts are not.
func TestInvoiceList_BuyerIsCutOnANarrowTerminal(t *testing.T) {
	body := strings.Replace(prodInvoiceList, `"name":"Công ty A",`, `"name":"Công ty trách nhiệm hữu hạn thương mại dịch vụ A",`, 1)
	f, out, _, _ := prodFactory(t, okJSON(body))
	f.IOStreams.SetTerminalWidth(72)

	require.NoError(t, run(f, "invoice", "list"))

	assert.Contains(t, out.String(), "…")
	assert.Contains(t, out.String(), "110.000")
	for _, l := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		assert.LessOrEqual(t, utf8.RuneCountInString(l), 72, l)
	}
}

// At 80 columns the list leaves out TYPE, then BUYER, and keeps the reference code whole.
func TestInvoiceList_DropsColumnsOnAnEightyColumnTerminal(t *testing.T) {
	body := strings.Replace(prodInvoiceList, `"reference_code":"DH1007"`, `"reference_code":"084e179d-d95a-11f0-aef4-52c7e9b4f41b"`, 1)
	f, out, _, _ := prodFactory(t, okJSON(body))
	f.IOStreams.SetTerminalWidth(80)

	require.NoError(t, run(f, "invoice", "list"))

	assert.Equal(t, "NUMBER          REF                                   STATUS  ISSUED      TOTAL\n"+
		"1C26TSP0000123  084e179d-d95a-11f0-aef4-52c7e9b4f41b  issued  2026-11-10  110.000\n"+
		"-               DH1006                                draft   -           0\n", out.String())
}

// At 80 columns a long item name is cut before its code and kind, which stay whole.
func TestInvoiceGet_ItemKeepsItsCodeOnAnEightyColumnTerminal(t *testing.T) {
	body := strings.Replace(prodInvoiceFull, `"item_name":"Móc khóa quà tặng"`, `"item_name":"Móc khóa quà tặng in logo công ty bằng kim loại không gỉ"`, 1)
	f, out, _, _ := prodFactory(t, okJSON(body))
	f.IOStreams.SetTerminalWidth(80)

	require.NoError(t, run(f, "invoice", "get", "DH1009"))

	assert.Regexp(t, `(?m)^2  Móc khóa quà tặng… \(KM01\) · promotion  1 cái  0`, out.String())
	for _, l := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		assert.LessOrEqual(t, utf8.RuneCountInString(l), 80, l)
	}
}

// At 90 columns only TYPE goes; BUYER stays, cut.
func TestInvoiceList_DropsTypeBeforeBuyer(t *testing.T) {
	body := strings.Replace(prodInvoiceList, `"reference_code":"DH1007"`, `"reference_code":"084e179d-d95a-11f0-aef4-52c7e9b4f41b"`, 1)
	f, out, _, _ := prodFactory(t, okJSON(body))
	f.IOStreams.SetTerminalWidth(90)

	require.NoError(t, run(f, "invoice", "list"))

	assert.NotContains(t, out.String(), "TYPE")
	assert.Contains(t, out.String(), "BUYER")
}

// A unit separator in a buyer name is data, not a tail: the name is cut at its end and TYPE stays.
func TestInvoiceList_ABuyerNameCannotForgeATail(t *testing.T) {
	body := strings.Replace(prodInvoiceList, `"name":"Công ty A",`, `"name":"Công ty\u001ftrách nhiệm hữu hạn thương mại dịch vụ A",`, 1)
	f, out, _, _ := prodFactory(t, okJSON(body))
	f.IOStreams.SetTerminalWidth(80)

	require.NoError(t, run(f, "invoice", "list"))

	assert.Contains(t, out.String(), "TYPE")
	assert.Contains(t, out.String(), "New invoice")
	assert.Contains(t, out.String(), "Công tytrách …")
	assert.NotContains(t, out.String(), "\x1f")
	for _, l := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		assert.LessOrEqual(t, utf8.RuneCountInString(l), 80, l)
	}
}

// Narrower than the columns that cannot go, the reference code still prints whole.
func TestInvoiceList_NeverDropsTheReferenceCode(t *testing.T) {
	body := strings.Replace(prodInvoiceList, `"reference_code":"DH1007"`, `"reference_code":"084e179d-d95a-11f0-aef4-52c7e9b4f41b"`, 1)
	f, out, _, _ := prodFactory(t, okJSON(body))
	f.IOStreams.SetTerminalWidth(40)

	require.NoError(t, run(f, "invoice", "list"))

	assert.Regexp(t, `^NUMBER +REF +STATUS +ISSUED +TOTAL\n`, out.String())
	assert.Contains(t, out.String(), "084e179d-d95a-11f0-aef4-52c7e9b4f41b")
}
