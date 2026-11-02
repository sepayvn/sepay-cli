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

// capturedPost records the set command's POST.
type capturedPost struct {
	method      string
	path        string
	idempotency string
	ifMatch     string
	body        map[string]any
}

// setServer serves GET with current as the data payload and captures the POST.
func setServer(t *testing.T, current string) (*cmdutil.Factory, *capturedPost) {
	t.Helper()
	captured := &capturedPost{}
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

func firstStructure(t *testing.T, c *capturedPost) map[string]any {
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

	assert.Equal(t, http.MethodPost, captured.method)
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
		check func(t *testing.T, c *capturedPost)
	}{
		"disable": {[]string{"--disable"}, func(t *testing.T, c *capturedPost) {
			assert.Equal(t, "off", c.body["PayCode"])
		}},
		"enable": {[]string{"--enable"}, func(t *testing.T, c *capturedPost) {
			assert.Equal(t, "on", c.body["PayCode"])
		}},
		"suffix range": {[]string{"--suffix-from", "4", "--suffix-to", "8"}, func(t *testing.T, c *capturedPost) {
			s := firstStructure(t, c)
			assert.EqualValues(t, 4, s["suffix_from"])
			assert.EqualValues(t, 8, s["suffix_to"])
		}},
		"active off": {[]string{"--active=false"}, func(t *testing.T, c *capturedPost) {
			assert.Equal(t, "off", firstStructure(t, c)["is_active"])
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f, captured := setServer(t, current)

			require.NoError(t, run(NewCmdPaycode(f), append([]string{"set"}, tc.args...)...))

			assert.Equal(t, http.MethodPost, captured.method)
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
	assert.NotEqual(t, http.MethodPost, captured.method, "nothing is written")
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

	assert.Empty(t, captured.idempotency, "config replace is naturally idempotent; no key sent")
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
	assert.Empty(t, captured.method, "nothing may be POSTed")
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
				if r.Method == http.MethodPost {
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
		if r.Method == http.MethodPost {
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
