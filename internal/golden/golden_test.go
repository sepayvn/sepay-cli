package golden

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssert_UpdateWritesThenMatches(t *testing.T) {
	t.Chdir(t.TempDir())
	*update = true
	t.Cleanup(func() { *update = false })

	Assert(t, "sample", []byte("{}\n"))
	got, err := os.ReadFile(filepath.Join("testdata", "sample.golden"))
	require.NoError(t, err)
	assert.Equal(t, "{}\n", string(got))

	*update = false
	Assert(t, "sample", []byte("{}\n"))
}

// recorder notes a failure instead of stopping the real test.
type recorder struct {
	testing.TB
	failed bool
}

func (r *recorder) Helper()               {}
func (r *recorder) Errorf(string, ...any) { r.failed = true }

func TestAssert_DetectsADifference(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll("testdata", 0o755))
	require.NoError(t, os.WriteFile(filepath.Join("testdata", "sample.golden"), []byte("old\n"), 0o644))

	r := &recorder{TB: t}
	Assert(r, "sample", []byte("new\n"))
	assert.True(t, r.failed)
}
