package root

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"
)

func TestRoot_VersionFlagHonoursJSON(t *testing.T) {
	ios, _, out, _ := iostreams.Test()
	cmd := NewCmdRoot(&cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}})
	cmd.SetArgs([]string{"--version", "--json"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))

	var got map[string]string
	require.NoError(t, json.Unmarshal(out.Bytes(), &got), out.String())
	assert.Contains(t, got, "version")
	assert.Contains(t, got, "commit")
}
