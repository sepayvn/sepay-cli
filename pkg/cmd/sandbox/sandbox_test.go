package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const banksJSON = `{"status":"success","data":[{"code":"ACB","short_name":"ACB"},{"code":"BIDV","short_name":"BIDV"}]}`

func testFactory(t *testing.T, h http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	return bankListFactory(t, banksJSON, h)
}

// bankListFactory is testFactory serving banks as the supported bank list.
func bankListFactory(t *testing.T, banks string, h http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The bank list a --bank code is checked against and the default comes from.
		if r.Method == http.MethodGet && r.URL.Path == "/banks" {
			_, _ = w.Write([]byte(banks))
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	f.NewAPIClient = func() (*api.Client, error) {
		c := api.NewClient(srv.URL, "test")
		c.MaxRetries = 0
		return c, nil
	}
	return f, out, errOut
}

func run(cmd *cobra.Command, args ...string) error {
	cmd.SetArgs(args)
	return cmd.ExecuteContext(context.Background())
}

func TestSimulate_PostsBody(t *testing.T) {
	var method, path string
	var body map[string]any
	f, out, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"xid":"tx_new"}}`))
	})
	require.NoError(t, run(NewCmdSandbox(f), "simulate", "payment_in",
		"--bank-account", "ba_1", "--amount", "100000", "--code", "DH1"))

	assert.Equal(t, http.MethodPost, method)
	assert.Equal(t, "/transactions/simulate", path)
	assert.Equal(t, "payment_in", body["type"])
	assert.Equal(t, "ba_1", body["bank_account_xid"])
	assert.EqualValues(t, 100000, body["amount"])
	assert.Equal(t, "DH1", body["code"])
	assert.Contains(t, out.String(), "tx_new")
	assert.Contains(t, errOut.String(), "Simulated payment_in")
}

func TestSimulate_RejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"non-positive amount", []string{"simulate", "payment_in", "--bank-account", "ba_1", "--amount=-5"}},
		{"non-content override", []string{"simulate", "payment_in", "--bank-account", "ba_1", "--amount", "1000", "--override", "type=x"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
				assert.Fail(t, "must not call the server on invalid input")
			})
			err := run(NewCmdSandbox(f), tt.args...)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		})
	}
}

func TestSimulate_EventAsFlag(t *testing.T) {
	var body map[string]any
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"xid":"tx_new"}}`))
	})
	require.NoError(t, run(NewCmdSandbox(f), "simulate",
		"--event", "payment_in", "--bank-account", "ba_1", "--amount", "100000"))
	assert.Equal(t, "payment_in", body["type"])
}

func TestSimulate_JSON(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"xid":"tx_new"}}`))
	})
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdSandbox(f), "simulate", "payment_in",
		"--bank-account", "ba_1", "--amount", "100000"))
	assert.Contains(t, out.String(), `"xid": "tx_new"`)
}

func TestSimulate_ServerWarningToStderr(t *testing.T) {
	f, out, errOut := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"xid":"tx_new","warnings":["accumulated_not_updated"]}}`))
	})
	// A degraded side effect still succeeds (exit 0, xid on stdout) but is said out loud.
	require.NoError(t, run(NewCmdSandbox(f), "simulate", "payment_in",
		"--bank-account", "ba_1", "--amount", "100000"))
	assert.Contains(t, out.String(), "tx_new")
	assert.Contains(t, errOut.String(), "accumulated balance was not updated")
}

func TestSimulate_VaCreditSendsVaNumber(t *testing.T) {
	var body map[string]any
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"xid":"tx_new"}}`))
	})
	require.NoError(t, run(NewCmdSandbox(f), "simulate", "va_credit",
		"--bank-account", "ba_1", "--amount", "5000", "--va", "SEPAY99"))
	assert.Equal(t, "SEPAY99", body["va_number"])
}

func TestSimulate_MissingRequiredNonInteractive(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		assert.Fail(t, "must not reach the server when a required input is missing")
	})
	err := run(NewCmdSandbox(f), "simulate", "payment_in", "--amount", "1000")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

// "--amount 0" is a value the caller typed, not a missing flag: it must be
// refused for being zero, not reported as absent.
func TestSimulate_BadAmount(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"explicit zero", []string{"--bank-account", "ba_1", "--amount", "0"}, "--amount must be between 1000 and 499000000"},
		{"below the minimum", []string{"--bank-account", "ba_1", "--amount", "999"}, "--amount must be between 1000 and 499000000"},
		{"above the maximum", []string{"--bank-account", "ba_1", "--amount", "499000001"}, "--amount must be between 1000 and 499000000"},
		{"va on another event", []string{"--bank-account", "ba_1", "--amount", "5000", "--va", "SB1"}, "--va applies to va_credit only, not payment_in"},
		{"omitted", []string{"--bank-account", "ba_1"}, "--amount is required"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
				assert.Fail(t, "must not reach the server with an unusable amount")
			})
			err := run(NewCmdSandbox(f), append([]string{"simulate", "payment_in"}, tt.args...)...)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.EqualError(t, err, tt.want)
		})
	}
}

func TestSimulate_Override(t *testing.T) {
	var body map[string]any
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"xid":"tx_new"}}`))
	})
	require.NoError(t, run(NewCmdSandbox(f), "simulate", "payment_in",
		"--bank-account", "ba_1", "--amount", "1000", "--override", "content=hello"))
	ov, ok := body["overrides"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "hello", ov["content"])
}

func TestSeed_CreatesCount(t *testing.T) {
	var count int32
	var lastBody map[string]any
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/transactions/simulate" {
			atomic.AddInt32(&count, 1)
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &lastBody)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"xid":"tx"}}`))
	})
	require.NoError(t, run(NewCmdSandbox(f), "seed", "--bank-account", "ba_1", "--count", "3"))
	assert.Equal(t, int32(3), atomic.LoadInt32(&count))
	assert.Equal(t, "ba_1", lastBody["bank_account_xid"])
	assert.Contains(t, errOut.String(), "Seeded 3 transactions")
}

func TestSeed_MissingBankAccountNonInteractive(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		assert.Fail(t, "must not call the server without a bank account")
	})
	err := run(NewCmdSandbox(f), "seed", "--count", "2")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestSimulate_BadOverride(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		assert.Fail(t, "must not reach the server with a malformed override")
	})
	err := run(NewCmdSandbox(f), "simulate", "payment_in",
		"--bank-account", "ba_1", "--amount", "1000", "--override", "noequals")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestInit_Full(t *testing.T) {
	var baCreated, vaCreated, whCreated bool
	var simCount int32
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/bank-accounts":
			baCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":"success","data":{"id":"ba_1"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/bank-accounts/ba_1/va":
			vaCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":"success","data":{"va":"SB1"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/webhooks":
			whCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_1"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/transactions/simulate":
			atomic.AddInt32(&simCount, 1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":"success","data":{"xid":"tx"}}`))
		default:
			assert.Failf(t, "unexpected request", "unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	require.NoError(t, run(NewCmdSandbox(f), "init", "--bank", "ACB", "--with-va", "--with-webhook", "https://x.test", "--seed", "2"))
	assert.True(t, baCreated && vaCreated && whCreated)
	assert.Equal(t, int32(2), atomic.LoadInt32(&simCount))
	assert.Contains(t, errOut.String(), "Created bank account ba_1")
	assert.Contains(t, errOut.String(), "Seeded 2 transactions")
}

func TestInit_Minimal(t *testing.T) {
	var simCount int32
	f, out, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/transactions/simulate" {
			atomic.AddInt32(&simCount, 1)
		}
		if r.URL.Path != "/bank-accounts" {
			assert.Failf(t, "unexpected request", "unexpected call %s (minimal init should only create a bank account)", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"ba_1"}}`))
	})
	require.NoError(t, run(NewCmdSandbox(f), "init"))
	assert.Equal(t, int32(0), atomic.LoadInt32(&simCount))
	assert.Contains(t, out.String(), "ba_1")
}

// init writes three resources in sequence, so every input must be checked before
// the first one: a URL only the webhook step refuses would otherwise leave a bank
// account, and its VA, provisioned with no way to finish.
func TestInit_ValidatesBeforeProvisioning(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "url with no host", args: []string{"--with-webhook", "test"}},
		{name: "url with no scheme", args: []string{"--with-webhook", "example.com/hook"}},
		{name: "non-http scheme", args: []string{"--with-webhook", "ftp://example.com/hook"}},
		{name: "both webhook kinds", args: []string{"--with-webhook", "https://x.test", "--with-webhook-listen"}},
		{name: "seed too large", args: []string{"--seed", "101"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _, _ := testFactory(t, func(_ http.ResponseWriter, r *http.Request) {
				assert.Failf(t, "unexpected request", "provisioned %s %s before the inputs were validated", r.Method, r.URL.Path)
			})
			err := run(NewCmdSandbox(f), append([]string{"init"}, tt.args...)...)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		})
	}
}

// A listen-only webhook carries delivery_mode and no url at all: the API refuses
// "listen" with a url present, even an empty one.
func TestInit_ListenOnlyWebhook(t *testing.T) {
	var body map[string]any
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/webhooks" {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":"success","data":{"id":"wh_1"}}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"ba_1"}}`))
	})
	require.NoError(t, run(NewCmdSandbox(f), "init", "--with-webhook-listen"))

	assert.Equal(t, "listen", body["delivery_mode"])
	assert.NotContains(t, body, "url")
	assert.Equal(t, "ba_1", body["bank_account_xid"])
	assert.Contains(t, errOut.String(), "Created listen-only webhook wh_1")
}

func TestInit_SeedFlagOutOfRange(t *testing.T) {
	for _, seed := range []string{"-1", "101"} {
		t.Run(seed, func(t *testing.T) {
			f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
				assert.Fail(t, "must not provision anything with an out-of-range --seed")
			})
			err := run(NewCmdSandbox(f), "init", "--seed", seed)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		})
	}
}

func TestParseSeedCount(t *testing.T) {
	tests := []struct {
		answer  string
		want    int
		wantErr bool
	}{
		{answer: "", want: 0},
		{answer: "  ", want: 0},
		{answer: "5", want: 5},
		{answer: " 100 ", want: 100},
		{answer: "101", wantErr: true},
		{answer: "-1", wantErr: true},
		{answer: "five", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.answer, func(t *testing.T) {
			got, err := parseSeedCount(tt.answer)
			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A step the server refuses leaves the earlier writes standing, so the run must
// report them: the xids are the only handle on resources nothing rolls back.
func TestInit_PartialResultWhenAStepFails(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bank-accounts":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":"success","data":{"id":"ba_1"}}`))
		case "/webhooks":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"status":"error","error_code":"validation_error","message":"loi","errors":{"url":"URL kh\u00f4ng h\u1ee3p l\u1ec7"}}`))
		default:
			assert.Failf(t, "unexpected request", "unexpected call %s %s", r.Method, r.URL.Path)
		}
	}

	t.Run("table", func(t *testing.T) {
		f, out, errOut := testFactory(t, handler)
		err := run(NewCmdSandbox(f), "init", "--with-webhook", "https://x.test")
		require.Error(t, err)
		assert.Contains(t, out.String(), "ba_1", "the created account must reach stdout")
		assert.Contains(t, errOut.String(), "stopped part-way")
	})

	t.Run("json", func(t *testing.T) {
		f, out, _ := testFactory(t, handler)
		f.Global.Output = "json"
		err := run(NewCmdSandbox(f), "init", "--with-webhook", "https://x.test")
		require.Error(t, err)

		var res initResult
		require.NoError(t, json.Unmarshal(out.Bytes(), &res))
		assert.Equal(t, "ba_1", res.BankAccount)
		assert.True(t, res.Partial)
		assert.Empty(t, res.Webhook)
	})
}

// Nothing created, nothing to report: the first step failing is a plain error.
func TestInit_NoResultWhenTheFirstWriteFails(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"status":"error","error_code":"validation_error","message":"loi"}`))
	})
	f.Global.Output = "json"
	err := run(NewCmdSandbox(f), "init")
	require.Error(t, err)
	assert.Empty(t, out.String())
}

// A complete run keeps its shape: no partial marker for a caller to branch on.
func TestInit_CompleteRunIsNotMarkedPartial(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"ba_1"}}`))
	})
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdSandbox(f), "init"))
	assert.NotContains(t, out.String(), "partial")
}

func TestReset_DeletesAll(t *testing.T) {
	var whList, baList int32
	var deletes []string
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/webhooks":
			if atomic.AddInt32(&whList, 1) == 1 {
				_, _ = w.Write([]byte(`{"status":"success","data":[{"id":"wh_1"},{"id":"wh_2"}],"meta":{"pagination":{"total":2}}}`))
			} else {
				_, _ = w.Write([]byte(`{"status":"success","data":[],"meta":{"pagination":{"total":0}}}`))
			}
		case r.Method == http.MethodGet && r.URL.Path == "/bank-accounts":
			if atomic.AddInt32(&baList, 1) == 1 {
				_, _ = w.Write([]byte(`{"status":"success","data":[{"id":"ba_1"}],"meta":{"pagination":{"total":1}}}`))
			} else {
				_, _ = w.Write([]byte(`{"status":"success","data":[],"meta":{"pagination":{"total":0}}}`))
			}
		case r.Method == http.MethodDelete:
			deletes = append(deletes, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			assert.Failf(t, "unexpected request", "unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	f.Global.Yes = true
	require.NoError(t, run(NewCmdSandbox(f), "reset"))
	assert.ElementsMatch(t, []string{"/webhooks/wh_1", "/webhooks/wh_2", "/bank-accounts/ba_1"}, deletes)
	assert.Contains(t, errOut.String(), "Deleted 2 webhooks, 1 bank accounts")
}

func TestReset_NeedsConfirmNonInteractive(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		assert.Fail(t, "must not delete anything without confirmation")
	})
	err := run(NewCmdSandbox(f), "reset")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestReset_RejectsProductionEnv(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		assert.Fail(t, "must not touch the server when env is production")
	})
	f.Global.Env = "production"
	f.Global.Yes = true
	err := run(NewCmdSandbox(f), "reset")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "drop --env production")
}

func TestSimulate_RejectsProductionEnv(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		assert.Fail(t, "must not simulate against production")
	})
	f.Global.Env = "production"
	err := run(NewCmdSandbox(f), "simulate", "payment_in", "--bank-account", "ba_1", "--amount", "1000")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestReset_SurfacesListError(t *testing.T) {
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":"error","error_code":"unauthorized","message":"nope"}`))
			return
		}
		assert.Fail(t, "must not delete when listing failed")
	})
	f.Global.Yes = true
	err := run(NewCmdSandbox(f), "reset")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitAuth, cmdutil.ExitCodeForError(err)) // 401 -> exit 4, not a silent "Deleted 0"
}

func TestReset_NoProgressReturnsError(t *testing.T) {
	var deleteCalls int32
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"status":"success","data":[{"id":"wh_1"}],"meta":{"pagination":{"total":1}}}`))
			return
		}
		atomic.AddInt32(&deleteCalls, 1) // DELETE always 500
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":"error","error_code":"server_error"}`))
	})
	f.Global.Yes = true
	err := run(NewCmdSandbox(f), "reset")
	require.Error(t, err) // must not loop forever, must surface
	assert.LessOrEqual(t, atomic.LoadInt32(&deleteCalls), int32(2))
}

func TestReset_ToleratesNotFound(t *testing.T) {
	var whList int32
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/webhooks":
			if atomic.AddInt32(&whList, 1) == 1 {
				_, _ = w.Write([]byte(`{"status":"success","data":[{"id":"wh_1"}],"meta":{"pagination":{"total":1}}}`))
			} else {
				_, _ = w.Write([]byte(`{"status":"success","data":[],"meta":{"pagination":{"total":0}}}`))
			}
		case r.Method == http.MethodGet && r.URL.Path == "/bank-accounts":
			_, _ = w.Write([]byte(`{"status":"success","data":[],"meta":{"pagination":{"total":0}}}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNotFound) // already gone
			_, _ = w.Write([]byte(`{"status":"error","error_code":"not_found"}`))
		}
	})
	f.Global.Yes = true
	require.NoError(t, run(NewCmdSandbox(f), "reset")) // 404 tolerated -> no error, loop advances
	assert.Contains(t, errOut.String(), "Deleted 0 webhooks, 0 bank accounts")
}

func TestSeed_DefaultCount(t *testing.T) {
	var count int32
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/transactions/simulate" {
			atomic.AddInt32(&count, 1)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"xid":"tx"}}`))
	})
	require.NoError(t, run(NewCmdSandbox(f), "seed", "--bank-account", "ba_1"))
	assert.Equal(t, int32(5), atomic.LoadInt32(&count)) // defaultSeedCount
}

func TestSeed_SimulateSendsIdempotencyKey(t *testing.T) {
	var keys []string
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("X-Idempotency-Key"))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"success","data":{"xid":"tx"}}`))
	})
	require.NoError(t, run(NewCmdSandbox(f), "seed", "--bank-account", "ba_1", "--count", "2"))
	require.Len(t, keys, 2)
	for _, k := range keys {
		assert.NotEmpty(t, k, "each seed simulate must carry an idempotency key")
	}
	assert.NotEqual(t, keys[0], keys[1], "each seeded txn is distinct -> distinct keys")
}

func TestInit_JSON(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bank-accounts":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":"success","data":{"id":"ba_1"}}`))
		case "/transactions/simulate":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":"success","data":{"xid":"tx"}}`))
		}
	})
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdSandbox(f), "init", "--seed", "1"))
	s := out.String()
	assert.Contains(t, s, `"bank_account": "ba_1"`)
	assert.Contains(t, s, `"seeded": 1`)
}

// The event can arrive as the positional arg or as --event, so both spellings are
// checked against the fixed set.
func TestSimulate_EventValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"bogus positional", []string{"simulate", "payment", "--bank-account", "ba_1", "--amount", "1000"}},
		{"bogus flag", []string{"simulate", "--event", "PAYMENT_IN", "--bank-account", "ba_1", "--amount", "1000"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
				assert.Fail(t, "must not call the server on an invalid event")
			})

			err := run(NewCmdSandbox(f), tt.args...)

			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), "payment_in, payment_out, va_credit")
		})
	}
}

// No event at all is the "required" usage error, not a domain complaint about "".
func TestSimulate_EmptyEventIsUnset(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		assert.Fail(t, "must not call the server without an event")
	})
	err := run(NewCmdSandbox(f), "simulate", "--event", "", "--bank-account", "ba_1", "--amount", "1000")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "--event is required")
}

func TestSimulate_CodeLength(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		assert.Fail(t, "must not call the server with an over-long code")
	})
	err := run(NewCmdSandbox(f), "simulate", "payment_in",
		"--bank-account", "ba_1", "--amount", "1000", "--code", "ABCDEFGHIJK") // 11
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "at most 10 characters")
}

func TestSimulate_CodeAtLimitAndEmpty(t *testing.T) {
	for _, code := range []string{"ABCDEFGHIJ", ""} {
		var body map[string]any
		f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":"success","data":{"xid":"tx_new"}}`))
		})
		require.NoError(t, run(NewCmdSandbox(f), "simulate", "payment_in",
			"--bank-account", "ba_1", "--amount", "1000", "--code", code))
		if code == "" {
			_, sent := body["code"]
			assert.False(t, sent, "an empty code stays unset")
			continue
		}
		assert.Equal(t, code, body["code"])
	}
}

// A negative count must not fold into the "omitted" branch and seed the default 5:
// a typo would write data and exit 0. --seed is the same concept on the twin path,
// so both answer with the same message.
func TestSeed_CountBoundsMatchSeedFlag(t *testing.T) {
	cases := []struct {
		name, count, want string
	}{
		{"negative", "-1", "--count must be 0 or more"},
		{"over the cap", "101", "--count must be 100 or less"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
				assert.Fail(t, "must not write a transaction for a count out of bounds")
			})
			err := run(NewCmdSandbox(f), "seed", "--bank-account", "ba_1", "--count", tc.count)
			require.Error(t, err)
			require.EqualError(t, err, tc.want)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		})
	}
}

// The bound is checked before the bank-account picker, so a count the run will
// refuse never asks the user to pick an account first.
func TestSeed_BadCountIsRefusedBeforeThePicker(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		assert.Fail(t, "must not fetch bank accounts for a count out of bounds")
	})
	err := run(NewCmdSandbox(f), "seed", "--count", "-1")
	require.Error(t, err)
	assert.EqualError(t, err, "--count must be 0 or more")
}

func TestSeed_AcceptedCounts(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want int32
	}{
		{"omitted takes the documented default", nil, 5},
		{"explicit zero takes the same default", []string{"--count", "0"}, 5},
		{"one", []string{"--count", "1"}, 1},
		{"an ordinary count", []string{"--count", "7"}, 7},
		{"the cap itself", []string{"--count", "100"}, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var count int32
			f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/transactions/simulate" {
					atomic.AddInt32(&count, 1)
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"status":"success","data":{"xid":"tx"}}`))
			})
			argv := append([]string{"seed", "--bank-account", "ba_1"}, tc.argv...)
			require.NoError(t, run(NewCmdSandbox(f), argv...))
			assert.Equal(t, tc.want, atomic.LoadInt32(&count))
		})
	}
}

func TestInit_ChecksTheBankCode(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("must not provision with an unknown bank: %s %s", r.Method, r.URL.Path)
	})
	err := run(NewCmdSandbox(f), "init", "--bank", "XYZ", "--seed", "0")
	require.EqualError(t, err, `--bank "XYZ" is not a supported bank code (have: ACB, BIDV)`)
}

func TestInit_DefaultBankComesFromTheServerList(t *testing.T) {
	cases := []struct {
		name, banks, want string
	}{
		{"ACB offered", banksJSON, "ACB"},
		{"ACB listed under another code", `{"status":"success","data":[{"code":"BIDV"},{"code":"ACBE2E","short_name":"ACB"}]}`, "ACBE2E"},
		{"ACB not offered", `{"status":"success","data":[{"code":"VCB"},{"code":"BIDV"}]}`, "VCB"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var bank any
			f, _, _ := bankListFactory(t, c.banks, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/bank-accounts":
					var body map[string]any
					b, _ := io.ReadAll(r.Body)
					_ = json.Unmarshal(b, &body)
					bank = body["bank"]
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"status":"success","data":{"id":"ba_1"}}`))
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
			})
			f.Global.Yes = true
			require.NoError(t, run(NewCmdSandbox(f), "init"))
			assert.Equal(t, c.want, bank)
		})
	}
}
