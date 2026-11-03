package paycode

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
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

func TestGet_Table(t *testing.T) {
	f, out, errOut := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"}]}}`))
	})
	require.NoError(t, run(NewCmdPaycode(f), "get"))
	assert.Contains(t, errOut.String(), "Pay-code: on")
	assert.Contains(t, out.String(), "DH")
	assert.Contains(t, out.String(), "NumberAndLetter")
}

func TestGet_JSON(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"PayCode":"off","PayCodeStructures":[]}}`))
	})
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdPaycode(f), "get"))
	assert.Contains(t, out.String(), `"PayCode": "off"`)
	assert.Contains(t, out.String(), `"PayCodeStructures": []`)
}

func TestSet_MergesActivePrefixPreservingRest(t *testing.T) {
	current := `{"PayCode":"on","PayCodeStructures":[` +
		`{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"},` +
		`{"is_active":"off","prefix":"XX","suffix_from":2,"suffix_to":8,"character_type":"NumberOnly"}]}`
	f, captured := setServer(t, current)

	require.NoError(t, run(NewCmdPaycode(f), "set", "SP"))

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

func TestSet_SuffixAndTypeFlagsOverride(t *testing.T) {
	f, captured := setServer(t, `{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"}]}`)

	require.NoError(t, run(NewCmdPaycode(f), "set", "SP", "--suffix-from", "5", "--suffix-to", "10", "--character-type", "NumberOnly"))

	s := firstStructure(t, captured)
	assert.EqualValues(t, 5, s["suffix_from"])
	assert.EqualValues(t, 10, s["suffix_to"])
	assert.Equal(t, "NumberOnly", s["character_type"])
}

func TestSet_DisableTogglesPayCodeOff(t *testing.T) {
	f, captured := setServer(t, `{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"}]}`)

	require.NoError(t, run(NewCmdPaycode(f), "set", "SP", "--disable"))

	assert.Equal(t, "off", captured.body["PayCode"])
}

func TestSet_FlagsAloneKeepTheStoredPrefix(t *testing.T) {
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

			require.NoError(t, run(NewCmdPaycode(f), append([]string{"set"}, tc.args...)...))

			assert.Equal(t, http.MethodPut, captured.method)
			assert.Equal(t, "DH", firstStructure(t, captured)["prefix"], "stored prefix kept")
			tc.check(t, captured)
		})
	}
}

func TestSet_FlagsAloneNeedAPrefixWhenNothingIsStored(t *testing.T) {
	f, captured := setServer(t, `{"PayCode":"on","PayCodeStructures":[]}`)

	err := run(NewCmdPaycode(f), "set", "--disable")

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "a pay-code prefix is required")
	assert.NotEqual(t, http.MethodPut, captured.method, "nothing is written")
}

func TestSet_EmptyCreatesDefaultStructure(t *testing.T) {
	f, captured := setServer(t, `{"PayCode":"on","PayCodeStructures":[]}`)

	require.NoError(t, run(NewCmdPaycode(f), "set", "SP"))

	s := firstStructure(t, captured)
	assert.Equal(t, "SP", s["prefix"])
	assert.EqualValues(t, 3, s["suffix_from"])
	assert.EqualValues(t, 10, s["suffix_to"])
	assert.Equal(t, "NumberOnly", s["character_type"])
	assert.Equal(t, "on", s["is_active"])
}

func TestSet_NoIdempotencyKey(t *testing.T) {
	f, captured := setServer(t, `{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"}]}`)

	require.NoError(t, run(NewCmdPaycode(f), "set", "SP"))

	assert.Empty(t, captured.idempotency, "set sends no key; If-Match guards the write")
}

// A PUT that may have landed is not resent: the repeat's If-Match would be stale and answer 409
// "nothing was saved" for a write that was saved.
func TestSet_ACutConnectionIsNotResent(t *testing.T) {
	const current = `{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":3,"suffix_to":10,"character_type":"NumberOnly"}],"version":"v1"}`
	puts := 0
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"status":"success","data":` + current + `}`))
			return
		}
		puts++
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

	err := run(NewCmdPaycode(f), "set", "HD")
	var transportErr *api.TransportError
	require.ErrorAs(t, err, &transportErr, "the outcome is unknown, so the error says the connection broke, not that nothing was saved")
	assert.Equal(t, 1, puts)
}

func TestSet_EnableDisableConflict(t *testing.T) {
	f, _ := setServer(t, `{"PayCode":"on","PayCodeStructures":[]}`)

	err := run(NewCmdPaycode(f), "set", "SP", "--enable", "--disable")

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestSet_RejectsPrefixAndSuffixOutOfRange(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"digits in prefix", []string{"set", "QAP11"}, "2-5 letters"},
		{"too short", []string{"set", "A"}, "2-5 letters"},
		{"too long", []string{"set", "ABCDEF"}, "2-5 letters"},
		{"suffix-from below range", []string{"set", "QAPA", "--suffix-from", "0"}, "--suffix-from must be between 1 and 30"},
		{"suffix-to above range", []string{"set", "QAPA", "--suffix-to", "31"}, "--suffix-to must be between 1 and 30"},
		{"inverted range", []string{"set", "QAPA", "--suffix-from", "10", "--suffix-to", "5"}, "less than or equal"},
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

func TestSet_AcceptsBoundaryValues(t *testing.T) {
	f, captured := setServer(t, `{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberOnly"}]}`)

	require.NoError(t, run(NewCmdPaycode(f), "set", "ABCDE", "--suffix-from", "1", "--suffix-to", "30"))

	s := firstStructure(t, captured)
	assert.Equal(t, "ABCDE", s["prefix"])
	assert.EqualValues(t, 1, s["suffix_from"])
	assert.EqualValues(t, 30, s["suffix_to"])
}

func TestSet_MissingPrefixIsUsageErrorWhenNotInteractive(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server without a prefix")
	})

	err := run(NewCmdPaycode(f), "set")

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "a pay-code prefix is required")
}

func TestSet_FlagsCheckedBeforeThePrompt(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server on an invalid flag combination")
	})

	err := run(NewCmdPaycode(f), "set", "--enable", "--disable")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--enable and --disable")
}

// A bool flag never consumes the next token, so a switch word would land as the prefix.
func TestSet_SwitchWordIsNotAPrefix(t *testing.T) {
	for _, word := range []string{"on", "off", "yes", "no", "true", "false"} {
		t.Run(word, func(t *testing.T) {
			f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("must not call the server with a switch word as the prefix")
			})

			err := run(NewCmdPaycode(f), "set", "--enable", word)

			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), "--enable is a switch, not a value")
		})
	}
}

func TestSet_SendsBackTheVersionItReadAsIfMatch(t *testing.T) {
	current := `{"PayCode":"on","version":"v1abc123",` +
		`"PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"}]}`
	f, captured := setServer(t, current)

	require.NoError(t, run(NewCmdPaycode(f), "set", "SP"))

	assert.Equal(t, "v1abc123", captured.ifMatch)
	_, inBody := captured.body["version"]
	assert.False(t, inBody, "version must not travel in the body")
}

func TestSet_OmitsVersionWhenServerSendsNone(t *testing.T) {
	f, captured := setServer(t, `{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"}]}`)

	require.NoError(t, run(NewCmdPaycode(f), "set", "SP"))

	assert.Empty(t, captured.ifMatch, "no If-Match when the server sent no version")
	_, inBody := captured.body["version"]
	assert.False(t, inBody)
}

func TestSet_RefusesLocallyWhenAStructureHasNoOnOffState(t *testing.T) {
	f, captured := setServer(t, `{"PayCode":"on","PayCodeStructures":[{"prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"}]}`)

	err := run(NewCmdPaycode(f), "set", "SP")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no on/off state")
	assert.Empty(t, captured.method, "nothing may be written")
}

func TestSet_WarnsWhenNoStructureIsActive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	require.NoError(t, run(NewCmdPaycode(f), "set", "SP"))
	assert.Contains(t, errOut.String(), "no pay-code structure is active")
}

func TestSet_DoesNotWarnWhenActiveWasSetExplicitly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	require.NoError(t, run(NewCmdPaycode(f), "set", "SP", "--active=false"))
	assert.NotContains(t, errOut.String(), "no pay-code structure is active")
}

func TestSet_ActiveIsABooleanMappedToOnOff(t *testing.T) {
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

			require.NoError(t, run(NewCmdPaycode(f), "set", "SP", tc.flag))
			assert.Contains(t, strings.ReplaceAll(sent, " ", ""), tc.want)
		})
	}
}

func TestSet_PrefixFlagPassesASwitchWord(t *testing.T) {
	for _, word := range []string{"on", "off", "yes", "no", "true", "false"} {
		t.Run(word, func(t *testing.T) {
			f, captured := setServer(t, `{"PayCode":"off","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberAndLetter"}]}`)

			require.NoError(t, run(NewCmdPaycode(f), "set", "--prefix", word, "--enable"))

			structs := captured.body["PayCodeStructures"].([]any)
			require.Len(t, structs, 1)
			assert.Equal(t, word, structs[0].(map[string]any)["prefix"])
			assert.Equal(t, "on", captured.body["PayCode"], "--enable still applies")
		})
	}
}

func TestSet_SwitchWordErrorNamesThePrefixFlag(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server")
	})

	err := run(NewCmdPaycode(f), "set", "--enable", "on")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "pass --prefix on")
}

func TestSet_PrefixGivenTwiceIsRefused(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server")
	})

	err := run(NewCmdPaycode(f), "set", "ERP", "--prefix", "ERP")

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "give the pay-code prefix once")
}

// The first-active fallback skips an inactive structure, so only --structure reaches it.
func TestSet_StructureTargetsAnInactiveStructure(t *testing.T) {
	current := `{"PayCode":"on","PayCodeStructures":[` +
		`{"is_active":"off","prefix":"HEHE","suffix_from":6,"suffix_to":8,"character_type":"NumberOnly"},` +
		`{"is_active":"on","prefix":"HAHA","suffix_from":16,"suffix_to":17,"character_type":"NumberOnly"}]}`
	f, captured := setServer(t, current)

	require.NoError(t, run(NewCmdPaycode(f), "set", "--structure", "hehe", "--active"))

	structs := captured.body["PayCodeStructures"].([]any)
	require.Len(t, structs, 2)
	assert.Equal(t, "on", structs[0].(map[string]any)["is_active"])
	assert.Equal(t, "HEHE", structs[0].(map[string]any)["prefix"])
	assert.Equal(t, "HAHA", structs[1].(map[string]any)["prefix"], "other structure untouched")
}

func TestSet_StructureRenamesTheNamedStructure(t *testing.T) {
	current := `{"PayCode":"on","PayCodeStructures":[` +
		`{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberOnly"},` +
		`{"is_active":"on","prefix":"XX","suffix_from":2,"suffix_to":8,"character_type":"NumberOnly"}]}`
	f, captured := setServer(t, current)

	require.NoError(t, run(NewCmdPaycode(f), "set", "SP", "--structure", "XX"))

	structs := captured.body["PayCodeStructures"].([]any)
	assert.Equal(t, "DH", structs[0].(map[string]any)["prefix"])
	assert.Equal(t, "SP", structs[1].(map[string]any)["prefix"])
}

func TestSet_UnknownStructureIsUsageError(t *testing.T) {
	var posted bool
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			posted = true
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"PayCode":"on","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberOnly"}]}}`))
	})

	err := run(NewCmdPaycode(f), "set", "SP", "--structure", "ZZ")

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), `no pay-code structure with prefix "ZZ" (have: DH)`)
	assert.False(t, posted)
}

func TestSet_PrintsThePayCodeToggle(t *testing.T) {
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"PayCode":"off","PayCodeStructures":[{"is_active":"on","prefix":"DH","suffix_from":1,"suffix_to":30,"character_type":"NumberOnly"}]}}`))
	})

	require.NoError(t, run(NewCmdPaycode(f), "set", "--disable"))
	assert.Contains(t, errOut.String(), "Pay-code: off")
}

func TestSet_StructurePrefersAnExactMatch(t *testing.T) {
	current := `{"PayCode":"on","PayCodeStructures":[` +
		`{"is_active":"on","prefix":"dh","suffix_from":1,"suffix_to":30,"character_type":"NumberOnly"},` +
		`{"is_active":"on","prefix":"DH","suffix_from":2,"suffix_to":8,"character_type":"NumberOnly"}]}`
	f, captured := setServer(t, current)

	require.NoError(t, run(NewCmdPaycode(f), "set", "SP", "--structure", "DH"))

	structs := captured.body["PayCodeStructures"].([]any)
	assert.Equal(t, "dh", structs[0].(map[string]any)["prefix"])
	assert.Equal(t, "SP", structs[1].(map[string]any)["prefix"])
}

func TestSet_WarnsWhenEcomConnectionsKeepTheOldPrefix(t *testing.T) {
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
		require.NoError(t, run(NewCmdPaycode(f), "set", "HD"))
		if tc.warns {
			assert.Contains(t, errOut.String(), "Warning: 2 Haravan/Shopify/Sapo connection(s) still use a pay-code prefix")
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
	assert.Contains(t, errOut.String(), "Created pay-code structure HD.")
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
		{"no prefix without a terminal", append([]string{"create"}, createFlags...), "a pay-code prefix is required"},
		{"prefix with a digit", append([]string{"create", "H1"}, createFlags...), "prefix must be 2-5 letters"},
		{"prefix too long", append([]string{"create", "ABCDEF"}, createFlags...), "prefix must be 2-5 letters"},
		{"prefix twice", append([]string{"create", "HD", "--prefix", "DH"}, createFlags...), "give the pay-code prefix once"},
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
