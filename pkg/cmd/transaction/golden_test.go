package transaction

import (
	"testing"

	"github.com/sepayvn/sepay-cli/internal/golden"

	"github.com/stretchr/testify/require"
)

func TestList_JSON_Golden(t *testing.T) {
	f, out, _ := testFactory(t, okJSON(oneTxList))
	f.Global.Output = "json"
	require.NoError(t, run(NewCmdTransaction(f), "list"))
	golden.Assert(t, "list_json", out.Bytes())
}
