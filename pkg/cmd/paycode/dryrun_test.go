package paycode

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type paycodeDryRun struct {
	DryRun  bool `json:"dry_run"`
	Request struct {
		Method  string         `json:"method"`
		Path    string         `json:"path"`
		Headers []string       `json:"headers"`
		Body    map[string]any `json:"body"`
	} `json:"request"`
	Before     map[string]any `json:"before"`
	After      map[string]any `json:"after"`
	NotChecked []string       `json:"not_checked"`
}

// readOnlyServer answers GET with twoStructures and fails the test on any write.
func readOnlyServer(t *testing.T) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	return testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("dry run sent %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":` + twoStructures + `}`))
	})
}

func dryRunPaycode(t *testing.T, env string, args ...string) paycodeDryRun {
	t.Helper()
	f, out, _ := readOnlyServer(t)
	f.Global.Output = "json"
	f.Global.Env = env
	require.NoError(t, run(NewCmdPaycode(f), append(args, "--dry-run")...))
	var got paycodeDryRun
	require.NoError(t, json.Unmarshal(out.Bytes(), &got), out.String())
	return got
}

func listAt(t *testing.T, m map[string]any, i int) map[string]any {
	t.Helper()
	list, ok := m["PayCodeStructures"].([]any)
	require.True(t, ok)
	require.Greater(t, len(list), i)
	return list[i].(map[string]any)
}

// mergePaycode writes into a copy, so before keeps the stored value: a preview that printed the
// new value on both sides would read as "nothing changes".
func TestDryRunUpdate_BeforeKeepsTheStoredValues(t *testing.T) {
	got := dryRunPaycode(t, config.EnvSandbox, "update", "--structure", "DH", "--suffix-to", "9")
	assert.Equal(t, http.MethodPut, got.Request.Method)
	assert.Equal(t, "/company/paycode", got.Request.Path)
	assert.EqualValues(t, 10, listAt(t, got.Before, 0)["suffix_to"])
	assert.EqualValues(t, 9, listAt(t, got.After, 0)["suffix_to"])
	assert.Equal(t, listAt(t, got.Before, 1), listAt(t, got.After, 1), "the other structure is untouched")
	assert.Equal(t, "v7", got.Before["version"])
	assert.NotContains(t, got.Request.Body, "version", "the version rides If-Match, not the body")
}

func TestMergePaycode_LeavesTheCurrentListAlone(t *testing.T) {
	cur := Paycode{PayCode: "on", Structures: []Structure{{IsActive: "on", Prefix: "DH", SuffixFrom: 3, SuffixTo: 10, CharacterType: "NumberOnly"}}}
	to := 9
	merged := mergePaycode(cur, updateOptions{suffixTo: &to, prefix: "XY"}, 0)
	assert.Equal(t, 10, cur.Structures[0].SuffixTo)
	assert.Equal(t, "DH", cur.Structures[0].Prefix)
	assert.Equal(t, 9, merged.Structures[0].SuffixTo)
	assert.Equal(t, "XY", merged.Structures[0].Prefix)
}

func TestDryRunUpdate_TableRows(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
		none bool
	}{
		{"suffix", []string{"update", "--structure", "DH", "--suffix-to", "9"}, "PayCodeStructures[0].suffix_to", false},
		{"toggle detection", []string{"update", "--disable"}, "PayCode", false},
		{"prefix of the second structure", []string{"update", "MM", "--structure", "HD"}, "PayCodeStructures[1].prefix", false},
		{"the stored value again", []string{"update", "--structure", "DH", "--suffix-to", "10"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, out, errOut := readOnlyServer(t)
			require.NoError(t, run(NewCmdPaycode(f), append(tc.args, "--dry-run")...))
			assert.Contains(t, errOut.String(), cmdutil.DryRunNotice)
			if tc.none {
				assert.Contains(t, errOut.String(), "No field would change.")
				return
			}
			assert.Contains(t, out.String(), tc.want)
			assert.NotContains(t, out.String(), "version")
		})
	}
}

func TestDryRunCreate(t *testing.T) {
	got := dryRunPaycode(t, config.EnvProduction, "create", "DH", "--suffix-from", "3", "--suffix-to", "10", "--character-type", "NumberOnly", "--active")
	assert.Equal(t, http.MethodPost, got.Request.Method)
	assert.Nil(t, got.Before)
	assert.Equal(t, got.Request.Body, got.After)
	assert.Equal(t, "DH", got.After["prefix"], "a prefix the list has is the server's to refuse")

	f, out, errOut := readOnlyServer(t)
	require.NoError(t, run(NewCmdPaycode(f), "create", "DH", "--suffix-from", "3", "--suffix-to", "10", "--character-type", "NumberOnly", "--active=false", "--dry-run"))
	assert.Contains(t, out.String(), "is_active")
	assert.Contains(t, out.String(), "off")
	assert.Contains(t, errOut.String(), "! not checked: "+cmdutil.DryRunNotChecked)
}

// The row the contract shows: the field, the stored value, then the value sent.
func TestDryRunUpdate_TableRowIsBeforeThenAfter(t *testing.T) {
	f, out, _ := readOnlyServer(t)
	require.NoError(t, run(NewCmdPaycode(f), "update", "--structure", "DH", "--suffix-to", "9", "--dry-run"))
	assert.Regexp(t, `(?m)^PayCodeStructures\[0\]\.suffix_to +10 +9$`, out.String())
}

// The server returns a new version, so after carries none and the table never shows it changing;
// the request names If-Match, which carries the stored one.
func TestDryRunUpdate_VersionRidesIfMatch(t *testing.T) {
	got := dryRunPaycode(t, config.EnvSandbox, "update", "--structure", "DH", "--suffix-to", "9")
	assert.NotContains(t, got.After, "version")
	assert.Equal(t, []string{"If-Match"}, got.Request.Headers)
	assert.Equal(t, []string{cmdutil.DryRunNotChecked}, got.NotChecked)

	f, out, _ := readOnlyServer(t)
	require.NoError(t, run(NewCmdPaycode(f), "update", "--structure", "DH", "--suffix-to", "9", "--dry-run"))
	assert.NotContains(t, out.String(), "version")
}
