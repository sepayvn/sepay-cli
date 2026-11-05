package pg

import (
	"net/http"
	"testing"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// checkoutInit answers the POST with a 302 to loc, the payment page URL.
func checkoutInit(loc string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/checkout/init" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Location", loc)
		w.WriteHeader(http.StatusFound)
	}
}

func runCheckout(t *testing.T, loc string, tty bool, extra ...string) (string, string, error) {
	t.Helper()
	f, out, errOut := testFactory(t, okJSON(brokerPGBasic), checkoutInit(loc))
	f.IOStreams.SetStdoutTTY(tty)
	args := append([]string{"checkout", "init", "--amount", "150000", "--description", "e2e", "--invoice", "INV-1"}, extra...)
	err := run(f, args...)
	return out.String(), errOut.String(), err
}

func TestCheckoutInit_RejectsStrayArguments(t *testing.T) {
	_, _, err := runCheckout(t, "https://pay.sepay.vn/v1/checkout", false, "50000")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

// pay.sepay.vn is not in the credential allowlist, so the gate must accept it by zone.
func TestCheckoutInit_RealCheckoutHostIsAccepted(t *testing.T) {
	const loc = "https://pay.sepay.vn/v1/checkout?merchant=SP-TEST-1&order=1"
	out, errOut, err := runCheckout(t, loc, true)
	require.NoError(t, err)
	assert.Contains(t, out, loc, "the URL is always printed")
	assert.NotContains(t, errOut, "not opening a browser", "a SePay checkout host must not be refused")
}

func TestCheckoutInit_ForeignHostDegradesToPrint(t *testing.T) {
	const loc = "https://pay.sepay.vn.attacker.io/v1/checkout?merchant=SP-TEST-1"
	out, errOut, err := runCheckout(t, loc, true)
	require.NoError(t, err, "a foreign host must degrade, never fail the command")
	assert.Contains(t, errOut, "not opening a browser")
	assert.Contains(t, out, loc, "the URL is still printed so the payment can be completed")
}

// Only C1 bytes survive Go's header validation, and jsonfmt.Clean strips them.
func TestCheckoutInit_PrintIsSanitised(t *testing.T) {
	const c1 = "\u009b"
	loc := "https://pay.sepay.vn/v1/checkout?x=" + c1 + "2Jgotcha"
	out, _, err := runCheckout(t, loc, false)
	require.NoError(t, err)
	assert.NotContains(t, out, c1, "a C1 control byte must not reach the terminal")
	assert.Contains(t, out, "gotcha", "only the control byte is removed, not the text")
}

func TestCheckoutInit_NoOpenIsQuiet(t *testing.T) {
	const loc = "https://evil.example/v1/checkout"
	out, errOut, err := runCheckout(t, loc, true, "--no-open")
	require.NoError(t, err)
	assert.NotContains(t, errOut, "not opening a browser")
	assert.Contains(t, out, loc)
}

func TestCheckoutInit_JSONKeepsTheRawURL(t *testing.T) {
	const loc = "https://pay.sepay.vn/v1/checkout?merchant=SP-TEST-1"
	f, out, _ := testFactory(t, okJSON(brokerPGBasic), checkoutInit(loc))
	f.Global.Output = "json"
	require.NoError(t, run(f, "checkout", "init", "--amount", "150000", "--description", "e2e", "--invoice", "INV-1"))
	assert.Contains(t, out.String(), "checkout_url")
	assert.Contains(t, out.String(), loc)
	assert.NotContains(t, out.String(), "\x1b[", "machine output carries no ANSI")
}

// merchantThenInit serves the pre-flight GET and the checkout POST, counting each.
func merchantThenInit(merchantBody, loc string, merchantHits, initHits *int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/merchant":
			*merchantHits++
			if merchantBody == "" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(merchantBody))
		case "/v1/checkout/init":
			*initHits++
			w.Header().Set("Location", loc)
			w.WriteHeader(http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

type preflightRun struct {
	out, errOut  string
	err          error
	merchantHits int
	initHits     int
}

func runCheckoutAgainst(t *testing.T, merchantBody string, extra ...string) preflightRun {
	t.Helper()
	const loc = "https://pay.sepay.vn/v1/checkout?merchant=SP-TEST-1"
	r := preflightRun{}
	f, out, errOut := testFactory(t, okJSON(brokerPGBasic), merchantThenInit(merchantBody, loc, &r.merchantHits, &r.initHits))
	f.IOStreams.SetStdoutTTY(false)
	args := append([]string{"checkout", "init", "--amount", "150000", "--description", "e2e", "--invoice", "INV-1"}, extra...)
	r.err = run(f, args...)
	r.out, r.errOut = out.String(), errOut.String()
	return r
}

const allMethodsOff = `{"payment_methods":[
	{"payment_method":"BANK_TRANSFER","active":0},
	{"payment_method":"NAPAS_BANK_TRANSFER","active":0},
	{"payment_method":"CARD","active":0}]}`

const onlyBankTransferOn = `{"payment_methods":[
	{"payment_method":"BANK_TRANSFER","active":1},
	{"payment_method":"NAPAS_BANK_TRANSFER","active":0},
	{"payment_method":"CARD","active":0}]}`

func TestCheckoutInit_NoActivePaymentMethodIsNamed(t *testing.T) {
	r := runCheckoutAgainst(t, allMethodsOff)
	require.Error(t, r.err)
	assert.Contains(t, r.err.Error(), "no active payment method")
	assert.Contains(t, r.err.Error(), "sepay pg merchant get", "the error has to say how to check")
	assert.Equal(t, 0, r.initHits, "nothing may be created server-side once the environment is known to be unusable")
	assert.Equal(t, 1, r.merchantHits)
	assert.EqualValues(t, 1, cmdutil.ExitCodeForError(r.err), "a merchant-state refusal is not a usage error")
}

// Exit 1, not a usage error: the enum value is valid.
func TestCheckoutInit_InactiveRequestedMethodIsNamed(t *testing.T) {
	r := runCheckoutAgainst(t, onlyBankTransferOn, "--payment-method", "CARD")
	require.Error(t, r.err)
	assert.Contains(t, r.err.Error(), "--payment-method CARD is not active")
	assert.Contains(t, r.err.Error(), "BANK_TRANSFER", "the active set is what makes it actionable")
	assert.Equal(t, 0, r.initHits)
	assert.EqualValues(t, 1, cmdutil.ExitCodeForError(r.err), "a valid enum value rejected by merchant state must not exit 2")
}

func TestCheckoutInit_ActiveMethodOutsideCheckoutSetIsNotUsable(t *testing.T) {
	r := runCheckoutAgainst(t, `{"payment_methods":[{"payment_method":"NAPAS_CARD","active":1}]}`)
	require.Error(t, r.err)
	assert.Contains(t, r.err.Error(), "no active payment method")
	assert.Equal(t, 0, r.initHits)
}

func TestCheckoutInit_EmptyPaymentMethodListBlocks(t *testing.T) {
	r := runCheckoutAgainst(t, `{"payment_methods":[]}`)
	require.Error(t, r.err)
	assert.Contains(t, r.err.Error(), "no active payment method")
	assert.Equal(t, 0, r.initHits)
}

func TestCheckoutInit_ActiveMethodStillProceeds(t *testing.T) {
	r := runCheckoutAgainst(t, onlyBankTransferOn, "--payment-method", "BANK_TRANSFER")
	require.NoError(t, r.err)
	assert.Equal(t, 1, r.merchantHits)
	assert.Equal(t, 1, r.initHits)
	assert.Contains(t, r.out, "https://pay.sepay.vn/v1/checkout")
}

func TestCheckoutInit_PreflightFailsOpenOnReadError(t *testing.T) {
	r := runCheckoutAgainst(t, "")
	require.NoError(t, r.err)
	assert.Equal(t, 1, r.merchantHits, "the pre-flight must actually have asked")
	assert.Equal(t, 1, r.initHits)
	assert.Contains(t, r.out, "https://pay.sepay.vn/v1/checkout")
}

func TestCheckoutInit_PreflightFailsOpenOnMissingKey(t *testing.T) {
	r := runCheckoutAgainst(t, `{"payment_method_applicable_order_prices":null}`)
	require.NoError(t, r.err)
	assert.Equal(t, 1, r.merchantHits, "the pre-flight must actually have asked")
	assert.Equal(t, 1, r.initHits)
	assert.Contains(t, r.out, "https://pay.sepay.vn/v1/checkout")
}
