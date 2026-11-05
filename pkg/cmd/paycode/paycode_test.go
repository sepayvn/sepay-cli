package paycode

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil/prompttest"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testFactory(t *testing.T, h http.HandlerFunc) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(h)
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

// capturedWrite records the write a command sends.
type capturedWrite struct {
	method      string
	path        string
	idempotency string
	ifMatch     string
	body        map[string]any
}

// setServer serves GET with current as the data payload and captures the write.
func setServer(t *testing.T, current string) (*cmdutil.Factory, *capturedWrite) {
	t.Helper()
	captured := &capturedWrite{}
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"status":"success","data":` + current + `}`))
			return
		}
		captured.method, captured.path = r.Method, r.URL.Path
		captured.idempotency = r.Header.Get("X-Idempotency-Key")
		captured.ifMatch = r.Header.Get("If-Match")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured.body)
		_, _ = w.Write([]byte(`{"status":"success","data":` + current + `}`))
	})
	return f, captured
}

func firstStructure(t *testing.T, c *capturedWrite) map[string]any {
	t.Helper()
	structs, ok := c.body["PayCodeStructures"].([]any)
	require.True(t, ok, "PayCodeStructures must be an array")
	require.NotEmpty(t, structs)
	return structs[0].(map[string]any)
}

func TestList_Table(t *testing.T) {
	f, out, errOut := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"}]}}`))
	})
	require.NoError(t, run(NewCmdPaycode(f), "list"))
	assert.Contains(t, errOut.String(), "Pay code: on")
	assert.Contains(t, out.String(), "DH")
	assert.Contains(t, out.String(), "NumberAndLetter")
}

func TestList_JSON(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"PayCode":"off","PayCodeStructures":[]}}`))
	})
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdPaycode(f), "list"))
	assert.Contains(t, out.String(), `"PayCode": "off"`)
	assert.Contains(t, out.String(), `"PayCodeStructures": []`)
}

func TestUpdate_MergesActivePrefixPreservingRest(t *testing.T) {
	current := `{"PayCode":"on","PayCodeStructures":[` +
		`{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"},` +
		`{"is_active":"off","prefix":"XX","suffix_from":2,"suffix_to":8,"character_type":"NumberOnly"}]}`
	f, captured := setServer(t, current)

	require.NoError(t, run(NewCmdPaycode(f), "update", "SP"))

	assert.Equal(t, http.MethodPut, captured.method)
	assert.Equal(t, "/company/paycode", captured.path)
	assert.Equal(t, "on", captured.body["PayCode"], "PayCode toggle preserved")

	structs := captured.body["PayCodeStructures"].([]any)
	require.Len(t, structs, 2)
	active := structs[0].(map[string]any)
	assert.Equal(t, "SP", active["prefix"], "active prefix updated")
	assert.EqualValues(t, 1, active["suffix_from"], "suffix preserved")
	assert.Equal(t, "NumberAndLetter", active["character_type"], "type preserved")
	other := structs[1].(map[string]any)
	assert.Equal(t, "XX", other["prefix"], "inactive structure untouched")
}

func TestUpdate_SuffixAndTypeFlagsOverride(t *testing.T) {
	f, captured := setServer(t, `{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"}]}`)

	require.NoError(t, run(NewCmdPaycode(f), "update", "SP", "--suffix-from", "5", "--suffix-to", "10", "--character-type", "NumberOnly"))

	s := firstStructure(t, captured)
	assert.EqualValues(t, 5, s["suffix_from"])
	assert.EqualValues(t, 10, s["suffix_to"])
	assert.Equal(t, "NumberOnly", s["character_type"])
}

func TestUpdate_DisableTogglesPayCodeOff(t *testing.T) {
	f, captured := setServer(t, `{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"}]}`)

	require.NoError(t, run(NewCmdPaycode(f), "update", "SP", "--disable"))

	assert.Equal(t, "off", captured.body["PayCode"])
}

func TestUpdate_FlagsAloneKeepTheStoredPrefix(t *testing.T) {
	current := `{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"}]}`
	cases := map[string]struct {
		args  []string
		check func(t *testing.T, c *capturedWrite)
	}{
		"disable": {[]string{"--disable"}, func(t *testing.T, c *capturedWrite) {
			assert.Equal(t, "off", c.body["PayCode"])
		}},
		"enable": {[]string{"--enable"}, func(t *testing.T, c *capturedWrite) {
			assert.Equal(t, "on", c.body["PayCode"])
		}},
		"suffix range": {[]string{"--suffix-from", "4", "--suffix-to", "8"}, func(t *testing.T, c *capturedWrite) {
			s := firstStructure(t, c)
			assert.EqualValues(t, 4, s["suffix_from"])
			assert.EqualValues(t, 8, s["suffix_to"])
		}},
		"active off": {[]string{"--active=false"}, func(t *testing.T, c *capturedWrite) {
			assert.Equal(t, "off", firstStructure(t, c)["is_active"])
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f, captured := setServer(t, current)

			require.NoError(t, run(NewCmdPaycode(f), append([]string{"update"}, tc.args...)...))

			assert.Equal(t, http.MethodPut, captured.method)
			assert.Equal(t, "DH", firstStructure(t, captured)["prefix"], "stored prefix kept")
			tc.check(t, captured)
		})
	}
}

func TestUpdate_FlagsAloneNeedAPrefixWhenNothingIsStored(t *testing.T) {
	f, captured := setServer(t, `{"PayCode":"on","PayCodeStructures":[]}`)

	err := run(NewCmdPaycode(f), "update", "--disable")

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "a pay code prefix is required")
	assert.NotEqual(t, http.MethodPut, captured.method, "nothing is written")
}

func TestUpdate_EmptyCreatesDefaultStructure(t *testing.T) {
	f, captured := setServer(t, `{"PayCode":"on","PayCodeStructures":[]}`)

	require.NoError(t, run(NewCmdPaycode(f), "update", "SP"))

	s := firstStructure(t, captured)
	assert.Equal(t, "SP", s["prefix"])
	assert.EqualValues(t, 3, s["suffix_from"])
	assert.EqualValues(t, 10, s["suffix_to"])
	assert.Equal(t, "NumberOnly", s["character_type"])
	assert.Equal(t, "on", s["is_active"])
}

func TestUpdate_NoIdempotencyKey(t *testing.T) {
	f, captured := setServer(t, `{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"}]}`)

	require.NoError(t, run(NewCmdPaycode(f), "update", "SP"))

	assert.Empty(t, captured.idempotency, "set sends no key; If-Match guards the write")
}

// A PUT that may have landed is not resent: the repeat's If-Match would be stale and answer 409
// "nothing was saved" for a write that was saved.
func TestUpdate_ACutConnectionIsNotResent(t *testing.T) {
	const current = `{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":3,"suffix_to":10,"character_type":"NumberOnly"}],"version":"v1"}`
	// Atomic: the client sees the cut connection before the aborted handler is done with it.
	var puts atomic.Int32
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"status":"success","data":` + current + `}`))
			return
		}
		puts.Add(1)
		panic(http.ErrAbortHandler)
	})
	f.NewAPIClient = func(next func() (*api.Client, error)) func() (*api.Client, error) {
		return func() (*api.Client, error) {
			c, err := next()
			if c != nil {
				c.MaxRetries = 2
			}
			return c, err
		}
	}(f.NewAPIClient)

	err := run(NewCmdPaycode(f), "update", "HD")
	var transportErr *api.TransportError
	require.ErrorAs(t, err, &transportErr, "the outcome is unknown, so the error says the connection broke, not that nothing was saved")
	assert.Equal(t, int32(1), puts.Load())
}

func TestUpdate_EnableDisableConflict(t *testing.T) {
	f, _ := setServer(t, `{"PayCode":"on","PayCodeStructures":[]}`)

	err := run(NewCmdPaycode(f), "update", "SP", "--enable", "--disable")

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestUpdate_RejectsPrefixAndSuffixOutOfRange(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"digits in prefix", []string{"update", "QAP11"}, "2-5 letters"},
		{"too short", []string{"update", "A"}, "2-5 letters"},
		{"too long", []string{"update", "ABCDEF"}, "2-5 letters"},
		{"suffix-from below range", []string{"update", "QAPA", "--suffix-from", "0"}, "--suffix-from must be between 1 and 30"},
		{"suffix-to above range", []string{"update", "QAPA", "--suffix-to", "31"}, "--suffix-to must be between 1 and 30"},
		{"inverted range", []string{"update", "QAPA", "--suffix-from", "10", "--suffix-to", "5"}, "less than or equal"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("must not call the server on an invalid structure")
			})

			err := run(NewCmdPaycode(f), tt.args...)

			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestUpdate_AcceptsBoundaryValues(t *testing.T) {
	f, captured := setServer(t, `{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberOnly"}]}`)

	require.NoError(t, run(NewCmdPaycode(f), "update", "ABCDE", "--suffix-from", "1", "--suffix-to", "30"))

	s := firstStructure(t, captured)
	assert.Equal(t, "ABCDE", s["prefix"])
	assert.EqualValues(t, 1, s["suffix_from"])
	assert.EqualValues(t, 30, s["suffix_to"])
}

func TestUpdate_MissingPrefixIsUsageErrorWhenNotInteractive(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server without a prefix")
	})

	err := run(NewCmdPaycode(f), "update")

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "a pay code prefix is required")
}

func TestUpdate_FlagsCheckedBeforeThePrompt(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server on an invalid flag combination")
	})

	err := run(NewCmdPaycode(f), "update", "--enable", "--disable")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--enable and --disable")
}

// A bool flag never consumes the next token, so a switch word would land as the prefix.
func TestUpdate_SwitchWordIsNotAPrefix(t *testing.T) {
	for _, word := range []string{"on", "off", "yes", "no", "true", "false"} {
		t.Run(word, func(t *testing.T) {
			f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("must not call the server with a switch word as the prefix")
			})

			err := run(NewCmdPaycode(f), "update", "--enable", word)

			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), "--enable is a switch, not a value")
		})
	}
}

func TestUpdate_SendsBackTheVersionItReadAsIfMatch(t *testing.T) {
	current := `{"PayCode":"on","version":"v1abc123",` +
		`"PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"}]}`
	f, captured := setServer(t, current)

	require.NoError(t, run(NewCmdPaycode(f), "update", "SP"))

	assert.Equal(t, "v1abc123", captured.ifMatch)
	_, inBody := captured.body["version"]
	assert.False(t, inBody, "version must not travel in the body")
}

func TestUpdate_OmitsVersionWhenServerSendsNone(t *testing.T) {
	f, captured := setServer(t, `{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"}]}`)

	require.NoError(t, run(NewCmdPaycode(f), "update", "SP"))

	assert.Empty(t, captured.ifMatch, "no If-Match when the server sent no version")
	_, inBody := captured.body["version"]
	assert.False(t, inBody)
}

func TestUpdate_RefusesLocallyWhenAStructureHasNoOnOffState(t *testing.T) {
	f, captured := setServer(t, `{"PayCode":"on","PayCodeStructures":[{"prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"}]}`)

	err := run(NewCmdPaycode(f), "update", "SP")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no on/off state")
	assert.Empty(t, captured.method, "nothing may be written")
}

func TestUpdate_WarnsWhenNoStructureIsActive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := `{"PayCode":"on","PayCodeStructures":[{"is_active":"off","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberOnly"}]}`
		_, _ = w.Write([]byte(`{"status":"success","data":` + body + `}`))
	}))
	t.Cleanup(srv.Close)

	ios, _, _, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	f.NewAPIClient = func() (*api.Client, error) {
		c := api.NewClient(srv.URL, "test")
		c.MaxRetries = 0
		return c, nil
	}

	require.NoError(t, run(NewCmdPaycode(f), "update", "SP"))
	assert.Contains(t, errOut.String(), "no pay code structure is active")
}

func TestUpdate_DoesNotWarnWhenActiveWasSetExplicitly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := `{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberOnly"}]}`
		_, _ = w.Write([]byte(`{"status":"success","data":` + body + `}`))
	}))
	t.Cleanup(srv.Close)

	ios, _, _, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	f.NewAPIClient = func() (*api.Client, error) {
		c := api.NewClient(srv.URL, "test")
		c.MaxRetries = 0
		return c, nil
	}

	require.NoError(t, run(NewCmdPaycode(f), "update", "SP", "--active=false"))
	assert.NotContains(t, errOut.String(), "no pay code structure is active")
}

func TestUpdate_ActiveIsABooleanMappedToOnOff(t *testing.T) {
	cases := []struct{ flag, want string }{
		{flag: "--active", want: `"is_active":"on"`},
		{flag: "--active=false", want: `"is_active":"off"`},
	}
	for _, tc := range cases {
		t.Run(tc.flag, func(t *testing.T) {
			var sent string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					b, _ := io.ReadAll(r.Body)
					sent = string(b)
				}
				body := `{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberOnly"}]}`
				_, _ = w.Write([]byte(`{"status":"success","data":` + body + `}`))
			}))
			t.Cleanup(srv.Close)

			ios, _, _, _ := iostreams.Test()
			f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
			f.NewAPIClient = func() (*api.Client, error) {
				c := api.NewClient(srv.URL, "test")
				c.MaxRetries = 0
				return c, nil
			}

			require.NoError(t, run(NewCmdPaycode(f), "update", "SP", tc.flag))
			assert.Contains(t, strings.ReplaceAll(sent, " ", ""), tc.want)
		})
	}
}

func TestUpdate_PrefixFlagPassesASwitchWord(t *testing.T) {
	for _, word := range []string{"on", "off", "yes", "no", "true", "false"} {
		t.Run(word, func(t *testing.T) {
			f, captured := setServer(t, `{"PayCode":"off","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"}]}`)

			require.NoError(t, run(NewCmdPaycode(f), "update", "--prefix", word, "--enable"))

			structs := captured.body["PayCodeStructures"].([]any)
			require.Len(t, structs, 1)
			assert.Equal(t, word, structs[0].(map[string]any)["prefix"])
			assert.Equal(t, "on", captured.body["PayCode"], "--enable still applies")
		})
	}
}

func TestUpdate_SwitchWordErrorNamesThePrefixFlag(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server")
	})

	err := run(NewCmdPaycode(f), "update", "--enable", "on")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "pass --prefix on")
}

func TestUpdate_PrefixGivenTwiceIsRefused(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server")
	})

	err := run(NewCmdPaycode(f), "update", "ERP", "--prefix", "ERP")

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "give the pay code prefix once")
}

// The first-active fallback skips an inactive structure, so only --structure reaches it.
func TestUpdate_StructureTargetsAnInactiveStructure(t *testing.T) {
	current := `{"PayCode":"on","PayCodeStructures":[` +
		`{"is_active":"off","prefix":"HEHE","suffix_from":6,"suffix_to":8,"character_type":"NumberOnly"},` +
		`{"is_active":"on","prefix":"HAHA","suffix_from":16,"suffix_to":17,"character_type":"NumberOnly"}]}`
	f, captured := setServer(t, current)

	require.NoError(t, run(NewCmdPaycode(f), "update", "--structure", "hehe", "--active"))

	structs := captured.body["PayCodeStructures"].([]any)
	require.Len(t, structs, 2)
	assert.Equal(t, "on", structs[0].(map[string]any)["is_active"])
	assert.Equal(t, "HEHE", structs[0].(map[string]any)["prefix"])
	assert.Equal(t, "HAHA", structs[1].(map[string]any)["prefix"], "other structure untouched")
}

func TestUpdate_StructureRenamesTheNamedStructure(t *testing.T) {
	current := `{"PayCode":"on","PayCodeStructures":[` +
		`{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberOnly"},` +
		`{"is_active":"on","prefix":"XX","suffix_from":2,"suffix_to":8,"character_type":"NumberOnly"}]}`
	f, captured := setServer(t, current)

	require.NoError(t, run(NewCmdPaycode(f), "update", "SP", "--structure", "XX"))

	structs := captured.body["PayCodeStructures"].([]any)
	assert.Equal(t, "DH", structs[0].(map[string]any)["prefix"])
	assert.Equal(t, "SP", structs[1].(map[string]any)["prefix"])
}

func TestUpdate_UnknownStructureIsUsageError(t *testing.T) {
	var posted bool
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			posted = true
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberOnly"}]}}`))
	})

	err := run(NewCmdPaycode(f), "update", "SP", "--structure", "ZZ")

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), `no pay code structure with prefix "ZZ" (have: DH)`)
	assert.False(t, posted)
}

func TestUpdate_PrintsThePayCodeToggle(t *testing.T) {
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"PayCode":"off","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberOnly"}]}}`))
	})

	require.NoError(t, run(NewCmdPaycode(f), "update", "--disable"))
	assert.Contains(t, errOut.String(), "Pay code: off")
}

func TestUpdate_StructurePrefersAnExactMatch(t *testing.T) {
	current := `{"PayCode":"on","PayCodeStructures":[` +
		`{"is_active":"on","prefix":"dh","suffix_from":1,"suffix_to":30,"character_type":"NumberOnly"},` +
		`{"is_active":"on","prefix":"DH","suffix_from":2,"suffix_to":8,"character_type":"NumberOnly"}]}`
	f, captured := setServer(t, current)

	require.NoError(t, run(NewCmdPaycode(f), "update", "SP", "--structure", "DH"))

	structs := captured.body["PayCodeStructures"].([]any)
	assert.Equal(t, "dh", structs[0].(map[string]any)["prefix"])
	assert.Equal(t, "SP", structs[1].(map[string]any)["prefix"])
}

func TestUpdate_WarnsWhenEcomConnectionsKeepTheOldPrefix(t *testing.T) {
	const current = `{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":3,"suffix_to":10,"character_type":"NumberOnly"}],"version":"v1"}`
	for _, tc := range []struct {
		notSynced string
		warns     bool
	}{{"2", true}, {"0", false}} {
		f, _, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"status":"success","data":` + current + `}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"success","data":` + strings.TrimSuffix(current, "}") + `,"ecom_rows_not_synced":` + tc.notSynced + `}}`))
		})
		require.NoError(t, run(NewCmdPaycode(f), "update", "HD"))
		if tc.warns {
			assert.Contains(t, errOut.String(), "Warning: 2 Haravan/Shopify/Sapo connection(s) still use a pay code prefix")
		} else {
			assert.NotContains(t, errOut.String(), "Warning:")
		}
	}
}

// createdData is a 201 for one structure appended to an existing one.
const createdData = `{"status":"success","data":{"structure":{"is_active":"on","prefix":"HD","suffix_from":6,"suffix_to":12,"character_type":"NumberAndLetter"},` +
	`"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":3,"suffix_to":10,"character_type":"NumberOnly"},` +
	`{"is_active":"on","prefix":"HD","suffix_from":6,"suffix_to":12,"character_type":"NumberAndLetter"}],"version":"v2"}}`

// createServer answers every request with status and body, capturing the last write.
func createServer(t *testing.T, status int, body string) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer, *capturedWrite) {
	t.Helper()
	captured := &capturedWrite{}
	f, out, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		captured.method, captured.path = r.Method, r.URL.Path
		captured.idempotency = r.Header.Get("X-Idempotency-Key")
		captured.ifMatch = r.Header.Get("If-Match")
		b, _ := io.ReadAll(r.Body)
		captured.body = nil
		_ = json.Unmarshal(b, &captured.body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	return f, out, errOut, captured
}

var createFlags = []string{"--suffix-from", "6", "--suffix-to", "12", "--character-type", "NumberAndLetter", "--active"}

func TestCreate_PostsOneFlatStructureWithAnIdempotencyKey(t *testing.T) {
	f, out, errOut, captured := createServer(t, http.StatusCreated, createdData)

	require.NoError(t, run(NewCmdPaycode(f), append([]string{"create", "HD"}, createFlags...)...))

	assert.Equal(t, http.MethodPost, captured.method)
	assert.Equal(t, "/company/paycode", captured.path)
	assert.NotEmpty(t, captured.idempotency, "the server refuses a create without X-Idempotency-Key")
	assert.Empty(t, captured.ifMatch, "create takes no version")
	assert.Equal(t, map[string]any{
		"is_active": "on", "prefix": "HD", "suffix_from": float64(6), "suffix_to": float64(12), "character_type": "NumberAndLetter",
	}, captured.body, "exactly the five structure fields: the server answers 422 to any other")
	assert.Contains(t, errOut.String(), "Created pay code structure HD.")
	assert.Contains(t, out.String(), "HD")
	assert.Contains(t, out.String(), "DH", "the whole list is shown")
}

func TestCreate_ActiveFalseSendsOff(t *testing.T) {
	f, _, _, captured := createServer(t, http.StatusCreated, createdData)
	require.NoError(t, run(NewCmdPaycode(f), "create", "HD", "--suffix-from", "6", "--suffix-to", "12", "--character-type", "NumberOnly", "--active=false"))
	assert.Equal(t, "off", captured.body["is_active"])
}

func TestCreate_JSONIsTheServerData(t *testing.T) {
	f, out, _, _ := createServer(t, http.StatusCreated, createdData)
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdPaycode(f), append([]string{"create", "HD"}, createFlags...)...))

	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, "HD", got["structure"].(map[string]any)["prefix"])
	assert.Len(t, got["PayCodeStructures"], 2)
	assert.Equal(t, "on", got["PayCode"])
	assert.Equal(t, "v2", got["version"])
}

func TestCreate_RetryReusesTheIdempotencyKey(t *testing.T) {
	calls := 0
	var keys []string
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("X-Idempotency-Key"))
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"status":"error","error_code":"rate_limited","message":"slow down"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(createdData))
	})
	f.NewAPIClient = func(next func() (*api.Client, error)) func() (*api.Client, error) {
		return func() (*api.Client, error) {
			c, err := next()
			if c != nil {
				c.MaxRetries = 1
			}
			return c, err
		}
	}(f.NewAPIClient)

	require.NoError(t, run(NewCmdPaycode(f), append([]string{"create", "HD"}, createFlags...)...))
	require.Len(t, keys, 2)
	assert.NotEmpty(t, keys[0])
	assert.Equal(t, keys[0], keys[1], "a retried create must not look like a second one")
}

func TestCreate_RefusesBeforeSending(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no suffix-from", []string{"create", "HD", "--suffix-to", "12", "--character-type", "NumberOnly", "--active"}, "--suffix-from is required"},
		{"no suffix-to", []string{"create", "HD", "--suffix-from", "6", "--character-type", "NumberOnly", "--active"}, "--suffix-to is required"},
		{"no character-type", []string{"create", "HD", "--suffix-from", "6", "--suffix-to", "12", "--active"}, "--character-type is required"},
		{"no active", []string{"create", "HD", "--suffix-from", "6", "--suffix-to", "12", "--character-type", "NumberOnly"}, "--active is required"},
		{"no prefix without a terminal", append([]string{"create"}, createFlags...), "a pay code prefix is required"},
		{"prefix with a digit", append([]string{"create", "H1"}, createFlags...), "prefix must be 2-5 letters"},
		{"prefix too long", append([]string{"create", "ABCDEF"}, createFlags...), "prefix must be 2-5 letters"},
		{"prefix twice", append([]string{"create", "HD", "--prefix", "DH"}, createFlags...), "give the pay code prefix once"},
		{"unknown character type", []string{"create", "HD", "--suffix-from", "6", "--suffix-to", "12", "--character-type", "Letters", "--active"}, "--character-type must be NumberOnly or NumberAndLetter"},
		{"suffix out of range", []string{"create", "HD", "--suffix-from", "0", "--suffix-to", "12", "--character-type", "NumberOnly", "--active"}, "--suffix-from must be between 1 and 30"},
		{"suffix range reversed", []string{"create", "HD", "--suffix-from", "12", "--suffix-to", "6", "--character-type", "NumberOnly", "--active"}, "--suffix-from must be less than or equal to --suffix-to"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _, _, captured := createServer(t, http.StatusCreated, createdData)
			err := run(NewCmdPaycode(f), tc.args...)
			var flagErr *cmdutil.FlagError
			require.ErrorAs(t, err, &flagErr, "a usage error exits 2")
			assert.Contains(t, err.Error(), tc.want)
			assert.Empty(t, captured.method, "nothing is sent")
		})
	}
}

func TestCreate_PrefixFlagPassesASwitchWord(t *testing.T) {
	f, _, _, captured := createServer(t, http.StatusCreated, createdData)
	require.NoError(t, run(NewCmdPaycode(f), append([]string{"create", "--prefix", "on"}, createFlags...)...))
	assert.Equal(t, "on", captured.body["prefix"])
}

func TestCreate_ConflictsKeepTheirCode(t *testing.T) {
	for _, code := range []string{"paycode_prefix_exists", "paycode_version_conflict"} {
		t.Run(code, func(t *testing.T) {
			f, _, _, _ := createServer(t, http.StatusConflict, `{"status":"error","error_code":"`+code+`","message":"Tiếng Việt từ server"}`)
			err := run(NewCmdPaycode(f), append([]string{"create", "HD"}, createFlags...)...)
			var apiErr *api.HTTPError
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, code, apiErr.Code)
			assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
		})
	}
}

func TestCreate_TerminalAsksEveryMissingField(t *testing.T) {
	f, _, _, captured := createServer(t, http.StatusCreated, createdData)
	prompts := prompttest.Use(t, f,
		prompttest.Step{Label: "Pay code prefix (2-5 letters)", Answer: "HD"},
		prompttest.Step{Label: "Character type", Answer: "NumberAndLetter"},
		prompttest.Step{Label: "Suffix length from (1-30)", Answer: "6"},
		prompttest.Step{Label: "Suffix length to (6-30)", Answer: "12"},
		prompttest.Step{Label: "Active", Answer: true},
	)

	require.NoError(t, run(NewCmdPaycode(f), "create"))

	assert.Equal(t, map[string]any{
		"is_active": "on", "prefix": "HD", "suffix_from": float64(6), "suffix_to": float64(12), "character_type": "NumberAndLetter",
	}, captured.body)
	assert.NotEmpty(t, captured.idempotency)
	assert.Equal(t, true, prompts.Defaults["Active"], "a new structure defaults to on")
}

func TestCreate_TerminalAsksOnlyWhatIsMissing(t *testing.T) {
	f, _, _, captured := createServer(t, http.StatusCreated, createdData)
	prompttest.Use(t, f,
		prompttest.Step{Label: "Suffix length to (4-30)", Answer: "9"},
		prompttest.Step{Label: "Active", Answer: false},
	)

	require.NoError(t, run(NewCmdPaycode(f), "create", "HD", "--character-type", "NumberOnly", "--suffix-from", "4"))

	assert.Equal(t, float64(4), captured.body["suffix_from"])
	assert.Equal(t, float64(9), captured.body["suffix_to"])
	assert.Equal(t, "off", captured.body["is_active"])
}

func TestCreate_TerminalWithEveryFlagAsksNothing(t *testing.T) {
	f, _, _, captured := createServer(t, http.StatusCreated, createdData)
	prompttest.Use(t, f)
	require.NoError(t, run(NewCmdPaycode(f), append([]string{"create", "HD"}, createFlags...)...))
	assert.Equal(t, "HD", captured.body["prefix"])
}

func TestCreate_TerminalRejectsABadAnswer(t *testing.T) {
	cases := []struct {
		name  string
		steps []prompttest.Step
		want  string
	}{
		{"prefix", []prompttest.Step{{Label: "Pay code prefix (2-5 letters)", Answer: "H1"}}, `prefix must be 2-5 letters, got "H1"`},
		{"suffix not a number", []prompttest.Step{
			{Label: "Pay code prefix (2-5 letters)", Answer: "HD"},
			{Label: "Character type", Answer: "NumberOnly"},
			{Label: "Suffix length from (1-30)", Answer: "six"},
		}, "--suffix-from must be between 1 and 30"},
		{"suffix out of range", []prompttest.Step{
			{Label: "Pay code prefix (2-5 letters)", Answer: "HD"},
			{Label: "Character type", Answer: "NumberOnly"},
			{Label: "Suffix length from (1-30)", Answer: "4"},
			{Label: "Suffix length to (4-30)", Answer: "31"},
		}, "--suffix-to must be between 4 and 30"},
		{"from above to", []prompttest.Step{
			{Label: "Pay code prefix (2-5 letters)", Answer: "HD"},
			{Label: "Character type", Answer: "NumberOnly"},
			{Label: "Suffix length from (1-30)", Answer: "9"},
			{Label: "Suffix length to (9-30)", Answer: "4"},
		}, "--suffix-to must be between 9 and 30"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _, _, captured := createServer(t, http.StatusCreated, createdData)
			prompttest.Use(t, f, c.steps...)
			err := run(NewCmdPaycode(f), "create")
			require.EqualError(t, err, c.want)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Empty(t, captured.method, "nothing is sent")
		})
	}
}

func TestCreate_TerminalChecksFlagsBeforeAsking(t *testing.T) {
	f, _, _, _ := createServer(t, http.StatusCreated, createdData)
	prompttest.Use(t, f)
	err := run(NewCmdPaycode(f), "create", "--character-type", "Letters")
	require.EqualError(t, err, "--character-type must be NumberOnly or NumberAndLetter")
}

func TestCreate_UnattendedStillNamesTheMissingFlag(t *testing.T) {
	for _, noInput := range []bool{false, true} {
		f, _, _, captured := createServer(t, http.StatusCreated, createdData)
		if noInput {
			prompttest.Use(t, f)
			f.Global.NoInput = true
		}
		err := run(NewCmdPaycode(f), "create")
		require.EqualError(t, err, "--suffix-from is required")
		assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		assert.Empty(t, captured.method)

		err = run(NewCmdPaycode(f), append([]string{"create"}, createFlags...)...)
		require.EqualError(t, err, "a pay code prefix is required")
	}
}

const twoStructures = `{"PayCode":"on","PayCodeStructures":[` +
	`{"is_active":"on","prefix":"DH","suffix_from":3,"suffix_to":10,"character_type":"NumberOnly"},` +
	`{"is_active":"off","prefix":"HD","suffix_from":6,"suffix_to":12,"character_type":"NumberAndLetter"}],"version":"v7"}`

func structureAt(t *testing.T, c *capturedWrite, i int) map[string]any {
	t.Helper()
	structs, ok := c.body["PayCodeStructures"].([]any)
	require.True(t, ok, "body has no structure list: %v", c.body)
	return structs[i].(map[string]any)
}

func TestUpdate_TerminalEditsThePickedFields(t *testing.T) {
	f, captured := setServer(t, twoStructures)
	prompts := prompttest.Use(t, f,
		prompttest.Step{Label: "Structure to edit", Answer: "HD · 6-12 · NumberAndLetter · off"},
		prompttest.Step{Label: "What do you want to edit?", Answer: []string{"Suffix length (6-12)", "Active (off)", "Pay code detection (on)"}},
		prompttest.Step{Label: "Suffix length from (1-30)", Answer: ""},
		prompttest.Step{Label: "Suffix length to (6-30)", Answer: "20"},
		prompttest.Step{Label: "Active", Answer: true},
		prompttest.Step{Label: "Pay code detection", Answer: false},
	)

	require.NoError(t, run(NewCmdPaycode(f), "update"))

	assert.Equal(t, "6", prompts.Defaults["Suffix length from (1-30)"], "each question offers the stored value")
	assert.Equal(t, "12", prompts.Defaults["Suffix length to (6-30)"])
	assert.Equal(t, false, prompts.Defaults["Active"])
	assert.Equal(t, true, prompts.Defaults["Pay code detection"])

	assert.Equal(t, "v7", captured.ifMatch)
	assert.Equal(t, "off", captured.body["PayCode"])
	edited := structureAt(t, captured, 1)
	assert.Equal(t, map[string]any{
		"is_active": "on", "prefix": "HD", "suffix_from": float64(6), "suffix_to": float64(20), "character_type": "NumberAndLetter",
	}, edited, "Enter keeps the stored bound; fields not picked keep theirs")
	assert.Equal(t, "DH", structureAt(t, captured, 0)["prefix"], "the other structure is untouched")
}

func TestUpdate_TerminalPrefixAndCharacterType(t *testing.T) {
	f, captured := setServer(t, twoStructures)
	prompts := prompttest.Use(t, f,
		prompttest.Step{Label: "Structure to edit", Answer: "DH · 3-10 · NumberOnly · on"},
		prompttest.Step{Label: "What do you want to edit?", Answer: []string{"Prefix (DH)", "Character type (NumberOnly)"}},
		prompttest.Step{Label: "Pay code prefix (2-5 letters)", Answer: "SP"},
		prompttest.Step{Label: "Character type", Answer: "NumberAndLetter"},
	)

	require.NoError(t, run(NewCmdPaycode(f), "update"))

	assert.Equal(t, "DH", prompts.Defaults["Pay code prefix (2-5 letters)"])
	edited := structureAt(t, captured, 0)
	assert.Equal(t, "SP", edited["prefix"])
	assert.Equal(t, "NumberAndLetter", edited["character_type"])
	assert.Equal(t, "on", edited["is_active"])
	assert.Equal(t, "on", captured.body["PayCode"])
}

func TestUpdate_TerminalNothingPickedSendsNothing(t *testing.T) {
	f, captured := setServer(t, twoStructures)
	prompttest.Use(t, f,
		prompttest.Step{Label: "Structure to edit", Answer: "DH · 3-10 · NumberOnly · on"},
		prompttest.Step{Label: "What do you want to edit?", Answer: []string{}},
	)
	err := run(NewCmdPaycode(f), "update")
	require.EqualError(t, err, "provide at least one field to update")
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Empty(t, captured.method)
}

func TestUpdate_TerminalRefusesAnInvertedSuffixRange(t *testing.T) {
	f, captured := setServer(t, twoStructures)
	prompttest.Use(t, f,
		prompttest.Step{Label: "Structure to edit", Answer: "DH · 3-10 · NumberOnly · on"},
		prompttest.Step{Label: "What do you want to edit?", Answer: []string{"Suffix length (3-10)"}},
		prompttest.Step{Label: "Suffix length from (1-30)", Answer: "12"},
		prompttest.Step{Label: "Suffix length to (12-30)", Answer: "10"},
	)
	err := run(NewCmdPaycode(f), "update")
	require.EqualError(t, err, "--suffix-to must be between 12 and 30")
	assert.Empty(t, captured.method)
}

func TestUpdate_TerminalWithNoStructureAsksOnlyThePrefix(t *testing.T) {
	f, captured := setServer(t, `{"PayCode":"on","PayCodeStructures":[]}`)
	prompttest.Use(t, f, prompttest.Step{Label: "Pay code prefix (2-5 letters)", Answer: "DH"})
	require.NoError(t, run(NewCmdPaycode(f), "update"))
	assert.Equal(t, "DH", structureAt(t, captured, 0)["prefix"])
	assert.EqualValues(t, 3, structureAt(t, captured, 0)["suffix_from"], "the rest is seeded")
}

func TestUpdate_TerminalWithFlagsAsksNothing(t *testing.T) {
	f, captured := setServer(t, twoStructures)
	prompttest.Use(t, f)
	require.NoError(t, run(NewCmdPaycode(f), "update", "--structure", "HD", "--suffix-to", "15"))
	assert.EqualValues(t, 15, structureAt(t, captured, 1)["suffix_to"])
}

func TestUpdate_TerminalToDefaultsUpToANewFrom(t *testing.T) {
	f, captured := setServer(t, twoStructures)
	prompts := prompttest.Use(t, f,
		prompttest.Step{Label: "Structure to edit", Answer: "DH · 3-10 · NumberOnly · on"},
		prompttest.Step{Label: "What do you want to edit?", Answer: []string{"Suffix length (3-10)"}},
		prompttest.Step{Label: "Suffix length from (1-30)", Answer: "12"},
		prompttest.Step{Label: "Suffix length to (12-30)", Answer: ""},
	)
	require.NoError(t, run(NewCmdPaycode(f), "update"))
	assert.Equal(t, "12", prompts.Defaults["Suffix length to (12-30)"], "the stored 10 is below the new from, so from is offered")
	assert.EqualValues(t, 12, structureAt(t, captured, 0)["suffix_from"])
	assert.EqualValues(t, 12, structureAt(t, captured, 0)["suffix_to"], "from equal to to is a fixed length")
}

func TestCreate_FixedLengthIsAccepted(t *testing.T) {
	f, _, _, captured := createServer(t, http.StatusCreated, createdData)
	require.NoError(t, run(NewCmdPaycode(f), "create", "HD", "--character-type", "NumberOnly", "--suffix-from", "8", "--suffix-to", "8", "--active"))
	assert.Equal(t, float64(8), captured.body["suffix_from"])
	assert.Equal(t, float64(8), captured.body["suffix_to"])
}

func TestCreate_TerminalFromIsCappedByAGivenTo(t *testing.T) {
	f, _, _, captured := createServer(t, http.StatusCreated, createdData)
	prompttest.Use(t, f,
		prompttest.Step{Label: "Suffix length from (1-5)", Answer: "5"},
		prompttest.Step{Label: "Active", Answer: true},
	)
	require.NoError(t, run(NewCmdPaycode(f), "create", "HD", "--character-type", "NumberOnly", "--suffix-to", "5"))
	assert.Equal(t, float64(5), captured.body["suffix_from"])
}

func TestUpdate_OneBoundMustNotCrossTheStoredOne(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--suffix-from", "12"}, "suffix length from 12 is greater than to 10; pass --suffix-from and --suffix-to together"},
		{[]string{"--suffix-to", "2"}, "suffix length from 3 is greater than to 2; pass --suffix-from and --suffix-to together"},
	}
	for _, c := range cases {
		f, captured := setServer(t, twoStructures)
		err := run(NewCmdPaycode(f), append([]string{"update", "--structure", "DH"}, c.args...)...)
		require.EqualError(t, err, c.want)
		assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		assert.Empty(t, captured.method, "nothing is sent")
	}
}

func TestUpdate_RefusesAPrefixAnotherStructureHas(t *testing.T) {
	f, captured := setServer(t, twoStructures)
	err := run(NewCmdPaycode(f), "update", "hd", "--structure", "DH")
	require.EqualError(t, err, `prefix "hd" is already used by another pay code structure`)
	assert.Empty(t, captured.method)

	f, captured = setServer(t, twoStructures)
	require.NoError(t, run(NewCmdPaycode(f), "update", "dh", "--structure", "DH"), "a structure may keep its own prefix in another case")
	assert.Equal(t, "dh", structureAt(t, captured, 0)["prefix"])
}

func TestUpdate_ProductionConfirmationNamesTheVerb(t *testing.T) {
	f, _ := setServer(t, twoStructures)
	f.Global.Env = "production"
	f.Global.Yes = true
	ios := f.IOStreams
	require.NoError(t, run(NewCmdPaycode(f), "update", "--structure", "DH", "--suffix-to", "9"))
	errOut := ios.ErrOut.(*bytes.Buffer).String()
	assert.Contains(t, errOut, "! live: update pay code targets PRODUCTION")
	assert.NotContains(t, errOut, "set pay code")
}

// Answering the field prompts is not consent to write production: the confirmation still
// follows them, and declining it sends nothing.
func TestTerminal_ProductionConfirmsAfterThePrompts(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		f, _, _, captured := createServer(t, http.StatusCreated, createdData)
		f.Global.Env = "production"
		prompttest.Use(t, f,
			prompttest.Step{Label: "Pay code prefix (2-5 letters)", Answer: "HD"},
			prompttest.Step{Label: "Character type", Answer: "NumberOnly"},
			prompttest.Step{Label: "Suffix length from (1-30)", Answer: "6"},
			prompttest.Step{Label: "Suffix length to (6-30)", Answer: "12"},
			prompttest.Step{Label: "Active", Answer: true},
			prompttest.Step{Label: "create pay code structure HD on production?", Answer: false},
		)
		err := run(NewCmdPaycode(f), "create")
		require.Error(t, err)
		assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		assert.Empty(t, captured.method, "nothing is sent once the confirmation is declined")
	})
	t.Run("update", func(t *testing.T) {
		f, captured := setServer(t, twoStructures)
		f.Global.Env = "production"
		prompttest.Use(t, f,
			prompttest.Step{Label: "Structure to edit", Answer: "HD · 6-12 · NumberAndLetter · off"},
			prompttest.Step{Label: "What do you want to edit?", Answer: []string{"Active (off)"}},
			prompttest.Step{Label: "Active", Answer: true},
			prompttest.Step{Label: "update pay code on production?", Answer: false},
		)
		err := run(NewCmdPaycode(f), "update")
		require.Error(t, err)
		assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		assert.Empty(t, captured.method, "nothing is sent once the confirmation is declined")
	})
}

func TestCreate_TerminalChecksTheGivenPrefixBeforeAsking(t *testing.T) {
	f, _, _, captured := createServer(t, http.StatusCreated, createdData)
	prompttest.Use(t, f)
	err := run(NewCmdPaycode(f), "create", "H1")
	require.EqualError(t, err, `prefix must be 2-5 letters, got "H1"`)
	assert.Empty(t, captured.method)
}

func TestUpdate_TerminalCharacterTypeStartsOnTheStoredOne(t *testing.T) {
	f, captured := setServer(t, twoStructures)
	prompts := prompttest.Use(t, f,
		prompttest.Step{Label: "Structure to edit", Answer: "HD · 6-12 · NumberAndLetter · off"},
		prompttest.Step{Label: "What do you want to edit?", Answer: []string{"Character type (NumberAndLetter)"}},
		prompttest.Step{Label: "Character type", Answer: "NumberAndLetter"},
	)
	require.NoError(t, run(NewCmdPaycode(f), "update"))
	assert.Equal(t, "NumberAndLetter", prompts.Defaults["Character type"], "Enter keeps the stored type")
	assert.Equal(t, "NumberAndLetter", structureAt(t, captured, 1)["character_type"])
}

func TestUpdate_TerminalPayCodeDetectionStartsOnTheStoredValue(t *testing.T) {
	f, _ := setServer(t, strings.Replace(twoStructures, `"PayCode":"on"`, `"PayCode":"off"`, 1))
	prompts := prompttest.Use(t, f,
		prompttest.Step{Label: "Structure to edit", Answer: "DH · 3-10 · NumberOnly · on"},
		prompttest.Step{Label: "What do you want to edit?", Answer: []string{"Pay code detection (off)"}},
		prompttest.Step{Label: "Pay code detection", Answer: false},
	)
	require.NoError(t, run(NewCmdPaycode(f), "update"))
	assert.Equal(t, false, prompts.Defaults["Pay code detection"], "Enter must not turn detection back on")
}

func TestCreate_TerminalChecksTheSuffixRangeBeforeAsking(t *testing.T) {
	cases := map[string][]string{
		"--suffix-from must be between 1 and 30":                  {"--suffix-from", "40"},
		"--suffix-from must be less than or equal to --suffix-to": {"--suffix-from", "9", "--suffix-to", "4"},
	}
	for want, args := range cases {
		t.Run(want, func(t *testing.T) {
			f, _, _, captured := createServer(t, http.StatusCreated, createdData)
			prompttest.Use(t, f)
			err := run(NewCmdPaycode(f), append([]string{"create"}, args...)...)
			require.EqualError(t, err, want)
			assert.Empty(t, captured.method)
		})
	}
}
