package jsonfmt

import (
	"bytes"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/golden"

	"github.com/stretchr/testify/require"
)

// sample covers what the machine formats must keep: key order from tags, raw UTF-8, URL characters
// left unescaped, null, and empty collections.
type sample struct {
	ID       string            `json:"id" yaml:"id"`
	Content  string            `json:"content" yaml:"content"`
	URL      string            `json:"url" yaml:"url"`
	Amount   int64             `json:"amount" yaml:"amount"`
	Code     *string           `json:"code" yaml:"code"`
	Tags     []string          `json:"tags" yaml:"tags"`
	Metadata map[string]string `json:"metadata" yaml:"metadata"`
}

var goldenSample = sample{
	ID:       "tx_1",
	Content:  "Thanh toán đơn hàng #42",
	URL:      "https://example.com/hook?a=1&b=<2>",
	Amount:   100000,
	Tags:     []string{},
	Metadata: map[string]string{},
}

func TestRenderJSON_Golden(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, RenderJSON(&buf, goldenSample))
	golden.Assert(t, "render_json", buf.Bytes())
}

func TestRenderYAML_Golden(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, RenderYAML(&buf, goldenSample))
	golden.Assert(t, "render_yaml", buf.Bytes())
}
