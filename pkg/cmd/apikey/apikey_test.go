package apikey

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
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only create's reply carries the full key; other endpoints return the server's hint.
const fullKey = "A1B2C3D4E5F60718293A4B5C6D7E8F90A1B2C3D4E5F60718293A4B5C6D7E7F9C" // gitleaks:allow

const maskedKey = "****7F9C"

const oneList = `{"status":"success","data":[{"id":"ak_1","name":"demo-app","api_key":"` + maskedKey + `","active":1,"created_at":"2026-07-29 09:00:00"}]}`

const oneKey = `{"status":"success","data":{"id":"ak_1","name":"demo-app","api_key":"` + maskedKey + `","active":1,"created_at":"2026-07-29 09:00:00"}}`

const createdKey = `{"status":"success","data":{"id":"ak_1","name":"demo-app","api_key":"` + fullKey + `","active":1,"created_at":"2026-07-29 09:00:00"}}`

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

func TestList_PrintsServerHintVerbatim(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/apikey", r.URL.Path)
		_, _ = w.Write([]byte(oneList))
	})
	require.NoError(t, run(NewCmdAPIKey(f), "list"))

	s := out.String()
	// Printed as received: masking the hint again would hide its last four.
	assert.Contains(t, s, maskedKey)
	assert.NotContains(t, s, fullKey)
	assert.Contains(t, s, "demo-app")
}

func TestList_RevealFlagIsGone(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server for an unknown flag")
	})
	err := run(NewCmdAPIKey(f), "list", "--reveal")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown flag")
}

func TestList_JSONMasksKey(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(oneList))
	})
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdAPIKey(f), "list"))

	s := out.String()
	assert.Contains(t, s, `"id": "ak_1"`)
	assert.Contains(t, s, `"api_key": "`+maskedKey+`"`)
	assert.Contains(t, s, `"active": 1`)
	assert.NotContains(t, s, fullKey)
}

func TestList_Empty(t *testing.T) {
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":[]}`))
	})
	require.NoError(t, run(NewCmdAPIKey(f), "list"))
	assert.Contains(t, errOut.String(), "No API keys found.")
}

func TestGet_PathAndMask(t *testing.T) {
	var gotPath string
	f, out, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(oneKey))
	})
	require.NoError(t, run(NewCmdAPIKey(f), "get", "ak_1"))

	assert.Equal(t, "/apikey/ak_1", gotPath)
	assert.Contains(t, out.String(), maskedKey)
	assert.NotContains(t, out.String(), fullKey)
}

func TestGet_RevealFlagIsGone(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server for an unknown flag")
	})
	err := run(NewCmdAPIKey(f), "get", "ak_1", "--reveal")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown flag")
}

func TestGet_RequiresXIDNonInteractive(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server without an xid")
	})
	err := run(NewCmdAPIKey(f), "get")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestCreate_PostsNameAndActive(t *testing.T) {
	var method, path, idemKey string
	var body map[string]any
	f, out, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, idemKey = r.Method, r.URL.Path, r.Header.Get("X-Idempotency-Key")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(createdKey))
	})
	require.NoError(t, run(NewCmdAPIKey(f), "create", "--name", "  demo-app  "))

	assert.Equal(t, http.MethodPost, method)
	assert.Equal(t, "/apikey", path)
	assert.Equal(t, "demo-app", body["name"], "name is trimmed before sending")
	assert.Equal(t, float64(1), body["active"], "active is always sent explicitly")
	assert.NotEmpty(t, idemKey)
	assert.Contains(t, out.String(), fullKey, "create returns the key in full")
	assert.Contains(t, errOut.String(), "Created API key ak_1.")
}

func TestCreate_ActiveFalse(t *testing.T) {
	var body map[string]any
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(createdKey))
	})
	require.NoError(t, run(NewCmdAPIKey(f), "create", "--name", "off", "--active=false"))
	assert.Equal(t, float64(0), body["active"])
}

func TestCreate_NameValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing name", []string{"create"}},
		{"blank name", []string{"create", "--name", "   "}},
		{"name over 200 characters", []string{"create", "--name", strings.Repeat("x", 201)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("must not call the server with an invalid name")
			})
			err := run(NewCmdAPIKey(f), c.args...)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
		})
	}
}

func TestUpdate_SendsOnlyChangedFields(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want map[string]any
	}{
		{"name only", []string{"update", "ak_1", "--name", "renamed"}, map[string]any{"name": "renamed"}},
		{"disable only", []string{"update", "ak_1", "--active=false"}, map[string]any{"active": float64(0)}},
		{"enable only", []string{"update", "ak_1", "--active=true"}, map[string]any{"active": float64(1)}},
		{"both", []string{"update", "ak_1", "--name", "renamed", "--active=false"}, map[string]any{"name": "renamed", "active": float64(0)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var method, path string
			var body map[string]any
			f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
				method, path = r.Method, r.URL.Path
				b, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(b, &body)
				_, _ = w.Write([]byte(oneKey))
			})
			require.NoError(t, run(NewCmdAPIKey(f), c.args...))
			assert.Equal(t, http.MethodPut, method)
			assert.Equal(t, "/apikey/ak_1", path)
			assert.Equal(t, c.want, body)
		})
	}
}

func TestUpdate_NothingToUpdate(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not call the server for an empty patch")
	})
	err := run(NewCmdAPIKey(f), "update", "ak_1")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestUpdate_EchoesTheHintNotTheKey(t *testing.T) {
	f, out, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(oneKey))
	})
	require.NoError(t, run(NewCmdAPIKey(f), "update", "ak_1", "--name", "renamed"))
	assert.Contains(t, out.String(), maskedKey)
	assert.NotContains(t, out.String(), fullKey)
}

func TestDelete_ConfirmedWithYes(t *testing.T) {
	var method, path string
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	f.Global.Yes = true
	require.NoError(t, run(NewCmdAPIKey(f), "delete", "ak_1"))

	assert.Equal(t, http.MethodDelete, method)
	assert.Equal(t, "/apikey/ak_1", path)
	assert.Contains(t, errOut.String(), "Deleted API key ak_1.")
}

func TestDelete_NeedsConfirmNonInteractive(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not delete without confirmation")
	})
	err := run(NewCmdAPIKey(f), "delete", "ak_1")
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

func TestProductionEnvRefused(t *testing.T) {
	for _, args := range [][]string{{"list"}, {"get", "ak_1"}, {"create", "--name", "x"}, {"update", "ak_1", "--name", "x"}, {"delete", "ak_1"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
				t.Error("must not reach the server on production")
			})
			f.Global.Env = config.EnvProduction
			f.Global.Yes = true
			err := run(NewCmdAPIKey(f), args...)
			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), "sandbox-only")
		})
	}
}

func TestCreate_MalformedIdempotencyKeyStopsTheWrite(t *testing.T) {
	f, _, _ := testFactory(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("must not mint a key with a malformed idempotency key")
	})
	f.Global.IdempotencyKey = "abcdefg"
	f.Global.Yes = true

	err := run(NewCmdAPIKey(f), "create", "--name", "zz-verify")

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "--idempotency-key must be")
}

func TestCreate_ValidIdempotencyKeyIsSentVerbatim(t *testing.T) {
	var got string
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Idempotency-Key")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(createdKey))
	})
	f.Global.IdempotencyKey = "e2e-idem-1789282392"
	f.Global.Yes = true

	require.NoError(t, run(NewCmdAPIKey(f), "create", "--name", "demo-app"))
	assert.Equal(t, "e2e-idem-1789282392", got)
}
